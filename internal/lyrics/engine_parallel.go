// Package lyrics предоставляет систему поиска, кэширования и форматирования текстов песен (Lyrics)
// с акцентом на русскоязычные источники, поддержкой LRCLIB/lrcmux протоколов и Subsonic API.
//
// Данный файл engine_parallel.go реализует высокопроизводительный параллельный опрос
// провайдеров текстов песен с ранним возвратом синхронизированного LRC и выбором лучшего кандидата по шкале качества.
package lyrics

import (
	"context"
	"log"
	"sync"
	"time"
)

type providerResult struct {
	index  int
	prov   Provider
	lyrics *Lyrics
	err    error
	dur    time.Duration
}

// queryProvidersParallel выполняет конкурентный опрос всех настроенных источников текстов.
// При получении чистого синхронизированного текста (LRC) высокого качества или инструментального трека
// возвращает результат мгновенно, отменяя остальные параллельные запросы для экономии ресурсов.
func (e *Engine) queryProvidersParallel(ctx context.Context, q SearchQuery) (*Lyrics, error) {
	// Ограничиваем количество одновременных поисков треков через глобальный семафор
	select {
	case e.sem <- struct{}{}:
		defer func() { <-e.sem }()
	case <-ctx.Done():
		return nil, ctx.Err()
	}

	if len(e.providers) == 0 {
		return nil, ErrNotFound
	}

	// Если настроен только 1 провайдер — выполняем прямой запрос без накладных расходов на горутины
	if len(e.providers) == 1 {
		p := e.providers[0]
		reqCtx, cancel := context.WithTimeout(ctx, e.cfg.RequestTimeout)
		defer cancel()
		res, err := p.Fetch(reqCtx, q)
		if err == nil && res != nil {
			log.Printf("[INFO] [LYRICS] Источник %s нашел %s текст для %q - %q", p.Name(), res.Kind, q.Artist, q.Title)
			return res, nil
		}
		return nil, ErrNotFound
	}

	subCtx, cancelAll := context.WithCancel(ctx)
	defer cancelAll()

	resCh := make(chan providerResult, len(e.providers))
	var wg sync.WaitGroup

	for i, p := range e.providers {
		wg.Add(1)
		go func(idx int, prov Provider) {
			defer wg.Done()
			pStart := time.Now()

			pCtx, pCancel := context.WithTimeout(subCtx, e.cfg.RequestTimeout)
			defer pCancel()

			res, err := prov.Fetch(pCtx, q)
			dur := time.Since(pStart)

			if err != nil && err != ErrNotFound && err != context.Canceled {
				log.Printf("[DEBUG] [LYRICS] Провайдер %s вернул ошибку за %v: %v", prov.Name(), dur.Round(time.Millisecond), err)
			}

			resCh <- providerResult{
				index:  idx,
				prov:   prov,
				lyrics: res,
				err:    err,
				dur:    dur,
			}
		}(i, p)
	}

	// Закрываем канал результатов, когда все горутины завершатся
	go func() {
		wg.Wait()
		close(resCh)
	}()

	var bestCandidate *Lyrics
	var bestScore CandidateScore
	var bestIndex = 999999
	var plainTimer *time.Timer
	var plainTimeoutCh <-chan time.Time

	// Grace-период: если найден обычный или цензурированный текст, даем еще немного времени
	// другим источникам найти чистый синхронизированный (LRC)
	plainGracePeriod := 700 * time.Millisecond

	for {
		select {
		case <-ctx.Done():
			return nil, ctx.Err()

		case <-plainTimeoutCh:
			// Истек grace-период ожидания лучшего кандидата — отдаем наилучший найденный
			if bestCandidate != nil {
				log.Printf("[INFO] [LYRICS] Использован текст из %s (балл: %.1f, grace-период завершен)",
					bestCandidate.Source, bestScore.Total)
				return bestCandidate, nil
			}

		case r, ok := <-resCh:
			if !ok {
				// Все провайдеры завершили работу
				if bestCandidate != nil {
					log.Printf("[INFO] [LYRICS] Найден текст в %s для %q - %q (балл: %.1f)",
						bestCandidate.Source, q.Artist, q.Title, bestScore.Total)
					return bestCandidate, nil
				}
				return nil, ErrNotFound
			}

			if r.err == nil && r.lyrics != nil {
				// Проверяем на дефектный текст (обрубок со 2-го куплета или заглушка)
				if !r.lyrics.Instrumental && IsDefectiveLyrics(r.lyrics.PlainLyrics) {
					log.Printf("[DEBUG] [LYRICS] Текст от %s отклонен как дефектный (неполный или заглушка)", r.prov.Name())
					continue
				}

				candArtist := r.lyrics.Artist
				if candArtist == "" {
					candArtist = q.Artist
				}
				candTitle := r.lyrics.Title
				if candTitle == "" {
					candTitle = q.Title
				}
				candAlbum := r.lyrics.Album
				if candAlbum == "" {
					candAlbum = q.Album
				}
				candDuration := r.lyrics.Duration
				if candDuration <= 0 {
					candDuration = q.Duration
				}

				score := ScoreCandidate(
					q,
					candArtist,
					candTitle,
					candAlbum,
					candDuration,
					r.lyrics.Kind,
					r.lyrics.PlainLyrics,
					e.cfg.DurationTolerance,
				)

				// Если кандидат отсеян валидатором (конфликт версий, несоответствие длительности) — пропускаем
				if !score.Accepted {
					log.Printf("[DEBUG] [LYRICS] Кандидат от %s отклонен валидатором качества (балл: %.1f)",
						r.prov.Name(), score.Total)
					continue
				}

				// 1. Инструментальный трек или чистый синхронизированный LRC высокого качества побеждает мгновенно!
				if r.lyrics.Instrumental || (r.lyrics.Kind == KindLRC && !score.IsCensored && score.Total >= 85.0) {
					log.Printf("[INFO] [LYRICS] Найден синхронизированный текст в %s за %v (балл: %.1f) для %q - %q",
						r.prov.Name(), r.dur.Round(time.Millisecond), score.Total, q.Artist, q.Title)
					cancelAll() // Останавливаем оставшиеся медленные источники
					return r.lyrics, nil
				}

				// 2. Обычный текст (plain) или цензурированный LRC: сохраняем по баллу и приоритету
				if bestCandidate == nil || score.Total > bestScore.Total || (score.Total == bestScore.Total && r.index < bestIndex) {
					bestCandidate = r.lyrics
					bestScore = score
					bestIndex = r.index

					log.Printf("[DEBUG] [LYRICS] Получен кандидат (%s) из %s за %v (балл: %.1f)",
						r.lyrics.Kind, r.prov.Name(), r.dur.Round(time.Millisecond), score.Total)

					// Запускаем таймер grace-периода для поиска лучшего LRC, если еще не запущен
					if plainTimer == nil {
						plainTimer = time.NewTimer(plainGracePeriod)
						plainTimeoutCh = plainTimer.C
					}
				}
			}
		}
	}
}
