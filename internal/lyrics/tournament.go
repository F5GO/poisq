// Package lyrics предоставляет систему поиска, ранжирования и сохранения текстов песен (Lyrics)
// по 5 мировым и региональным источникам (LRCLIB, Яндекс Музыка, Musixmatch, NetEase, Genius).
//
// Данный файл tournament.go реализует параллельный каскад опроса 5 провайдеров (Fan-Out / LRC Tournament)
// с защитой каждого источника через Circuit Breaker (отключение на 3 минуты при сбоях),
// индивидуальный Rate Limiter и сохранением всех сырых ответов в таблицу provider_cache.
package lyrics

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"os"
	"sync"
	"time"

	"github.com/F5GO/poisq/internal/db"
	"github.com/F5GO/poisq/internal/httpclient"
	"github.com/F5GO/poisq/internal/keypool"
	"github.com/F5GO/poisq/internal/logger"
)

// TournamentCandidate описывает найденный вариант текста от одного провайдера вместе с его оценкой.
type TournamentCandidate struct {
	ProviderID string         `json:"providerId"`
	Lyrics     *Lyrics        `json:"lyrics"`
	Score      CandidateScore `json:"score"`
}

// TournamentResult объединяет победителя турнира и полный список всех найденных вариантов.
type TournamentResult struct {
	Winner     *TournamentCandidate  `json:"winner"`
	Candidates []TournamentCandidate `json:"candidates"`
}

type guardedProvider struct {
	prov    Provider
	limiter *httpclient.ProviderLimiter
	breaker *httpclient.CircuitBreaker
}

// TournamentOrchestrator управляет конкурентным опросом 5 баз текстов песен.
type TournamentOrchestrator struct {
	providers []*guardedProvider
	yandex    *YandexProvider
	cacheRepo *db.ProviderCacheRepository
	keyPool   *keypool.Manager
	timeout   time.Duration
	tolerance time.Duration
}

// NewTournamentOrchestrator инициализирует 5 провайдеров (LRCLIB, Яндекс Музыка, Musixmatch, NetEase, Genius)
// с индивидуальными ограничителями скорости и автоматическими выключателями (Circuit Breaker на 3 минуты).
func NewTournamentOrchestrator(cacheRepo *db.ProviderCacheRepository, keyPool *keypool.Manager) *TournamentOrchestrator {
	timeout := 6 * time.Second
	tolerance := 6 * time.Second

	envYandexToken := os.Getenv("LYRICS_YANDEX_TOKEN")
	if envYandexToken == "" {
		envYandexToken = os.Getenv("YANDEX_MUSIC_TOKEN")
	}

	lrclibProv := NewLrclibProvider(timeout, tolerance)
	yandexProv := NewYandexProvider(timeout, tolerance, envYandexToken)
	musixmatchProv := NewMusixmatchProvider(timeout)
	neteaseProv := NewNetEaseProvider(timeout, tolerance)
	geniusProv := NewGeniusProvider(timeout)

	providers := []*guardedProvider{
		{
			prov:    lrclibProv,
			limiter: httpclient.NewProviderLimiter(4, 150*time.Millisecond),
			breaker: httpclient.NewCircuitBreaker(4, 3*time.Minute),
		},
		{
			prov:    yandexProv,
			limiter: httpclient.NewProviderLimiter(3, 250*time.Millisecond),
			breaker: httpclient.NewCircuitBreaker(4, 3*time.Minute),
		},
		{
			prov:    musixmatchProv,
			limiter: httpclient.NewProviderLimiter(2, 350*time.Millisecond),
			breaker: httpclient.NewCircuitBreaker(3, 3*time.Minute),
		},
		{
			prov:    neteaseProv,
			limiter: httpclient.NewProviderLimiter(3, 250*time.Millisecond),
			breaker: httpclient.NewCircuitBreaker(3, 3*time.Minute),
		},
		{
			prov:    geniusProv,
			limiter: httpclient.NewProviderLimiter(2, 300*time.Millisecond),
			breaker: httpclient.NewCircuitBreaker(3, 3*time.Minute),
		},
	}

	return &TournamentOrchestrator{
		providers: providers,
		yandex:    yandexProv,
		cacheRepo: cacheRepo,
		keyPool:   keyPool,
		timeout:   timeout,
		tolerance: tolerance,
	}
}

// RunTournament параллельно опрашивает все 5 источников, оценивает ответы по шкале CandidateScore (0..100),
// сохраняет каждый вариант в provider_cache и возвращает лучший результат.
func (t *TournamentOrchestrator) RunTournament(ctx context.Context, q SearchQuery) (*TournamentResult, error) {
	logger.Info("[TOURNAMENT] Старт турнира текстов: «%s — %s» (альбом: %q, хронометраж: %v)", q.Artist, q.Title, q.Album, q.Duration)

	// Если в пуле ключей добавлен OAuth-токен Яндекс Музыки — актуализируем его перед запросом
	if t.keyPool != nil && t.yandex != nil {
		if leased, err := t.keyPool.Acquire("yandex"); err == nil && leased != nil {
			t.yandex.SetToken(leased.PlainKey)
			t.keyPool.ReportSuccess(leased.ID)
		} else if !t.yandex.HasToken() {
			logger.Warn("[TOURNAMENT] [yandex] OAuth-токен Яндекс Музыки не задан (пул ключей и env пусты): синхронные тексты Яндекса недоступны")
		}
	}

	var wg sync.WaitGroup
	var mu sync.Mutex
	var candidates []TournamentCandidate

	for _, gp := range t.providers {
		if !gp.breaker.Allow() {
			logger.Warn("[TOURNAMENT] [%s] Пропущен: Circuit Breaker активен еще ~%s", gp.prov.ID(), gp.breaker.OpenFor().Round(time.Second))
			continue
		}

		wg.Add(1)
		go func(g *guardedProvider) {
			defer wg.Done()

			release, err := g.limiter.Wait(ctx)
			if err != nil {
				logger.Warn("[TOURNAMENT] [%s] Не получен слот rate limiter: %v", g.prov.ID(), err)
				return
			}
			defer release()

			pCtx, cancel := context.WithTimeout(ctx, t.timeout)
			defer cancel()

			started := time.Now()
			lyr, err := g.prov.Fetch(pCtx, q)
			elapsed := time.Since(started).Round(time.Millisecond)
			if err != nil {
				switch {
				case errors.Is(err, ErrNotFound):
					logger.Info("[TOURNAMENT] [%s] Не найдено (%s)", g.prov.ID(), elapsed)
				case errors.Is(err, context.Canceled):
					logger.Info("[TOURNAMENT] [%s] Запрос отменен (%s)", g.prov.ID(), elapsed)
				default:
					reason := err.Error()
					if errors.Is(err, context.DeadlineExceeded) {
						reason = fmt.Sprintf("таймаут %s: %v", t.timeout, err)
					}
					logger.Warn("[TOURNAMENT] [%s] ОШИБКА провайдера через %s: %s", g.prov.ID(), elapsed, reason)
					if g.breaker.RecordFailure() {
						logger.Warn("[TOURNAMENT] [%s] Circuit Breaker РАЗОМКНУТ: источник отключен на %s", g.prov.ID(), g.breaker.OpenFor().Round(time.Second))
					}
				}
				return
			}
			if lyr == nil {
				logger.Info("[TOURNAMENT] [%s] Пустой ответ (%s)", g.prov.ID(), elapsed)
				return
			}
			g.breaker.RecordSuccess()

			content := lyr.SyncedLyrics
			if content == "" {
				content = lyr.PlainLyrics
			}
			if content == "" && !lyr.Instrumental {
				logger.Info("[TOURNAMENT] [%s] Ответ без текста (%s)", g.prov.ID(), elapsed)
				return
			}

			score := ScoreCandidate(q, lyr.Artist, lyr.Title, lyr.Album, lyr.Duration, lyr.Kind, content, t.tolerance)
			if !score.Accepted {
				logger.Info("[TOURNAMENT] [%s] Кандидат отклонен фильтром (%s): «%s — %s», альбом %q, длительность %v; артист=%.1f название=%.1f длительность=%.1f",
					g.prov.ID(), elapsed, lyr.Artist, lyr.Title, lyr.Album, lyr.Duration,
					score.ArtistScore, score.TitleScore, score.DurationScore)
				return
			}

			logger.Info("[TOURNAMENT] [%s] Найден подходящий текст за %s! Формат: %s, оценка: %.1f из 100 (артист=%.1f название=%.1f длительность=%.1f), строк: %d",
				g.prov.ID(), elapsed, lyr.Kind, score.Total, score.ArtistScore, score.TitleScore, score.DurationScore, len(lyr.Lines))

			// Сохраняем сырой ответ провайдера в SQLite (provider_cache) для переключения в UI
			if t.cacheRepo != nil && q.TrackID != "" {
				metaBytes, _ := json.Marshal(map[string]any{
					"artist":     lyr.Artist,
					"title":      lyr.Title,
					"album":      lyr.Album,
					"durationMs": lyr.Duration.Milliseconds(),
					"isCensored": score.IsCensored,
				})
				if upErr := t.cacheRepo.Upsert(db.ProviderCacheRecord{
					TrackID:      q.TrackID,
					Provider:     g.prov.ID(),
					Kind:         string(lyr.Kind),
					Score:        score.Total,
					RawLyrics:    content,
					MetadataJSON: string(metaBytes),
				}); upErr != nil {
					logger.Warn("[TOURNAMENT] [%s] Не удалось сохранить ответ в provider_cache: %v", g.prov.ID(), upErr)
				}
			}

			mu.Lock()
			candidates = append(candidates, TournamentCandidate{
				ProviderID: g.prov.ID(),
				Lyrics:     lyr,
				Score:      score,
			})
			mu.Unlock()
		}(gp)
	}

	wg.Wait()

	if len(candidates) == 0 {
		logger.Warn("[TOURNAMENT] Ни один из провайдеров не вернул текст для «%s — %s»", q.Artist, q.Title)
		return nil, ErrNotFound
	}

	// Выбираем победителя: синхронизированный LRC (KindLRC или валидные таймкоды)
	// всегда имеет абсолютный приоритет перед обычным Plain-текстом (KindPlain).
	// Если оба синхронизированы или оба plain — побеждает кандидат с высшим баллом.
	bestIdx := 0
	for i := 1; i < len(candidates); i++ {
		cur := candidates[i]
		best := candidates[bestIdx]

		curSynced := isCandidateSynced(cur)
		bestSynced := isCandidateSynced(best)

		if curSynced && !bestSynced {
			bestIdx = i
		} else if !curSynced && bestSynced {
			continue
		} else if cur.Score.Total > best.Score.Total {
			bestIdx = i
		}
	}

	winner := candidates[bestIdx]
	logger.Info("[TOURNAMENT] Победитель турнира для «%s — %s»: [%s] (формат: %s, итоговый балл: %.1f)",
		q.Artist, q.Title, winner.ProviderID, winner.Lyrics.Kind, winner.Score.Total)
	return &TournamentResult{
		Winner:     &winner,
		Candidates: candidates,
	}, nil
}

func isCandidateSynced(c TournamentCandidate) bool {
	if c.Lyrics == nil {
		return false
	}
	if c.Lyrics.Kind == KindLRC || c.Lyrics.Kind == KindInstrumental {
		return true
	}
	return len(ParseLRC(c.Lyrics.SyncedLyrics)) > 0
}
