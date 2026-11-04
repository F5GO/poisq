// Package lyrics предоставляет систему поиска, ранжирования и сохранения текстов песен (Lyrics).
//
// Данный файл discovery.go реализует «поиск реальных написаний» исполнителя. Вместо того чтобы просить нейросеть
// придумывать варианты имени, мы спрашиваем сами базы:
//  1. MusicBrainz знает настоящие алиасы артиста (в том числе стилизованные: «МакSим», «MakSim»);
//  2. базы ищутся по названию БЕЗ артиста, и из результатов берутся записи с тем же названием и длительностью
//     в пределах ±2 сек — имя исполнителя в такой записи и есть то, как он реально записан в этой базе.
//
// Найденные написания проверяются: либо они очевидно похожи на исходное (транслитерация, подстрока),
// либо их подтверждает ИИ-судья (выбирает только из реально найденных записей, ничего не придумывает).
// Все найденные варианты используются как поисковые запросы; принять текст можно лишь через консенсус источников.
package lyrics

import (
	"context"
	"sort"
	"strings"
	"sync"
	"time"

	"github.com/F5GO/poisq/internal/logger"
)

// DiscoveredTrack — реальная запись, найденная в базе по названию.
type DiscoveredTrack struct {
	Provider string
	Artist   string
	Title    string
	Duration time.Duration
}

// Discoverer — провайдер, умеющий искать записи по названию без указания артиста.
type Discoverer interface {
	Discover(ctx context.Context, title string, duration time.Duration) ([]DiscoveredTrack, error)
}

// ArtistAliasSource отдаёт настоящие имена и алиасы исполнителя (реализует MusicBrainz-клиент).
type ArtistAliasSource interface {
	ArtistNames(ctx context.Context, name string) ([]string, error)
}

// ArtistJudge выбирает из реально найденных записей те, что принадлежат тому же исполнителю (реализует ИИ).
// Возвращает индексы принятых записей. Ничего не придумывает: выбор только из переданного списка.
type ArtistJudge interface {
	SameArtist(ctx context.Context, original SearchQuery, candidates []DiscoveredTrack) ([]int, error)
}

// foldArtist приводит имя к виду для грубого сравнения: нижний регистр, смешанные алфавиты, транслитерация.
func foldArtist(s string) string {
	n := NormalizeTrackString(s)
	if hasCyrillic(n) {
		n = strings.ToLower(TransliterateRuToLat(n))
	}
	return strings.Join(strings.Fields(strings.NewReplacer("'", "", "’", "", "`", "", "-", " ", ".", "").Replace(n)), " ")
}

// artistsPlausiblyEqual — быстрая проверка без ИИ: имена совпадают после транслитерации, одно содержит другое
// или отличаются не более чем на ~35% символов.
func artistsPlausiblyEqual(a, b string) bool {
	fa, fb := foldArtist(a), foldArtist(b)
	if fa == "" || fb == "" {
		return false
	}
	if fa == fb || strings.Contains(fa, fb) || strings.Contains(fb, fa) {
		return true
	}
	maxLen := len([]rune(fa))
	if l := len([]rune(fb)); l > maxLen {
		maxLen = l
	}
	return float64(LevenshteinDistance(fa, fb))/float64(maxLen) <= 0.35
}

// discoverTracks параллельно опрашивает провайдеров-Discoverer и оставляет записи с тем же названием
// и длительностью в пределах ±2 сек.
func (t *TournamentOrchestrator) discoverTracks(ctx context.Context, q SearchQuery) []DiscoveredTrack {
	var wg sync.WaitGroup
	var mu sync.Mutex
	var found []DiscoveredTrack

	for _, gp := range t.providers {
		d, ok := gp.prov.(Discoverer)
		if !ok || !gp.breaker.Allow() {
			continue
		}
		wg.Add(1)
		go func(id string, d Discoverer) {
			defer wg.Done()
			pCtx, cancel := context.WithTimeout(ctx, t.timeout)
			defer cancel()
			tracks, err := d.Discover(pCtx, q.Title, q.Duration)
			if err != nil {
				logger.Info("[DISCOVER] [%s] поиск по названию не удался: %v", id, err)
				return
			}
			mu.Lock()
			found = append(found, tracks...)
			mu.Unlock()
		}(gp.prov.ID(), d)
	}
	wg.Wait()

	var out []DiscoveredTrack
	for _, d := range found {
		if !MatchTitle(q.Title, d.Title) {
			continue
		}
		if q.Duration > 0 && d.Duration > 0 {
			diff := q.Duration - d.Duration
			if diff < 0 {
				diff = -diff
			}
			if diff > 2*time.Second {
				continue
			}
		}
		out = append(out, d)
	}
	return out
}

// realNameQueries строит запросы из реальных написаний исполнителя: алиасы MusicBrainz и записи, найденные по названию.
// already — ключи «артист|название» уже выполненных запросов.
func (t *TournamentOrchestrator) realNameQueries(ctx context.Context, q SearchQuery, already map[string]struct{}) []SearchQuery {
	var out []SearchQuery
	seenArtist := map[string]struct{}{strings.ToLower(q.Artist): {}}
	add := func(artist, title, src string) {
		artist = strings.TrimSpace(artist)
		if artist == "" {
			return
		}
		key := strings.ToLower(artist) + "|" + strings.ToLower(title)
		if _, ok := already[key]; ok {
			return
		}
		if _, ok := seenArtist[strings.ToLower(artist)]; ok {
			return
		}
		seenArtist[strings.ToLower(artist)] = struct{}{}
		v := q
		v.Artist = artist
		v.Title = title
		out = append(out, v)
		logger.Info("[DISCOVER] Реальное написание артиста (%s): %q", src, artist)
	}

	// 1. Настоящие алиасы из MusicBrainz
	if t.aliasSource != nil {
		aCtx, cancel := context.WithTimeout(ctx, 8*time.Second)
		names, err := t.aliasSource.ArtistNames(aCtx, q.Artist)
		cancel()
		if err != nil {
			logger.Info("[DISCOVER] MusicBrainz: %v", err)
		}
		for _, n := range names {
			add(n, q.Title, "MusicBrainz")
		}
	}

	// 2. Записи, найденные по названию и длительности
	tracks := t.discoverTracks(ctx, q)
	if len(tracks) > 0 {
		sort.SliceStable(tracks, func(i, j int) bool { return tracks[i].Artist < tracks[j].Artist })

		var plausible, unsure []DiscoveredTrack
		seen := map[string]struct{}{}
		for _, d := range tracks {
			k := strings.ToLower(d.Artist)
			if _, ok := seen[k]; ok {
				continue
			}
			seen[k] = struct{}{}
			if artistsPlausiblyEqual(q.Artist, d.Artist) {
				plausible = append(plausible, d)
			} else {
				unsure = append(unsure, d)
			}
		}
		for _, d := range plausible {
			add(d.Artist, d.Title, "база "+d.Provider)
		}
		// Остальных проверяет ИИ-судья — только выбор из реально найденных записей
		if len(unsure) > 0 && t.judge != nil {
			if len(unsure) > 8 {
				unsure = unsure[:8]
			}
			jCtx, cancel := context.WithTimeout(ctx, 12*time.Second)
			idx, err := t.judge.SameArtist(jCtx, q, unsure)
			cancel()
			if err != nil {
				logger.Warn("[DISCOVER] ИИ-судья недоступен: %v", err)
			}
			for _, i := range idx {
				if i >= 0 && i < len(unsure) {
					add(unsure[i].Artist, unsure[i].Title, "ИИ подтвердил запись базы "+unsure[i].Provider)
				}
			}
		}
	}
	if len(out) > 4 {
		out = out[:4]
	}
	return out
}
