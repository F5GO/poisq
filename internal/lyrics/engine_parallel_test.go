// Package lyrics предоставляет систему поиска, кэширования и форматирования текстов песен (Lyrics)
// с акцентом на русскоязычные источники, поддержкой LRCLIB/lrcmux протоколов и Subsonic API.
//
// Данный файл engine_parallel_test.go тестирует работу параллельного диспетчера источников:
// ранний возврат синхронизированного LRC, обработку grace-периода и отмену медленных запросов.
package lyrics

import (
	"context"
	"testing"
	"time"
)

type mockDelayProvider struct {
	id     string
	name   string
	delay  time.Duration
	lyrics *Lyrics
	err    error
}

func (m *mockDelayProvider) ID() string   { return m.id }
func (m *mockDelayProvider) Name() string { return m.name }
func (m *mockDelayProvider) Fetch(ctx context.Context, q SearchQuery) (*Lyrics, error) {
	select {
	case <-time.After(m.delay):
		return m.lyrics, m.err
	case <-ctx.Done():
		return nil, ctx.Err()
	}
}

func TestEngine_Parallel_FastLRCWins(t *testing.T) {
	cfg := &Config{
		Enabled:               true,
		RequestTimeout:        2 * time.Second,
		MaxConcurrentRequests: 4,
	}
	engine := &Engine{
		cfg:    cfg,
		cache:  NewCache(nil, time.Hour, 100),
		flight: NewSingleFlightGroup(),
		sem:    make(chan struct{}, 4),
	}

	// 1. Медленный plain провайдер (500мс)
	slowPlain := &mockDelayProvider{
		id:    "slow_plain",
		name:  "Slow Plain Source",
		delay: 500 * time.Millisecond,
		lyrics: &Lyrics{
			Kind:        KindPlain,
			PlainLyrics: "Текст обычной песни",
			Source:      "slow_plain",
		},
	}

	// 2. Быстрый LRC провайдер (50мс)
	fastLRC := &mockDelayProvider{
		id:    "fast_lrc",
		name:  "Fast LRC Source",
		delay: 50 * time.Millisecond,
		lyrics: &Lyrics{
			Kind:         KindLRC,
			PlainLyrics:  "Текст синхронизированной песни",
			SyncedLyrics: "[00:10.00] Текст синхронизированной песни",
			Source:       "fast_lrc",
		},
	}

	engine.providers = []Provider{slowPlain, fastLRC}

	start := time.Now()
	res, err := engine.queryProvidersParallel(context.Background(), SearchQuery{
		Artist: "Тестовый Артист",
		Title:  "Тестовый Трек",
	})
	dur := time.Since(start)

	if err != nil {
		t.Fatalf("Неожиданная ошибка: %v", err)
	}

	if res == nil || res.Kind != KindLRC || res.Source != "fast_lrc" {
		t.Fatalf("Ожидался быстрый LRC результат от fast_lrc, получено: %+v", res)
	}

	// Быстрый LRC должен вернуться намного быстрее, чем 500мс (медленный источник)
	if dur > 300*time.Millisecond {
		t.Errorf("Параллельный опрос занял слишком много времени: %v (ожидалось < 300ms)", dur)
	}
}

func TestEngine_Parallel_PlainGracePeriod(t *testing.T) {
	cfg := &Config{
		Enabled:               true,
		RequestTimeout:        2 * time.Second,
		MaxConcurrentRequests: 4,
	}
	engine := &Engine{
		cfg:    cfg,
		cache:  NewCache(nil, time.Hour, 100),
		flight: NewSingleFlightGroup(),
		sem:    make(chan struct{}, 4),
	}

	// Быстрый plain (30мс)
	fastPlain := &mockDelayProvider{
		id:    "fast_plain",
		name:  "Fast Plain Source",
		delay: 30 * time.Millisecond,
		lyrics: &Lyrics{
			Kind:        KindPlain,
			PlainLyrics: "Быстрый обычный текст",
			Source:      "fast_plain",
		},
	}

	// Медленный LRC (150мс)
	slowLRC := &mockDelayProvider{
		id:    "slow_lrc",
		name:  "Slow LRC Source",
		delay: 150 * time.Millisecond,
		lyrics: &Lyrics{
			Kind:         KindLRC,
			PlainLyrics:  "Синхронизированный текст",
			SyncedLyrics: "[00:05.00] Синхронизированный текст",
			Source:       "slow_lrc",
		},
	}

	engine.providers = []Provider{fastPlain, slowLRC}

	res, err := engine.queryProvidersParallel(context.Background(), SearchQuery{
		Artist: "Тест",
		Title:  "Трек",
	})

	if err != nil {
		t.Fatalf("Неожиданная ошибка: %v", err)
	}

	// В grace-периоде должен победить LRC, так как он пришел раньше истечения grace-таймера
	if res == nil || res.Kind != KindLRC || res.Source != "slow_lrc" {
		t.Fatalf("Ожидался переход на LRC во время grace-периода, получено: %+v", res)
	}
}
