package metadata

import (
	"context"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
)

func TestMusicBrainzSearchArtistsReturnsBoundedCandidates(t *testing.T) {
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if got := r.Header.Get("User-Agent"); got != musicBrainzUserAgent {
			t.Errorf("User-Agent = %q", got)
		}
		if got := r.URL.Query().Get("limit"); got != "2" {
			t.Errorf("limit = %q", got)
		}
		w.Header().Set("Content-Type", "application/json")
		_, _ = w.Write([]byte(`{"artists":[
			{"id":"mbid-1","name":"Кино","sort-name":"Кино","country":"RU","score":100,"aliases":[{"name":"Kino"},{"name":"Kino"}]},
			{"id":"mbid-2","name":"KINO","sort-name":"KINO","country":"US","score":82,"aliases":[]},
			{"id":"mbid-3","name":"Extra","score":10,"aliases":[]}
		]}`))
	}))
	defer server.Close()

	client := NewMusicBrainzClient()
	client.baseURL = server.URL
	artists, err := client.SearchArtists(context.Background(), "Кино", 2)
	if err != nil {
		t.Fatalf("search artists: %v", err)
	}
	if len(artists) != 2 || artists[0].ArtistMBID != "mbid-1" || len(artists[0].Aliases) != 1 {
		t.Fatalf("unexpected artists: %#v", artists)
	}
}

func TestMusicBrainzLookupArtistCompatibility(t *testing.T) {
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		_, _ = w.Write([]byte(`{"artists":[{"id":"mbid-1","name":"Кино","sort-name":"Кино","country":"RU","score":90,"aliases":[]}]}`))
	}))
	defer server.Close()
	client := NewMusicBrainzClient()
	client.baseURL = server.URL
	artist, err := client.LookupArtist(context.Background(), "Кино")
	if err != nil {
		t.Fatalf("lookup artist: %v", err)
	}
	if artist.ArtistMBID != "mbid-1" || artist.Score != 90 {
		t.Fatalf("unexpected artist: %#v", artist)
	}
}

func TestMusicBrainzBrowseArtistReleaseGroups(t *testing.T) {
	const artistID = "cc197bad-dc9c-440d-a5b5-d52ba2e14234"
	const releaseID = "1b022e01-4da6-387b-8658-8678046e4cef"
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.URL.Path != "/release-group" || r.URL.Query().Get("artist") != artistID || r.URL.Query().Get("limit") != "30" {
			t.Errorf("unexpected request: %s", r.URL.String())
		}
		if r.URL.Query().Get("release-group-status") != "website-default" {
			t.Errorf("missing release status filter")
		}
		_, _ = w.Write([]byte(`{"release-groups":[
			{"id":"` + releaseID + `","title":"Группа крови","first-release-date":"1988-01-05"},
			{"id":"invalid","title":"bad","first-release-date":"oops"}
		]}`))
	}))
	defer server.Close()
	client := NewMusicBrainzClient()
	client.baseURL = server.URL
	releases, err := client.BrowseArtistReleaseGroups(context.Background(), artistID, 30)
	if err != nil {
		t.Fatal(err)
	}
	if len(releases) != 1 || releases[0].ReleaseGroupMBID != releaseID || releases[0].Title != "Группа крови" || releases[0].Year != 1988 {
		t.Fatalf("unexpected release groups: %#v", releases)
	}
	if _, err := client.BrowseArtistReleaseGroups(context.Background(), "../../etc/passwd", 30); err == nil {
		t.Fatal("invalid artist MBID accepted")
	}
}

func TestMusicBrainzSearchArtistRecordings(t *testing.T) {
	const artistID = "cc197bad-dc9c-440d-a5b5-d52ba2e14234"
	const recordingID = "1b022e01-4da6-387b-8658-8678046e4cef"
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		query := r.URL.Query().Get("query")
		if r.URL.Path != "/recording" || !strings.Contains(query, "arid:"+artistID) || !strings.Contains(query, `recording:"Песня"`) || r.URL.Query().Get("limit") != "100" {
			t.Errorf("unexpected request: %s", r.URL.String())
		}
		_, _ = w.Write([]byte(`{"recordings":[
			{"id":"` + recordingID + `","title":"Песня"},
			{"id":"invalid","title":"bad"}
		]}`))
	}))
	defer server.Close()
	client := NewMusicBrainzClient()
	client.baseURL = server.URL
	recordings, err := client.SearchArtistRecordings(context.Background(), artistID, []string{"Песня"}, 100)
	if err != nil {
		t.Fatal(err)
	}
	if len(recordings) != 1 || recordings[0].RecordingMBID != recordingID || recordings[0].Title != "Песня" {
		t.Fatalf("unexpected recordings: %#v", recordings)
	}
	if _, err := client.SearchArtistRecordings(context.Background(), "../../etc/passwd", []string{"Песня"}, 100); err == nil {
		t.Fatal("invalid artist MBID accepted")
	}
	if _, err := client.SearchArtistRecordings(context.Background(), artistID, nil, 100); err == nil {
		t.Fatal("empty recording titles accepted")
	}
}

func TestMusicBrainzSearchArtistsRejectsInvalidAndMalformedResponses(t *testing.T) {
	client := NewMusicBrainzClient()
	if _, err := client.SearchArtists(context.Background(), "", 3); err == nil {
		t.Fatal("empty query accepted")
	}
	if _, err := client.SearchArtists(context.Background(), strings.Repeat("x", 2<<10+1), 3); err == nil {
		t.Fatal("oversized query accepted")
	}

	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		_, _ = w.Write([]byte(`{"artists":`))
	}))
	defer server.Close()
	client.baseURL = server.URL
	if _, err := client.SearchArtists(context.Background(), "Кино", 3); err == nil || strings.Contains(err.Error(), `{"artists"`) {
		t.Fatalf("malformed response error is missing or leaked body: %v", err)
	}
}

func TestMusicBrainzSearchArtistsDoesNotLeakProviderBody(t *testing.T) {
	const secret = "provider-secret-response"
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		w.WriteHeader(http.StatusInternalServerError)
		_, _ = w.Write([]byte(secret))
	}))
	defer server.Close()
	client := NewMusicBrainzClient()
	client.baseURL = server.URL
	_, err := client.SearchArtists(context.Background(), "Кино", 3)
	if err == nil || strings.Contains(err.Error(), secret) {
		t.Fatalf("provider body leaked through error: %v", err)
	}
}

func TestMusicBrainzSearchArtistsHonorsCancellation(t *testing.T) {
	ctx, cancel := context.WithCancel(context.Background())
	cancel()
	client := NewMusicBrainzClient()
	if _, err := client.SearchArtists(ctx, "Кино", 3); err == nil || !strings.Contains(err.Error(), context.Canceled.Error()) {
		t.Fatalf("cancellation not propagated: %v", err)
	}
}

func TestMusicBrainzArtistLinksReturnsOnlyExactSafeLinks(t *testing.T) {
	const mbid = "cc197bad-dc9c-440d-a5b5-d52ba2e14234"
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.URL.Path != "/artist/"+mbid || r.URL.Query().Get("inc") != "url-rels" {
			t.Errorf("unexpected request: %s", r.URL.String())
		}
		_, _ = w.Write([]byte(`{"id":"` + mbid + `","relations":[
			{"type":"wikidata","url":{"resource":"https://www.wikidata.org/wiki/Q123"}},
			{"type":"wikipedia","url":{"resource":"https://ru.wikipedia.org/wiki/%D0%9A%D0%B8%D0%BD%D0%BE_(%D0%B3%D1%80%D1%83%D0%BF%D0%BF%D0%B0)"}},
			{"type":"wikipedia","url":{"resource":"http://evil.example/wiki/Kino"}}
		]}`))
	}))
	defer server.Close()
	client := NewMusicBrainzClient()
	client.baseURL = server.URL
	links, err := client.LookupArtistLinks(context.Background(), mbid)
	if err != nil {
		t.Fatalf("lookup links: %v", err)
	}
	if links.WikidataID != "Q123" || !strings.HasPrefix(links.WikipediaURL, "https://ru.wikipedia.org/wiki/") {
		t.Fatalf("unexpected links: %#v", links)
	}
	if _, err := client.LookupArtistLinks(context.Background(), "../../etc/passwd"); err == nil {
		t.Fatal("unsafe MBID accepted")
	}
}
