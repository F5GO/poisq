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

type fakeRegionalWikipediaLink struct {
	rawURL   string
	sourceID string
}

func (f fakeRegionalWikipediaLink) ExactWikipediaLink(context.Context, ResolvedArtist) (string, string, error) {
	return f.rawURL, f.sourceID, nil
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

func TestWikipediaSourceFallsBackToExactForeignArticleForTranslation(t *testing.T) {
	wikidata := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		_, _ = w.Write([]byte(`{"entities":{"Q456":{"sitelinks":{"dewiki":{"title":"Phil Fuldner"}}}}}`))
	}))
	deWikipedia := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.URL.Query().Get("titles") != "Phil Fuldner" {
			t.Errorf("wikipedia title = %q", r.URL.Query().Get("titles"))
		}
		_, _ = w.Write([]byte(`{"query":{"pages":{"456":{"pageid":456,"title":"Phil Fuldner","extract":"Philipp André Fuldner ist ein deutscher DJ und Musikproduzent. Er veröffentlichte mehrere Singles und arbeitete als Moderator."}}}}`))
	}))
	defer wikidata.Close()
	defer deWikipedia.Close()
	source, err := newWikipediaSourceWithLanguages(fakeArtistLinks{links: &metadata.MBArtistLinks{
		ArtistMBID: exactTestMBID, WikidataID: "Q456", WikipediaURL: "https://de.wikipedia.org/wiki/Phil_Fuldner",
	}}, wikidata.URL, map[string]string{"ru": deWikipedia.URL, "de": deWikipedia.URL})
	if err != nil {
		t.Fatal(err)
	}
	result, err := source.Fetch(context.Background(), exactResolvedArtist())
	if err != nil {
		t.Fatal(err)
	}
	if result == nil || result.SourceArtistID != "Q456" || result.LanguageHint != "de" || !strings.Contains(result.OriginalText, "Musikproduzent") || !strings.HasPrefix(result.SourceURL, "https://de.wikipedia.org/wiki/") {
		t.Fatalf("unexpected foreign Wikipedia result: %#v", result)
	}
}

func TestWikipediaSourceUsesRegionalCatalogExactLink(t *testing.T) {
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.URL.Query().Get("titles") != "Anikv" {
			t.Errorf("title = %q", r.URL.Query().Get("titles"))
		}
		_, _ = w.Write([]byte(`{"query":{"pages":{"7":{"pageid":7,"title":"Anikv","extract":"Anikv is a Georgian singer and songwriter whose releases include several studio recordings and collaborations."}}}}`))
	}))
	defer server.Close()
	source, err := newWikipediaSourceWithLanguages(fakeArtistLinks{links: &metadata.MBArtistLinks{}}, server.URL, map[string]string{
		"ru": server.URL, "en": server.URL,
	})
	if err != nil {
		t.Fatal(err)
	}
	source.regionalLinks = fakeRegionalWikipediaLink{rawURL: "https://en.wikipedia.org/wiki/Anikv", sourceID: "yandex:41075"}
	artist := exactResolvedArtist()
	artist.Identity.MusicBrainzID = ""
	result, err := source.Fetch(context.Background(), artist)
	if err != nil || result == nil || result.LanguageHint != "en" || result.SourceArtistID != "yandex:41075" ||
		!containsString(result.EvidenceCodes, "catalog:yandex") || containsString(result.EvidenceCodes, "id:musicbrainz") {
		t.Fatalf("regional Wikipedia fallback failed: result=%#v err=%v", result, err)
	}
}

func TestExactWikipediaArticleRejectsLookalikeHosts(t *testing.T) {
	for _, raw := range []string{
		"http://de.wikipedia.org/wiki/Artist",
		"https://wikipedia.org.evil.example/wiki/Artist",
		"https://evil.de.wikipedia.org/wiki/Artist",
	} {
		if _, ok := exactWikipediaArticle(raw); ok {
			t.Fatalf("unsafe Wikipedia URL accepted: %s", raw)
		}
	}
}

func TestWikipediaSourceRejectsUnverifiedExactNamePage(t *testing.T) {
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.URL.Query().Get("titles") != "Кино" || r.URL.Query().Get("prop") != "extracts|pageprops" {
			t.Errorf("unexpected exact fallback query: %q", r.URL.RawQuery)
		}
		_, _ = w.Write([]byte(`{"query":{"pages":{"9":{"pageid":9,"title":"Кино","extract":"Статья о кинематографе без подтверждённых релизов."}}}}`))
	}))
	defer server.Close()
	source, err := newWikipediaSource(fakeArtistLinks{links: &metadata.MBArtistLinks{ArtistMBID: exactTestMBID}}, server.URL, server.URL)
	if err != nil {
		t.Fatalf("new source: %v", err)
	}
	result, err := source.Fetch(context.Background(), exactResolvedArtist())
	if err != nil || result != nil {
		t.Fatalf("unverified name fallback occurred: result=%#v err=%v", result, err)
	}
}

func TestWikipediaSourceUsesExactNameWithLocalReleaseEvidence(t *testing.T) {
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.URL.Query().Get("titles") != "Biicla" || r.URL.Query().Get("redirects") != "1" {
			t.Errorf("unexpected exact fallback query: %q", r.URL.RawQuery)
		}
		_, _ = w.Write([]byte(`{"query":{"pages":{"11637091":{"pageid":11637091,"title":"Biicla","pageprops":{"wikibase_item":"Q141738077"},"extract":"Biicla — российский музыкальный продюсер. Его альбом Hyper House стал заметным электронным релизом."}}}}`))
	}))
	defer server.Close()
	source, err := newWikipediaSource(fakeArtistLinks{links: &metadata.MBArtistLinks{ArtistMBID: exactTestMBID}}, server.URL, server.URL)
	if err != nil {
		t.Fatal(err)
	}
	artist := exactResolvedArtist()
	artist.Context.Name = "Biicla"
	artist.Context.Albums = []IdentityRelease{{Title: "Hyper House", Year: 2024}}
	result, err := source.Fetch(context.Background(), artist)
	if err != nil || result == nil {
		t.Fatalf("verified exact fallback failed: result=%#v err=%v", result, err)
	}
	if result.SourceArtistID != "Q141738077" || result.LanguageHint != "ru" || result.MatchConfidence != 1 ||
		!containsString(result.EvidenceCodes, "album:mentioned") || !strings.HasPrefix(result.SourceURL, "https://ru.wikipedia.org/wiki/") {
		t.Fatalf("unexpected exact fallback result: %#v", result)
	}
}

func TestWikipediaReleaseEvidenceRequiresAlbumOrTwoTracks(t *testing.T) {
	extract := "Исполнитель выпустил Hyper House и синглы No Place и Making New."
	if got := wikipediaReleaseEvidence(extract, []IdentityRelease{{Title: "Hyper House"}}, nil); got != "album:mentioned" {
		t.Fatalf("album evidence = %q", got)
	}
	if got := wikipediaReleaseEvidence(extract, nil, []string{"No Place"}); got != "" {
		t.Fatalf("single track was decisive: %q", got)
	}
	if got := wikipediaReleaseEvidence(extract, nil, []string{"No Place", "Making New"}); got != "track:multiple_mentioned" {
		t.Fatalf("track evidence = %q", got)
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
