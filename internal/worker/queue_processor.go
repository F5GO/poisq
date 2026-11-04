// Package worker реализует фоновый обработчик персистентной очереди задач (lyrics_jobs)
// с автоматической постановкой на паузу при исчерпании квот API-ключей и атомарной
// записью готовых .lrc файлов рядом с треками в папку /music.
//
// Данный файл queue_processor.go связывает каскадный турнир провайдеров (LRC Tournament),
// валидатор Gemini 2.5 Flash, генератор караоке Groq Whisper и атомарный писатель .lrc.
package worker

import (
	"context"
	"errors"
	"fmt"
	"strings"
	"sync"
	"time"

	"github.com/F5GO/poisq/internal/ai"
	"github.com/F5GO/poisq/internal/db"
	"github.com/F5GO/poisq/internal/keypool"
	"github.com/F5GO/poisq/internal/logger"
	"github.com/F5GO/poisq/internal/lyrics"
)

// QueueProcessor управляет фоновым выполнением задач поиска текстов, AI-консенсуса и караоке.
type QueueProcessor struct {
	jobRepo    *db.JobRepository
	trackRepo  *db.TrackRepository
	cacheRepo  *db.ProviderCacheRepository
	keyPool    *keypool.Manager
	tournament *lyrics.TournamentOrchestrator
	cleaner    *ai.LyricsCleaner
	karaoke    *ai.KaraokePipeline
	lrcWriter  *lyrics.AtomicLRCWriter
	tracker    *JobTelemetryTracker

	mu       sync.RWMutex
	isPaused bool
	wakeCh   chan struct{}
}

// NewQueueProcessor создает новый диспетчер очереди фоновых задач.
func NewQueueProcessor(
	jobRepo *db.JobRepository,
	trackRepo *db.TrackRepository,
	cacheRepo *db.ProviderCacheRepository,
	keyPool *keypool.Manager,
	tournament *lyrics.TournamentOrchestrator,
	cleaner *ai.LyricsCleaner,
	karaoke *ai.KaraokePipeline,
	lrcWriter *lyrics.AtomicLRCWriter,
) *QueueProcessor {
	return &QueueProcessor{
		jobRepo:    jobRepo,
		trackRepo:  trackRepo,
		cacheRepo:  cacheRepo,
		keyPool:    keyPool,
		tournament: tournament,
		cleaner:    cleaner,
		karaoke:    karaoke,
		lrcWriter:  lrcWriter,
		tracker:    NewJobTelemetryTracker(),
		wakeCh:     make(chan struct{}, 1),
	}
}

// GetJobLogs возвращает форматированные строки лога выполнения для конкретной задачи.
func (qp *QueueProcessor) GetJobLogs(jobID int64) []string {
	if qp.tracker == nil {
		return nil
	}
	return qp.tracker.FormatLogLines(jobID)
}

// ClearJobLogs очищает журнал выполнения задачи при полном перезапуске.
func (qp *QueueProcessor) ClearJobLogs(jobID int64) {
	if qp.tracker != nil {
		qp.tracker.ClearJob(jobID)
	}
}

func (qp *QueueProcessor) reportStep(jobID int64, stage string, msg string) {
	if qp.tracker != nil {
		qp.tracker.RecordStep(jobID, stage, msg)
	}
	if qp.jobRepo != nil {
		_ = qp.jobRepo.UpdateProgress(jobID, msg)
	}
	logger.Info("[WORKER] [Задача #%d] %s", jobID, msg)
}

// Wake сигнализирует воркеру о появлении новой задачи в очереди.
func (qp *QueueProcessor) Wake() {
	select {
	case qp.wakeCh <- struct{}{}:
	default:
	}
}

// SetPaused позволяет поставить обработку очереди на паузу или возобновить её из веб-админки.
func (qp *QueueProcessor) SetPaused(paused bool) {
	qp.mu.Lock()
	qp.isPaused = paused
	qp.mu.Unlock()
	if !paused {
		qp.Wake()
	}
}

// IsPaused возвращает текущий статус паузы воркера.
func (qp *QueueProcessor) IsPaused() bool {
	qp.mu.RLock()
	defer qp.mu.RUnlock()
	return qp.isPaused
}

// Start запускает фоновый цикл обработки очереди до отмены контекста.
func (qp *QueueProcessor) Start(ctx context.Context) {
	go func() {
		ticker := time.NewTicker(4 * time.Second)
		defer ticker.Stop()

		for {
			select {
			case <-ctx.Done():
				return
			case <-qp.wakeCh:
				qp.processBatch(ctx)
			case <-ticker.C:
				qp.processBatch(ctx)
			}
		}
	}()
}

func (qp *QueueProcessor) processBatch(ctx context.Context) {
	for i := 0; i < 5; i++ {
		if ctx.Err() != nil || qp.IsPaused() {
			return
		}

		job, err := qp.jobRepo.ClaimNext(time.Now().Unix())
		if err != nil {
			if errors.Is(err, db.ErrNoPendingJobs) {
				return
			}
			logger.Warn("[WORKER] Ошибка выборки задачи из очереди: %v", err)
			return
		}

		logger.Info("[WORKER] Взята задача #%d в обработку: тип=%s, трекId=%s, попытка=%d", job.ID, job.JobType, job.TrackID, job.AttemptCount)
		jobStarted := time.Now()

		if err := qp.executeJob(ctx, job); err != nil {
			if errors.Is(err, db.ErrNoAvailableKeys) {
				if job.JobType == "whisper_karaoke" && (qp.keyPool == nil || !qp.keyPool.HasKeys("groq")) {
					msg := "Отсутствует ключ Groq Cloud в пуле. Добавьте его во вкладке «Ключи API»."
					qp.tracker.RecordStep(job.ID, "error", "✖ "+msg)
					_ = qp.jobRepo.MarkRetryOrFailed(job.ID, 3, 3, msg)
					logger.Warn("[WORKER] Задача #%d отклонена: в пуле нет ключа Groq Cloud для AI Whisper караоке", job.ID)
					return
				}
				retryAt := qp.keyPool.NextRetryTimestamp("gemini")
				groqRetry := qp.keyPool.NextRetryTimestamp("groq")
				if groqRetry > retryAt {
					retryAt = groqRetry
				}
				waitMsg := "Ожидание освобождения API-ключей: " + err.Error()
				qp.tracker.RecordStep(job.ID, "waiting", "⏳ "+waitMsg)
				_ = qp.jobRepo.MarkWaitingQuota(job.ID, retryAt, waitMsg)
				logger.Info("[WORKER] Ключи в кулдауне. Задача #%d переведена в ожидание до %v",
					job.ID, time.Unix(retryAt, 0).Format(time.RFC3339))
				return
			}

			qp.tracker.RecordStep(job.ID, "error", "✖ Ошибка: "+err.Error())
			_ = qp.jobRepo.MarkRetryOrFailed(job.ID, job.AttemptCount, 3, err.Error())
			logger.Warn("[WORKER] Задача #%d (трек %s) завершилась ошибкой через %s (попытка %d): %v",
				job.ID, job.TrackID, time.Since(jobStarted).Round(time.Millisecond), job.AttemptCount, err)
			continue
		}

		qp.tracker.RecordStep(job.ID, "done", "✓ Задача успешно завершена! Текст синхронизирован и сохранен на диск (.lrc).")
		_ = qp.jobRepo.MarkDone(job.ID)
		logger.Info("[WORKER] Задача #%d успешно завершена за %s!", job.ID, time.Since(jobStarted).Round(time.Millisecond))
	}
}

// ExecuteSingleTrack выполняет полный или специализированный пайплайн для одного трека.
func (qp *QueueProcessor) executeJob(ctx context.Context, job *db.LyricsJobRecord) error {
	track, err := qp.trackRepo.GetByID(job.TrackID)
	if err != nil {
		return err
	}

	logger.Info("[WORKER] [Задача #%d] Старт обработки: «%s — %s» (тип задачи: %s)", job.ID, track.Artist, track.Title, job.JobType)
	qp.reportStep(job.ID, "init", "Старт задачи для «"+track.Artist+" — "+track.Title+"» (режим: "+job.JobType+")")

	// Проверяем, есть ли уже сохраненный текст у трека или готовый кэш пословной транскрибации Whisper v3
	var refPlain string
	if track != nil {
		refPlain = ai.FastCleanStructuralTags(track.ActiveLyrics)
		if refPlain == "" {
			refPlain = ai.FastCleanStructuralTags(track.LyricsBody)
		}
	}

	var acousticJSON string
	if cachedList, err := qp.cacheRepo.ListByTrack(track.ID); err == nil {
		for _, c := range cachedList {
			if c.Provider == "whisper_words_v3" && strings.TrimSpace(c.RawLyrics) != "" {
				acousticJSON = c.RawLyrics
				break
			}
		}
	}

	var tRes *lyrics.TournamentResult
	// Если уже есть кэш Whisper v3 (повтор с этапа Gemini) или уже есть текст при запросе whisper_karaoke — не тратим время на повторный турнир!
	skipTournament := (job.JobType == "whisper_karaoke") && (acousticJSON != "" || refPlain != "")
	if !skipTournament {
		q := lyrics.SearchQuery{
			TrackID:  track.ID,
			Artist:   track.Artist,
			Title:    track.Title,
			Album:    track.Album,
			Duration: time.Duration(track.Duration) * time.Second,
		}

		// Шаг 1: Запуск параллельного LRC-турнира по 5 источникам
		qp.reportStep(job.ID, "tournament", "Этап 1: Параллельный опрос 5 внешних провайдеров (LRCLIB, NetEase, QQMusic, Musixmatch, Genius)...")
		var tErr error
		tRes, tErr = qp.tournament.RunTournament(ctx, q)
		if tErr != nil && !errors.Is(tErr, lyrics.ErrNotFound) {
			return tErr
		}

		if tRes != nil && tRes.Winner != nil {
			qp.reportStep(job.ID, "tournament", "Победитель турнира: "+tRes.Winner.ProviderID+" (тип: "+string(tRes.Winner.Lyrics.Kind)+")")
		} else {
			qp.reportStep(job.ID, "tournament", "Внешние базы не вернули готовый LRC-тайминг")
		}
	} else {
		qp.reportStep(job.ID, "tournament_skip", "Этап 1 пропущен: переходим сразу к акустической синхронизации караоке")
	}

	// Если нужен только быстрый турнир без облачного AI
	if job.JobType == "tournament" {
		if tRes == nil || tRes.Winner == nil {
			logger.Warn("[WORKER] [Задача #%d] Текст не найден ни в одном источнике для «%s — %s»", job.ID, track.Artist, track.Title)
			return lyrics.ErrNotFound
		}
		logger.Info("[WORKER] [Задача #%d] Турнир завершен победой %s (формат: %s, оценка: %.1f)",
			job.ID, tRes.Winner.ProviderID, tRes.Winner.Lyrics.Kind, tRes.Winner.Score.Total)
		return qp.saveWinningLyrics(track, tRes.Winner.ProviderID, tRes.Winner.Lyrics.Kind,
			selectBestText(tRes.Winner.Lyrics), track.LangCode, false)
	}

	// Шаг 2: Если найден синхронизированный LRC (и это не принудительный запуск whisper_karaoke) — проводим очистку и AI-консенсус
	if job.JobType != "whisper_karaoke" && tRes != nil && tRes.Winner != nil && tRes.Winner.Lyrics.Kind == lyrics.KindLRC {
		cleanedLocal := ai.FastCleanStructuralTags(tRes.Winner.Lyrics.SyncedLyrics)
		if sErr := qp.saveWinningLyrics(track, tRes.Winner.ProviderID, lyrics.KindLRC, cleanedLocal, track.LangCode, false); sErr != nil {
			logger.Warn("[WORKER] [Задача #%d] Не удалось сохранить текст: %v", job.ID, sErr)
		}

		if qp.cleaner != nil {
			qp.reportStep(job.ID, "consensus", "Этап 2: Валидация и очистка LRC-таймкодов через Google Gemini...")
			logger.Info("[WORKER] [Задача #%d] Запуск AI-консенсуса и валидации текста через Gemini...", job.ID)
			consensus, aiErr := qp.cleaner.BuildConsensus(ctx, track.Artist, track.Title, tRes.Candidates)
			if aiErr != nil {
				if errors.Is(aiErr, db.ErrNoAvailableKeys) && job.JobType == "ai_consensus" {
					return aiErr
				}
				logger.Warn("[WORKER] [Задача #%d] AI-консенсус не удался, оставлен локально очищенный LRC от %s: %v", job.ID, tRes.Winner.ProviderID, aiErr)
				qp.tracker.RecordStep(job.ID, "consensus_failed", "⚠ AI-консенсус не удался: "+aiErr.Error()+" (оставлен LRC провайдера)")
				return nil
			}
			if cErr := qp.cacheRepo.Upsert(db.ProviderCacheRecord{
				TrackID:   track.ID,
				Provider:  "poisq",
				Kind:      "lrc",
				Score:     99.5,
				RawLyrics: consensus.CleanedLyrics,
			}); cErr != nil {
				logger.Warn("[WORKER] [Задача #%d] Не удалось записать provider_cache: %v", job.ID, cErr)
			}
			return qp.saveWinningLyrics(track, "poisq", lyrics.KindLRC, consensus.CleanedLyrics, consensus.LangCode, true)
		}
		return nil
	}

	// Шаг 3: Если точных таймкодов нет или запущен whisper_karaoke -> запускаем облачное караоке Groq Whisper + Gemini!
	if tRes != nil && tRes.Winner != nil {
		candPlain := ai.FastCleanStructuralTags(tRes.Winner.Lyrics.PlainLyrics)
		if candPlain == "" {
			candPlain = ai.FastCleanStructuralTags(tRes.Winner.Lyrics.SyncedLyrics)
		}
		if candPlain != "" {
			refPlain = candPlain
			if job.JobType != "whisper_karaoke" {
				if sErr := qp.saveWinningLyrics(track, tRes.Winner.ProviderID, lyrics.KindPlain, refPlain, track.LangCode, false); sErr != nil {
					logger.Warn("[WORKER] [Задача #%d] Не удалось сохранить текст: %v", job.ID, sErr)
				}
			}
		}
	}

	if qp.karaoke != nil && (job.JobType == "whisper_karaoke" || job.JobType == "full_pipeline") {
		logger.Info("[WORKER] [Задача #%d] Старт караоке-пайплайна (Groq Whisper + Gemini) для «%s — %s»...", job.ID, track.Artist, track.Title)

		if acousticJSON != "" {
			qp.reportStep(job.ID, "whisper_cached", "Этап 2: Использован кэш пословной транскрибации Whisper v3 — переход сразу к выравниванию Gemini")
			logger.Info("[WORKER] [Задача #%d] Обнаружен кэш пословной транскрибации Whisper v3 — пропускаем аудио-транскрибацию!", job.ID)
		} else {
			qp.reportStep(job.ID, "whisper", "Этап 2: Сжатие аудио в моно-Opus (16 kHz) и пословная DTW-транскрибация через Groq Whisper Large v3...")
			logger.Info("[WORKER] [Задача #%d] Этап 1-2: Сжатие Opus и пословное распознавание через Groq Whisper Large v3...", job.ID)
			promptHint := track.Artist + " - " + track.Title
			rawJSON, wErr := qp.karaoke.TranscribeToSegments(ctx, track.FilePath, track.LangCode, promptHint)
			if wErr != nil {
				return wErr
			}
			acousticJSON = rawJSON
			if cErr := qp.cacheRepo.Upsert(db.ProviderCacheRecord{
				TrackID:   track.ID,
				Provider:  "whisper_words_v3",
				Kind:      "acoustic_json",
				Score:     50.0,
				RawLyrics: acousticJSON,
			}); cErr != nil {
				logger.Warn("[WORKER] [Задача #%d] Не удалось записать provider_cache: %v", job.ID, cErr)
			}
			qp.reportStep(job.ID, "whisper_done", "Этап 2 завершен: получены и откалиброваны пословные таймкоды вокала от Groq Whisper")
		}

		qp.reportStep(job.ID, "gemini", "Этап 3: Глобальное выравнивание Нидлмана — Вунша + синхронизация через Google Gemini 3.8 Flash...")
		logger.Info("[WORKER] [Задача #%d] Этап 3: Высокоточный пословный мердж и синхронизация строк через Google Gemini...", job.ID)

		var karaokeOut *ai.ConsensusOutput
		var kErr error
		const maxGeminiStageRetries = 4
		for gTry := 1; gTry <= maxGeminiStageRetries; gTry++ {
			karaokeOut, kErr = qp.karaoke.MergeSegmentsWithReference(ctx, acousticJSON, track.Artist, track.Title, refPlain)
			if kErr == nil {
				break
			}
			errLower := strings.ToLower(kErr.Error())
			isOverloaded := strings.Contains(errLower, "503") ||
				strings.Contains(errLower, "high demand") ||
				strings.Contains(errLower, "unavailable") ||
				strings.Contains(errLower, "timeout") ||
				strings.Contains(errLower, "deadline exceeded")
			if isOverloaded && gTry < maxGeminiStageRetries {
				waitSec := gTry * 6
				qp.reportStep(job.ID, "gemini_retry",
					fmt.Sprintf("⏳ Сервер Gemini перегружен (503 High Demand). Пауза %d сек и повтор ТОЛЬКО этапа 3 (попытка %d/%d без сброса задачи)...",
						waitSec, gTry+1, maxGeminiStageRetries))
				select {
				case <-time.After(time.Duration(waitSec) * time.Second):
					continue
				case <-ctx.Done():
					return ctx.Err()
				}
			}
			break
		}
		if kErr != nil {
			return kErr
		}

		qp.reportStep(job.ID, "saving", "Этап 4: Запись синхронизированного .lrc файла на диск (Источник: poisq)...")
		if cErr := qp.cacheRepo.Upsert(db.ProviderCacheRecord{
			TrackID:   track.ID,
			Provider:  "poisq",
			Kind:      "lrc",
			Score:     100.0,
			RawLyrics: karaokeOut.CleanedLyrics,
		}); cErr != nil {
			logger.Warn("[WORKER] [Задача #%d] Не удалось записать provider_cache: %v", job.ID, cErr)
		}
		langOut := karaokeOut.LangCode
		if langOut == "" {
			langOut = track.LangCode
		}
		return qp.saveWinningLyrics(track, "poisq", lyrics.KindLRC, karaokeOut.CleanedLyrics, langOut, true)
	}

	if refPlain != "" {
		return nil
	}
	return lyrics.ErrNotFound
}

func (qp *QueueProcessor) saveWinningLyrics(track *db.TrackRecord, source string, kind lyrics.LyricsKind, content string, langCode string, isVerified bool) error {
	trimmed := strings.TrimSpace(content)
	if trimmed == "" {
		return nil
	}

	status := "plain"
	if isVerified {
		status = "verified"
	} else if kind == lyrics.KindLRC || len(lyrics.ParseLRC(trimmed)) > 0 {
		status = "synced"
	} else if kind == lyrics.KindInstrumental {
		status = "instrumental"
	}

	lrcWritten := false
	if qp.lrcWriter != nil && track.FilePath != "" {
		if _, err := qp.lrcWriter.WriteForAudioFile(track.FilePath, trimmed); err == nil {
			lrcWritten = true
			logger.Info("[WORKER] Файл .lrc успешно записан на диск рядом с %s", track.FilePath)
		} else {
			logger.Warn("[WORKER] Не удалось записать .lrc файл для %s: %v", track.FilePath, err)
		}
	}

	logger.Info("[WORKER] Текст для «%s — %s» сохранен в базу (статус: %s, источник: %s, lrc_file: %v)",
		track.Artist, track.Title, status, source, lrcWritten)
	return qp.trackRepo.UpdateLyrics(track.ID, status, source, trimmed, langCode, lrcWritten)
}

func selectBestText(lyr *lyrics.Lyrics) string {
	if lyr == nil {
		return ""
	}
	if strings.TrimSpace(lyr.SyncedLyrics) != "" {
		return lyr.SyncedLyrics
	}
	return lyr.PlainLyrics
}
