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
	"strings"
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
	Consensus  *ConsensusInfo        `json:"consensus,omitempty"`
}

type guardedProvider struct {
	prov    Provider
	limiter *httpclient.ProviderLimiter
	breaker *httpclient.CircuitBreaker
}

// QueryExpander подсказывает дополнительные варианты запроса (например, через LLM).
// Реализация живёт в пакете ai; результаты используются только как поисковые запросы.
type QueryExpander interface {
	ExpandQuery(ctx context.Context, q SearchQuery) ([]SearchQuery, error)
}

// TournamentOrchestrator управляет конкурентным опросом 5 баз текстов песен.
type TournamentOrchestrator struct {
	providers []*guardedProvider
	yandex    *YandexProvider
	cacheRepo *db.ProviderCacheRepository
	keyPool   *keypool.Manager
	timeout   time.Duration
	tolerance time.Duration
	expander  QueryExpander
}

// SetQueryExpander подключает ИИ-расширение запросов (используется как последняя фаза умного поиска).
func (t *TournamentOrchestrator) SetQueryExpander(e QueryExpander) {
	t.expander = e
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

	// Анонимный desktop-API Musixmatch (apic-desktop) с сентября 2026 отдаёт на любой запрос один и тот же
	// «ложный» трек (Drake — NOKIA), поэтому по умолчанию источник отключён и не тратит секунды турнира.
	// Включить обратно (например, если появится рабочий токен/хост): LYRICS_MUSIXMATCH=true.
	if strings.EqualFold(os.Getenv("LYRICS_MUSIXMATCH"), "true") {
		providers = append(providers, &guardedProvider{
			prov:    musixmatchProv,
			limiter: httpclient.NewProviderLimiter(2, 350*time.Millisecond),
			breaker: httpclient.NewCircuitBreaker(3, 3*time.Minute),
		})
		logger.Info("[TOURNAMENT] Musixmatch включён (LYRICS_MUSIXMATCH=true)")
	} else {
		logger.Info("[TOURNAMENT] Musixmatch отключён по умолчанию (desktop-API отдаёт подставной трек); включить: LYRICS_MUSIXMATCH=true")
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

// RunTournament опрашивает все источники и выбирает победителя «по большинству».
//
// Фаза 1 — исходный запрос. Если в ней не нашёлся полноценный LRC, подтверждённый согласием источников,
// включается «умный поиск»: запрос повторяется в вариантах написания (транслитерация и псевдонимы артиста,
// название без пометок изданий, первый из нескольких исполнителей). Все кандидаты из всех фаз
// сверяются между собой (см. consensus.go); LRC берётся только из кластера согласных друг с другом текстов.
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

	candidates := t.runPhase(ctx, q, q, "основной запрос")
	winner, info := t.choose(candidates, q)

	if winner == nil || !(info != nil && info.ConsensusOK && info.HasFullLRC) {
		variants := GenerateQueryVariants(q, 4)
		if len(variants) > 0 {
			logger.Info("[TOURNAMENT] Умный поиск: нет подтверждённого полноценного LRC, пробую %d вариантов запроса", len(variants))
		}
		for i, vq := range variants {
			if ctx.Err() != nil {
				break
			}
			label := fmt.Sprintf("вариант %d/%d: «%s — %s»", i+1, len(variants), vq.Artist, vq.Title)
			more := t.runPhase(ctx, vq, q, label)
			candidates = mergeCandidates(candidates, more)

			winner, info = t.choose(candidates, q)
			if winner != nil && info != nil && info.ConsensusOK && info.HasFullLRC {
				logger.Info("[TOURNAMENT] Умный поиск: подтверждённый LRC найден на варианте %d/%d", i+1, len(variants))
				break
			}
		}
	}

	// Фаза ИИ: детерминированные варианты не помогли — спрашиваем LLM, как трек может быть записан в базах
	if t.expander != nil && ctx.Err() == nil && !(winner != nil && info != nil && info.ConsensusOK && info.HasFullLRC) {
		aiCtx, cancel := context.WithTimeout(ctx, 12*time.Second)
		aiVariants, aiErr := t.expander.ExpandQuery(aiCtx, q)
		cancel()
		if aiErr != nil {
			logger.Warn("[TOURNAMENT] ИИ-подсказки вариантов недоступны: %v", aiErr)
		} else {
			tried := map[string]struct{}{strings.ToLower(q.Artist) + "|" + strings.ToLower(q.Title): {}}
			for _, v := range GenerateQueryVariants(q, 4) {
				tried[strings.ToLower(v.Artist)+"|"+strings.ToLower(v.Title)] = struct{}{}
			}
			n := 0
			for _, vq := range aiVariants {
				k := strings.ToLower(vq.Artist) + "|" + strings.ToLower(vq.Title)
				if _, ok := tried[k]; ok {
					continue
				}
				tried[k] = struct{}{}
				n++
				if n > 3 || ctx.Err() != nil {
					break
				}
				label := fmt.Sprintf("ИИ-вариант %d: «%s — %s»", n, vq.Artist, vq.Title)
				more := t.runPhase(ctx, vq, q, label)
				candidates = mergeCandidates(candidates, more)
				winner, info = t.choose(candidates, q)
				if winner != nil && info != nil && info.ConsensusOK && info.HasFullLRC {
					logger.Info("[TOURNAMENT] ИИ-поиск: подтверждённый LRC найден на варианте %d", n)
					break
				}
			}
		}
	}

	if len(candidates) == 0 || winner == nil {
		logger.Warn("[TOURNAMENT] Ни один из провайдеров не вернул текст для «%s — %s»", q.Artist, q.Title)
		return nil, ErrNotFound
	}

	if info != nil {
		logger.Info("[TOURNAMENT] Консенсус: %s", info.Reason)
		if len(info.Dissenters) > 0 {
			logger.Info("[TOURNAMENT] Не согласны с большинством: %s", strings.Join(info.Dissenters, ", "))
		}
	}

	t.persistCandidates(q, candidates, winner.ProviderID)

	logger.Info("[TOURNAMENT] Победитель турнира для «%s — %s»: [%s] (формат: %s, итоговый балл: %.1f)",
		q.Artist, q.Title, winner.ProviderID, winner.Lyrics.Kind, winner.Score.Total)
	return &TournamentResult{
		Winner:     winner,
		Candidates: candidates,
		Consensus:  info,
	}, nil
}

// choose выбирает победителя: сначала по большинству, при отсутствии согласия — прежним правилом
// (синхронизированный текст приоритетнее, среди равных побеждает больший балл).
func (t *TournamentOrchestrator) choose(candidates []TournamentCandidate, q SearchQuery) (*TournamentCandidate, *ConsensusInfo) {
	if len(candidates) == 0 {
		return nil, nil
	}
	if w, info := PickConsensusWinner(candidates, q); w != nil {
		return w, info
	} else {
		legacy := legacyWinner(candidates)
		if info != nil && legacy != nil {
			info.Reason = info.Reason + "; выбран лучший по баллу (" + legacy.ProviderID + ")"
		}
		return legacy, info
	}
}

func legacyWinner(candidates []TournamentCandidate) *TournamentCandidate {
	if len(candidates) == 0 {
		return nil
	}
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
	w := candidates[bestIdx]
	return &w
}

// mergeCandidates добавляет новые кандидаты, отбрасывая точные дубликаты (тот же источник и тот же текст).
func mergeCandidates(base, extra []TournamentCandidate) []TournamentCandidate {
	key := func(c TournamentCandidate) string {
		toks := textTokens(c.Lyrics)
		h := len(toks)
		if len(toks) > 0 {
			h = h*31 + len(toks[0]) + len(toks[len(toks)-1])*7
		}
		return fmt.Sprintf("%s|%s|%d|%d", c.ProviderID, c.Lyrics.Kind, h, len(c.Lyrics.SyncedLyrics))
	}
	seen := map[string]struct{}{}
	for _, c := range base {
		seen[key(c)] = struct{}{}
	}
	for _, c := range extra {
		k := key(c)
		if _, ok := seen[k]; ok {
			continue
		}
		seen[k] = struct{}{}
		base = append(base, c)
	}
	return base
}

// persistCandidates сохраняет в provider_cache по одному лучшему ответу каждого источника (победителя — приоритетно),
// чтобы в UI можно было переключаться между вариантами без повторных сетевых вызовов.
func (t *TournamentOrchestrator) persistCandidates(q SearchQuery, candidates []TournamentCandidate, winnerProvider string) {
	if t.cacheRepo == nil || q.TrackID == "" {
		return
	}
	best := map[string]TournamentCandidate{}
	for _, c := range candidates {
		cur, ok := best[c.ProviderID]
		if !ok || (isCandidateSynced(c) && !isCandidateSynced(cur)) ||
			(isCandidateSynced(c) == isCandidateSynced(cur) && c.Score.Total > cur.Score.Total) {
			best[c.ProviderID] = c
		}
	}
	for id, c := range best {
		content := c.Lyrics.SyncedLyrics
		if content == "" {
			content = c.Lyrics.PlainLyrics
		}
		if content == "" && !c.Lyrics.Instrumental {
			continue
		}
		metaBytes, _ := json.Marshal(map[string]any{
			"artist":     c.Lyrics.Artist,
			"title":      c.Lyrics.Title,
			"album":      c.Lyrics.Album,
			"durationMs": c.Lyrics.Duration.Milliseconds(),
			"isCensored": c.Score.IsCensored,
			"isWinner":   id == winnerProvider,
		})
		if upErr := t.cacheRepo.Upsert(db.ProviderCacheRecord{
			TrackID:      q.TrackID,
			Provider:     id,
			Kind:         string(c.Lyrics.Kind),
			Score:        c.Score.Total,
			RawLyrics:    content,
			MetadataJSON: string(metaBytes),
		}); upErr != nil {
			logger.Warn("[TOURNAMENT] [%s] Не удалось сохранить ответ в provider_cache: %v", id, upErr)
		}
	}
}

// runPhase параллельно опрашивает все доступные источники запросом fetchQ.
// Оценка кандидатов (ScoreCandidate) всегда выполняется относительно исходного запроса scoreQ.
func (t *TournamentOrchestrator) runPhase(ctx context.Context, fetchQ, scoreQ SearchQuery, label string) []TournamentCandidate {
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
			lyr, err := g.prov.Fetch(pCtx, fetchQ)
			elapsed := time.Since(started).Round(time.Millisecond)
			if err != nil {
				switch {
				case errors.Is(err, ErrNotFound):
					logger.Info("[TOURNAMENT] [%s] Не найдено (%s) — %s", g.prov.ID(), elapsed, label)
				case errors.Is(err, context.Canceled):
					logger.Info("[TOURNAMENT] [%s] Запрос отменен (%s)", g.prov.ID(), elapsed)
				default:
					reason := err.Error()
					if errors.Is(err, context.DeadlineExceeded) {
						reason = fmt.Sprintf("таймаут %s: %v", t.timeout, err)
					}
					logger.Warn("[TOURNAMENT] [%s] ОШИБКА провайдера через %s: %s — %s", g.prov.ID(), elapsed, reason, label)
					if g.breaker.RecordFailure() {
						logger.Warn("[TOURNAMENT] [%s] Circuit Breaker РАЗОМКНУТ: источник отключен на %s", g.prov.ID(), g.breaker.OpenFor().Round(time.Second))
					}
				}
				return
			}
			if lyr == nil {
				logger.Info("[TOURNAMENT] [%s] Пустой ответ (%s) — %s", g.prov.ID(), elapsed, label)
				return
			}
			g.breaker.RecordSuccess()

			content := lyr.SyncedLyrics
			if content == "" {
				content = lyr.PlainLyrics
			}
			if content == "" && !lyr.Instrumental {
				logger.Info("[TOURNAMENT] [%s] Ответ без текста (%s) — %s", g.prov.ID(), elapsed, label)
				return
			}

			score := ScoreCandidate(scoreQ, lyr.Artist, lyr.Title, lyr.Album, lyr.Duration, lyr.Kind, content, t.tolerance)
			if !score.Accepted {
				logger.Info("[TOURNAMENT] [%s] Кандидат отклонен фильтром (%s): «%s — %s», альбом %q, длительность %v; артист=%.1f название=%.1f длительность=%.1f — %s",
					g.prov.ID(), elapsed, lyr.Artist, lyr.Title, lyr.Album, lyr.Duration,
					score.ArtistScore, score.TitleScore, score.DurationScore, label)
				return
			}

			logger.Info("[TOURNAMENT] [%s] Найден подходящий текст за %s! Формат: %s, оценка: %.1f из 100 (артист=%.1f название=%.1f длительность=%.1f), строк: %d — %s",
				g.prov.ID(), elapsed, lyr.Kind, score.Total, score.ArtistScore, score.TitleScore, score.DurationScore, len(lyr.Lines), label)

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
	return candidates
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
