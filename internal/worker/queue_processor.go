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
		wakeCh:     make(chan struct{}, 1),
	}
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

		logger.Info("[WORKER] Взята задача #%d в обработку: тип=%s, трекId=%s", job.ID, job.JobType, job.TrackID)

		if err := qp.executeJob(ctx, job); err != nil {
			if errors.Is(err, db.ErrNoAvailableKeys) {
				if job.JobType == "whisper_karaoke" && (qp.keyPool == nil || !qp.keyPool.HasKeys("groq")) {
					_ = qp.jobRepo.MarkRetryOrFailed(job.ID, 3, 3, "Отсутствует ключ Groq Cloud в пуле. Добавьте его во вкладке «Ключи API».")
					logger.Warn("[WORKER] Задача #%d отклонена: в пуле нет ключа Groq Cloud для AI Whisper караоке", job.ID)
					return
				}
				retryAt := qp.keyPool.NextRetryTimestamp("gemini")
				groqRetry := qp.keyPool.NextRetryTimestamp("groq")
				if groqRetry > retryAt {
					retryAt = groqRetry
				}
				_ = qp.jobRepo.MarkWaitingQuota(job.ID, retryAt, "Ожидание освобождения API-ключей: "+err.Error())
				logger.Info("[WORKER] Ключи в кулдауне. Задача #%d переведена в ожидание до %v",
					job.ID, time.Unix(retryAt, 0).Format(time.RFC3339))
				return
			}

			_ = qp.jobRepo.MarkRetryOrFailed(job.ID, job.AttemptCount, 3, err.Error())
			logger.Warn("[WORKER] Задача #%d (трек %s) завершилась ошибкой (попытка %d): %v",
				job.ID, job.TrackID, job.AttemptCount, err)
			continue
		}

		_ = qp.jobRepo.MarkDone(job.ID)
		logger.Info("[WORKER] Задача #%d успешно завершена!", job.ID)
	}
}

// ExecuteSingleTrack выполняет полный или специализированный пайплайн для одного трека.
func (qp *QueueProcessor) executeJob(ctx context.Context, job *db.LyricsJobRecord) error {
	track, err := qp.trackRepo.GetByID(job.TrackID)
	if err != nil {
		return err
	}

	logger.Info("[WORKER] [Задача #%d] Старт обработки: «%s — %s» (тип задачи: %s)", job.ID, track.Artist, track.Title, job.JobType)

	q := lyrics.SearchQuery{
		TrackID:  track.ID,
		Artist:   track.Artist,
		Title:    track.Title,
		Album:    track.Album,
		Duration: time.Duration(track.Duration) * time.Second,
	}

	// Шаг 1: Запуск параллельного LRC-турнира по 5 источникам
	tRes, err := qp.tournament.RunTournament(ctx, q)
	if err != nil && !errors.Is(err, lyrics.ErrNotFound) {
		return err
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

	// Шаг 2: Если найден синхронизированный LRC — проводим мультиязычную очистку и AI-консенсус через Gemini
	if tRes != nil && tRes.Winner != nil && tRes.Winner.Lyrics.Kind == lyrics.KindLRC {
		cleanedLocal := ai.FastCleanStructuralTags(tRes.Winner.Lyrics.SyncedLyrics)
		// Сохраняем промежуточный быстрый синхрон, чтобы трек сразу имел LRC
		_ = qp.saveWinningLyrics(track, tRes.Winner.ProviderID, lyrics.KindLRC, cleanedLocal, track.LangCode, false)

		if qp.cleaner != nil {
			logger.Info("[WORKER] [Задача #%d] Запуск AI-консенсуса и валидации текста через Gemini...", job.ID)
			consensus, aiErr := qp.cleaner.BuildConsensus(ctx, track.Artist, track.Title, tRes.Candidates)
			if aiErr != nil {
				if errors.Is(aiErr, db.ErrNoAvailableKeys) && job.JobType == "ai_consensus" {
					return aiErr
				}
				// Если ключи Gemini не заданы, оставляем чистый локально очищенный LRC
				return nil
			}
			_ = qp.cacheRepo.Upsert(db.ProviderCacheRecord{
				TrackID:   track.ID,
				Provider:  "gemini_consensus",
				Kind:      "lrc",
				Score:     99.5,
				RawLyrics: consensus.CleanedLyrics,
			})
			return qp.saveWinningLyrics(track, "gemini_consensus", lyrics.KindLRC, consensus.CleanedLyrics, consensus.LangCode, true)
		}
		return nil
	}

	// Шаг 3: Если точных таймкодов нигде нет (только Plain-текст или пусто) -> запускаем облачное караоке Groq Whisper + Gemini!
	var refPlain string
	if tRes != nil && tRes.Winner != nil {
		refPlain = ai.FastCleanStructuralTags(tRes.Winner.Lyrics.PlainLyrics)
		if refPlain != "" {
			_ = qp.saveWinningLyrics(track, tRes.Winner.ProviderID, lyrics.KindPlain, refPlain, track.LangCode, false)
		}
	}

	if qp.karaoke != nil && (job.JobType == "whisper_karaoke" || job.JobType == "full_pipeline") {
		logger.Info("[WORKER] [Задача #%d] Запуск транскрибации караоке через Groq Whisper + Gemini...", job.ID)
		karaokeOut, kErr := qp.karaoke.GenerateSyncedLRC(ctx, track.FilePath, track.Artist, track.Title, refPlain, track.LangCode)
		if kErr != nil {
			return kErr
		}
		_ = qp.cacheRepo.Upsert(db.ProviderCacheRecord{
			TrackID:   track.ID,
			Provider:  "whisper_gemini",
			Kind:      "lrc",
			Score:     100.0,
			RawLyrics: karaokeOut.CleanedLyrics,
		})
		return qp.saveWinningLyrics(track, "whisper_gemini", lyrics.KindLRC, karaokeOut.CleanedLyrics, karaokeOut.LangCode, true)
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
