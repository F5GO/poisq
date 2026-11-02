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
	"log"
	"strings"
	"sync"
	"time"

	"github.com/F5GO/poisq/internal/ai"
	"github.com/F5GO/poisq/internal/db"
	"github.com/F5GO/poisq/internal/keypool"
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
			log.Printf("[WARN] [WORKER] Ошибка выборки задачи из очереди: %v", err)
			return
		}

		if err := qp.executeJob(ctx, job); err != nil {
			if errors.Is(err, db.ErrNoAvailableKeys) {
				retryAt := qp.keyPool.NextRetryTimestamp("gemini")
				groqRetry := qp.keyPool.NextRetryTimestamp("groq")
				if groqRetry > retryAt {
					retryAt = groqRetry
				}
				_ = qp.jobRepo.MarkWaitingQuota(job.ID, retryAt, "Ожидание освобождения API-ключей: "+err.Error())
				log.Printf("[INFO] [WORKER] Все ключи в кулдауне/исчерпаны. Задача #%d переведена в ожидание до %v",
					job.ID, time.Unix(retryAt, 0).Format(time.RFC3339))
				return
			}

			_ = qp.jobRepo.MarkRetryOrFailed(job.ID, job.AttemptCount, 3, err.Error())
			log.Printf("[WARN] [WORKER] Задача #%d (трек %s) завершилась ошибкой (попытка %d): %v",
				job.ID, job.TrackID, job.AttemptCount, err)
			continue
		}

		_ = qp.jobRepo.MarkDone(job.ID)
	}
}

// ExecuteSingleTrack выполняет полный или специализированный пайплайн для одного трека.
func (qp *QueueProcessor) executeJob(ctx context.Context, job *db.LyricsJobRecord) error {
	track, err := qp.trackRepo.GetByID(job.TrackID)
	if err != nil {
		return err
	}

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
			return lyrics.ErrNotFound
		}
		return qp.saveWinningLyrics(track, tRes.Winner.ProviderID, tRes.Winner.Lyrics.Kind,
			selectBestText(tRes.Winner.Lyrics), track.LangCode, false)
	}

	// Шаг 2: Если найден синхронизированный LRC — проводим мультиязычную очистку и AI-консенсус через Gemini
	if tRes != nil && tRes.Winner != nil && tRes.Winner.Lyrics.Kind == lyrics.KindLRC {
		cleanedLocal := ai.FastCleanStructuralTags(tRes.Winner.Lyrics.SyncedLyrics)
		// Сохраняем промежуточный быстрый синхрон, чтобы трек сразу имел LRC
		_ = qp.saveWinningLyrics(track, tRes.Winner.ProviderID, lyrics.KindLRC, cleanedLocal, track.LangCode, false)

		if qp.cleaner != nil {
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
		} else {
			log.Printf("[WARN] [WORKER] Не удалось записать .lrc файл для %s: %v", track.FilePath, err)
		}
	}

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
