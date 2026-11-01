// Package lyrics предоставляет систему поиска, кэширования и форматирования текстов песен (Lyrics)
// с акцентом на русскоязычные источники, поддержкой LRCLIB/lrcmux протоколов и Subsonic API.
//
// Данный файл engine_parallel.go реализует высокопроизводительный параллельный опрос
// провайдеров текстов песен с ранним возвратом синхронизированного LRC и выбором лучшего plain-кандидата.
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
// При получении синхронизированного текста (LRC) или инструментального трека возвращает результат мгновенно,
// отменяя остальные параллельные запросы для экономии ресурсов.
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

	var bestPlain *Lyrics
	var bestPlainIndex = 999999
	var plainTimer *time.Timer
	var plainTimeoutCh <-chan time.Time

	// Grace-период: если найден обычный текст (plain), даем еще немного времени другим источникам найти синхронизированный (LRC)
	plainGracePeriod := 700 * time.Millisecond

	for {
		select {
		case <-ctx.Done():
			return nil, ctx.Err()

		case <-plainTimeoutCh:
			// Истек grace-период ожидания LRC — отдаем лучший найденный обычный текст
			if bestPlain != nil {
				log.Printf("[INFO] [LYRICS] Использован plain текст из %s (LRC не найден в grace-период)", bestPlain.Source)
				return bestPlain, nil
			}

		case r, ok := <-resCh:
			if !ok {
				// Все провайдеры завершили работу
				if bestPlain != nil {
					log.Printf("[INFO] [LYRICS] Найден plain текст в %s для %q - %q", bestPlain.Source, q.Artist, q.Title)
					return bestPlain, nil
				}
				return nil, ErrNotFound
			}

			if r.err == nil && r.lyrics != nil {
				// 1. Синхронизированный LRC или инструментальный трек побеждает немедленно!
				if r.lyrics.Instrumental || r.lyrics.Kind == KindLRC {
					log.Printf("[INFO] [LYRICS] Найден синхронизированный текст в %s за %v для %q - %q",
						r.prov.Name(), r.dur.Round(time.Millisecond), q.Artist, q.Title)
					cancelAll() // Останавливаем оставшиеся медленные источники
					return r.lyrics, nil
				}

				// 2. Обычный текст (plain): сохраняем по приоритету (меньший индекс в списке провайдеров)
				if r.lyrics.Kind == KindPlain && r.index < bestPlainIndex {
					bestPlain = r.lyrics
					bestPlainIndex = r.index
					log.Printf("[DEBUG] [LYRICS] Получен plain кандидат из %s за %v", r.prov.Name(), r.dur.Round(time.Millisecond))

					// Запускаем таймер grace-периода для поиска LRC, если еще не запущен
					if plainTimer == nil {
						plainTimer = time.NewTimer(plainGracePeriod)
						plainTimeoutCh = plainTimer.C
					}
				}
			}
		}
	}
}
