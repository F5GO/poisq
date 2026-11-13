package artistbio

import (
	"context"
	"net/http"
	"net/http/httptest"
	"strings"
	"sync"
	"testing"

	"github.com/F5GO/poisq/internal/keypool"
	"github.com/F5GO/poisq/internal/metadata"
)

const exactTestMBID = "cc197bad-dc9c-440d-a5b5-d52ba2e14234"

type fakeArtistLinks struct {
	links *metadata.MBArtistLinks
	err   error
}

func (f fakeArtistLinks) LookupArtistLinks(context.Context, string) (*metadata.MBArtistLinks, error) {
	return f.links, f.err
}

func exactResolvedArtist() ResolvedArtist {
	return ResolvedArtist{
		Context: IdentityContext{ArtistID: "artist", Name: "Кино", MusicBrainzID: exactTestMBID},
		Identity: IdentityResolution{
			Status: ResolutionResolved, Provider: ProviderMusicBrainz, ProviderArtistID: exactTestMBID,
			MusicBrainzID: exactTestMBID, Confidence: 1, EvidenceCodes: []string{"id:musicbrainz"},
		},
	}
}

func TestWikipediaSourceUsesExactWikidataRussianLink(t *testing.T) {
	wikidata := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.URL.Path != "/wiki/Special:EntityData/Q123.json" {
			t.Errorf("wikidata path = %q", r.URL.Path)
		}
		_, _ = w.Write([]byte(`{"entities":{"Q123":{"sitelinks":{"ruwiki":{"title":"Кино (группа)"}}}}}`))
	}))
	defer wikidata.Close()
	wikipedia := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.URL.Query().Get("titles") != "Кино (группа)" || r.URL.Query().Get("explaintext") != "1" {
			t.Errorf("wikipedia query = %q", r.URL.RawQuery)
		}
		_, _ = w.Write([]byte(`{"query":{"pages":{"123":{"pageid":123,"title":"Кино (группа)","extract":"Настоящее русское описание."}}}}`))
	}))
	defer wikipedia.Close()
	source, err := newWikipediaSource(fakeArtistLinks{links: &metadata.MBArtistLinks{
		ArtistMBID: exactTestMBID, WikidataID: "Q123",
	}}, wikidata.URL, wikipedia.URL)
	if err != nil {
		t.Fatalf("new source: %v", err)
	}
	result, err := source.Fetch(context.Background(), exactResolvedArtist())
	if err != nil {
		t.Fatalf("fetch: %v", err)
	}
	if result == nil || result.SourceArtistID != "Q123" || result.OriginalText != "Настоящее русское описание." || result.LanguageHint != "ru" {
		t.Fatalf("unexpected result: %#v", result)
	}
}

func TestWikipediaSourceDoesNotSearchByName(t *testing.T) {
	server := httptest.NewServer(http.HandlerFunc(func(http.ResponseWriter, *http.Request) {
		t.Fatal("provider must not be called without an exact link")
	}))
	defer server.Close()
	source, err := newWikipediaSource(fakeArtistLinks{links: &metadata.MBArtistLinks{ArtistMBID: exactTestMBID}}, server.URL, server.URL)
	if err != nil {
		t.Fatalf("new source: %v", err)
	}
	result, err := source.Fetch(context.Background(), exactResolvedArtist())
	if err != nil || result != nil {
		t.Fatalf("name-only fallback occurred: result=%#v err=%v", result, err)
	}
}

func TestAudioDBSourcePrefersRussianBiographyByMBID(t *testing.T) {
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.URL.Query().Get("i") != exactTestMBID {
			t.Errorf("MBID query = %q", r.URL.Query().Get("i"))
		}
		_, _ = w.Write([]byte(`{"artists":[{"idArtist":"42","strMusicBrainzID":"` + exactTestMBID + `","strBiographyRU":"Русское описание","strBiographyEN":"English bio"}]}`))
	}))
	defer server.Close()
	source, err := newTheAudioDBSource(server.URL)
	if err != nil {
		t.Fatalf("new source: %v", err)
	}
	result, err := source.Fetch(context.Background(), exactResolvedArtist())
	if err != nil {
		t.Fatalf("fetch: %v", err)
	}
	if result == nil || result.SourceArtistID != "42" || result.OriginalText != "Русское описание" || result.LanguageHint != "ru" {
		t.Fatalf("unexpected result: %#v", result)
	}
}

type fakeBiographyKeys struct {
	mu          sync.Mutex
	key         *keypool.LeasedKey
	err         error
	successes   int
	rateLimits  int
	invalid     int
	unavailable int
}

func (f *fakeBiographyKeys) Acquire(string) (*keypool.LeasedKey, error) { return f.key, f.err }
func (f *fakeBiographyKeys) ReportSuccess(int64) {
	f.mu.Lock()
	f.successes++
	f.mu.Unlock()
}
func (f *fakeBiographyKeys) ReportRateLimit429(int64, string) {
	f.mu.Lock()
	f.rateLimits++
	f.mu.Unlock()
}
func (f *fakeBiographyKeys) ReportUnavailable503(int64, string) {
	f.mu.Lock()
	f.unavailable++
	f.mu.Unlock()
}
func (f *fakeBiographyKeys) ReportInvalid(int64, int, string) {
	f.mu.Lock()
	f.invalid++
	f.mu.Unlock()
}

func TestLastFMSourceUsesMBIDRussianLocaleAndConfiguredKey(t *testing.T) {
	const secret = "lastfm-secret"
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		query := r.URL.Query()
		if query.Get("mbid") != exactTestMBID || query.Get("lang") != "ru" || query.Get("api_key") != secret {
			t.Errorf("unexpected query: %q", query.Encode())
		}
		_, _ = w.Write([]byte(`{"artist":{"name":"Кино","mbid":"` + exactTestMBID + `","bio":{"content":"Русское <b>описание</b><br />Вторая строка"}}}`))
	}))
	defer server.Close()
	keys := &fakeBiographyKeys{key: &keypool.LeasedKey{ID: 7, PlainKey: secret}}
	source, err := newLastFMSource(server.URL, keys)
	if err != nil {
		t.Fatalf("new source: %v", err)
	}
	result, err := source.Fetch(context.Background(), exactResolvedArtist())
	if err != nil {
		t.Fatalf("fetch: %v", err)
	}
	if result == nil || result.OriginalText != "Русское описание\nВторая строка" || result.LanguageHint != "ru" {
		t.Fatalf("unexpected result: %#v", result)
	}
	if keys.successes != 1 {
		t.Fatalf("key success count = %d", keys.successes)
	}
}

func TestLastFMSourceMissingKeyDoesNotCallProvider(t *testing.T) {
	server := httptest.NewServer(http.HandlerFunc(func(http.ResponseWriter, *http.Request) {
		t.Fatal("provider called without key")
	}))
	defer server.Close()
	source, err := newLastFMSource(server.URL, &fakeBiographyKeys{})
	if err != nil {
		t.Fatalf("new source: %v", err)
	}
	result, err := source.Fetch(context.Background(), exactResolvedArtist())
	if err != nil || result != nil {
		t.Fatalf("missing key result=%#v err=%v", result, err)
	}
}

func TestExactSourcesRejectOversizedOrMismatchedRecords(t *testing.T) {
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		_, _ = w.Write([]byte(`{"artists":[{"idArtist":"42","strMusicBrainzID":"different","strBiographyRU":"` + strings.Repeat("я", 100) + `"}]}`))
	}))
	defer server.Close()
	source, err := newTheAudioDBSource(server.URL)
	if err != nil {
		t.Fatal(err)
	}
	result, err := source.Fetch(context.Background(), exactResolvedArtist())
	if err != nil || result != nil {
		t.Fatalf("mismatched MBID accepted: %#v %v", result, err)
	}
}
