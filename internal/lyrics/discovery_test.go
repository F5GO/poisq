package lyrics

import (
	"context"
	"testing"
	"time"

	"github.com/F5GO/poisq/internal/httpclient"
)

type fakeLRCProv struct {
	id       string
	realName string        // под каким именем артист записан в этой базе
	lrc      bool          // отдаёт ли синхронный LRC
	dur      time.Duration // длительность записи
}

func (f *fakeLRCProv) ID() string   { return f.id }
func (f *fakeLRCProv) Name() string { return f.id }
func (f *fakeLRCProv) Fetch(ctx context.Context, q SearchQuery) (*Lyrics, error) {
	if q.Artist != f.realName {
		return nil, ErrNotFound
	}
	l := &Lyrics{Kind: KindPlain, PlainLyrics: songA, Artist: f.realName, Title: q.Title, Duration: f.dur, Source: f.id}
	if f.lrc {
		l.Kind = KindLRC
		l.SyncedLyrics = makeLRC(songA, 7)
		l.Lines = ParseLRC(l.SyncedLyrics)
	}
	return l, nil
}
func (f *fakeLRCProv) Discover(ctx context.Context, title string, d time.Duration) ([]DiscoveredTrack, error) {
	return []DiscoveredTrack{
		{Provider: f.id, Artist: f.realName, Title: title, Duration: f.dur},
		{Provider: f.id, Artist: "Совсем Другой Артист", Title: title, Duration: f.dur + 40*time.Second}, // другая длительность — отсеется
	}, nil
}

// plainOnly отвечает на исходное написание артиста обычным текстом (чтобы было с кем сверять LRC).
type plainOnlyProv struct{ artist string }

func (p *plainOnlyProv) ID() string   { return "plainbase" }
func (p *plainOnlyProv) Name() string { return "plainbase" }
func (p *plainOnlyProv) Fetch(ctx context.Context, q SearchQuery) (*Lyrics, error) {
	return &Lyrics{Kind: KindPlain, PlainLyrics: songA, Artist: p.artist, Title: q.Title, Duration: 90 * time.Second, Source: "plainbase"}, nil
}

func newTestTournament(provs ...Provider) *TournamentOrchestrator {
	t := &TournamentOrchestrator{timeout: 2 * time.Second, tolerance: 6 * time.Second}
	for _, p := range provs {
		t.providers = append(t.providers, &guardedProvider{
			prov:    p,
			limiter: httpclient.NewProviderLimiter(4, time.Millisecond),
			breaker: httpclient.NewCircuitBreaker(5, time.Minute),
		})
	}
	return t
}

func TestTournamentFindsRealArtistSpellingByTitleAndDuration(t *testing.T) {
	// В библиотеке «Мумий Тролль», в базе LRC записан как «Mumiy Troll» — прямой вариант не угадать
	lrcBase := &fakeLRCProv{id: "lrcbase", realName: "Mumiy Troll", lrc: true, dur: 90 * time.Second}
	other := &plainOnlyProv{artist: "Мумий Тролль"}
	tr := newTestTournament(lrcBase, other)

	q := SearchQuery{Artist: "Мумий Тролль", Title: "Отпускаю", Duration: 90 * time.Second}
	res, err := tr.RunTournament(context.Background(), q)
	if err != nil {
		t.Fatal(err)
	}
	if res.Winner.ProviderID != "lrcbase" || res.Winner.Lyrics.Kind != KindLRC {
		t.Fatalf("ожидался LRC из lrcbase по реальному написанию, получено %s/%s; консенсус: %+v", res.Winner.ProviderID, res.Winner.Lyrics.Kind, res.Consensus)
	}
	if res.Consensus == nil || !res.Consensus.HasFullLRC {
		t.Fatalf("нет подтверждённого полноценного LRC: %+v", res.Consensus)
	}
}

func TestArtistsPlausiblyEqual(t *testing.T) {
	yes := [][2]string{{"Мумий Тролль", "Mumiy Troll"}, {"МакSим", "MakSim"}, {"Дельфин", "Delfin"}, {"Linkin Park", "linkin park"}}
	no := [][2]string{{"Дельфин", "Drake"}, {"Земфира", "Metallica"}}
	for _, c := range yes {
		if !artistsPlausiblyEqual(c[0], c[1]) {
			t.Errorf("%q и %q должны считаться похожими", c[0], c[1])
		}
	}
	for _, c := range no {
		if artistsPlausiblyEqual(c[0], c[1]) {
			t.Errorf("%q и %q не должны считаться похожими", c[0], c[1])
		}
	}
}
