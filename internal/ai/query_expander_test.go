package ai

import (
	"context"
	"errors"
	"testing"

	"github.com/F5GO/poisq/internal/lyrics"
)

type fakeGen struct {
	out   string
	err   error
	calls int
}

func (f *fakeGen) GenerateText(ctx context.Context, s, u, m string) (string, error) {
	f.calls++
	return f.out, f.err
}

type memCache map[string]string

func (m memCache) Get(k string) (string, bool) { v, ok := m[k]; return v, ok }
func (m memCache) Set(k, v string) error       { m[k] = v; return nil }

func TestFallbackGeneratorUsesSecondOnError(t *testing.T) {
	a := &fakeGen{err: errors.New("503")}
	b := &fakeGen{out: "ok"}
	got, err := NewFallbackGenerator().Add("a", a).Add("b", b).GenerateText(context.Background(), "", "", "")
	if err != nil || got != "ok" || a.calls != 1 || b.calls != 1 {
		t.Fatalf("got=%q err=%v a=%d b=%d", got, err, a.calls, b.calls)
	}
}

func TestQueryExpanderCachesAndSanitizes(t *testing.T) {
	gen := &fakeGen{out: "```json\n{\"artist_names\":[\"MakSim\",\"МакSим\",\"bad\\nname\",\" Maksim \"],\"title_names\":[\"Otpuskayu\",\"Отпускаю\"]}\n```"}
	cache := memCache{}
	ex := NewLLMQueryExpander(gen, cache)
	q := lyrics.SearchQuery{Artist: "МакSим", Title: "Отпускаю"}

	got, err := ex.ExpandQuery(context.Background(), q)
	if err != nil {
		t.Fatal(err)
	}
	var artists, titles []string
	for _, v := range got {
		if v.Artist != q.Artist {
			artists = append(artists, v.Artist)
		}
		if v.Title != q.Title {
			titles = append(titles, v.Title)
		}
	}
	// «Maksim» совпадает с «MakSim» без учёта регистра и отбрасывается как дубликат; «bad\nname» — как мусор
	if len(artists) != 1 || artists[0] != "MakSim" {
		t.Fatalf("артисты: %v", artists)
	}
	if len(titles) != 1 || titles[0] != "Otpuskayu" {
		t.Fatalf("названия: %v", titles)
	}

	if _, err := ex.ExpandQuery(context.Background(), q); err != nil || gen.calls != 1 {
		t.Fatalf("второй вызов должен идти из кэша: calls=%d err=%v", gen.calls, err)
	}
}

func TestArtistJudgeParsesIndexesOnly(t *testing.T) {
	gen := &fakeGen{out: "```json\n{\"same\":[0,2]}\n```"}
	j := NewLLMArtistJudge(gen)
	idx, err := j.SameArtist(context.Background(),
		lyrics.SearchQuery{Artist: "МакSим", Title: "Отпускаю"},
		[]lyrics.DiscoveredTrack{{Artist: "MakSim"}, {Artist: "Drake"}, {Artist: "Maxim"}})
	if err != nil || len(idx) != 2 || idx[0] != 0 || idx[1] != 2 {
		t.Fatalf("idx=%v err=%v", idx, err)
	}
}
