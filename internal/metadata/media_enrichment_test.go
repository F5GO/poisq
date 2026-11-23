package metadata

import (
	"context"
	"net/http"
	"net/http/httptest"
	"path/filepath"
	"strings"
	"testing"

	"github.com/F5GO/poisq/internal/db"
	"github.com/F5GO/poisq/internal/security"
)

func TestRankArtworkCandidatesUsesRoleGeometryAndStableTies(t *testing.T) {
	input := []ArtworkCandidate{
		{URL: "https://images.example/wide.jpg", Source: "fanarttv", Width: 2160, Height: 400, Score: 70},
		{URL: "https://images.example/square.jpg", Source: "coverartarchive", Width: 1500, Height: 1500, Score: 70},
		{URL: "file:///tmp/bad.jpg", Source: "local", Width: 2000, Height: 2000, Score: 100},
	}
	if got := RankArtworkCandidates(ArtworkBanner, input); len(got) != 2 || !strings.Contains(got[0].URL, "wide") {
		t.Fatalf("banner ranking=%#v", got)
	}
	if got := RankArtworkCandidates(ArtworkAlbumPrimary, input); len(got) != 2 || !strings.Contains(got[0].URL, "square") {
		t.Fatalf("album ranking=%#v", got)
	}
	ties := []ArtworkCandidate{
		{URL: "https://b.example/image.jpg", Source: "same", Width: 1000, Height: 1000, Score: 50},
		{URL: "https://a.example/image.jpg", Source: "same", Width: 1000, Height: 1000, Score: 50},
	}
	if got := RankArtworkCandidates(ArtworkDisc, ties); len(got) != 2 || got[0].URL != ties[1].URL {
		t.Fatalf("unstable tie ranking=%#v", got)
	}
}

func TestStageArtworkUsesSafeRoleFilename(t *testing.T) {
	root := t.TempDir()
	folder := filepath.Join(root, "album")
	guard, _ := security.NewPathGuard(root)
	database, err := db.OpenPoisqDatabase(filepath.Join(t.TempDir(), "poisq.db"))
	if err != nil {
		t.Fatal(err)
	}
	defer database.Close()
	staging := db.NewStagingRepository(database)
	service := NewMediaEnrichmentService(guard, staging, db.NewMediaEnrichmentRepository(database), nil)
	id, err := service.StageArtwork(context.Background(), "album", "album-1", folder, ArtworkLogo, []ArtworkCandidate{{
		URL: "https://images.example/logo.png", Source: "fanarttv", Width: 1200, Height: 400, Score: 80,
	}})
	if err != nil {
		t.Fatal(err)
	}
	record, err := staging.GetByID(id)
	if err != nil || record.ChangeType != "artwork" || !strings.Contains(record.ProposedJSON, `"fileName":"logo.png"`) || !strings.Contains(record.ProposedJSON, `"role":"logo"`) {
		t.Fatalf("proposal=%#v err=%v", record, err)
	}
	if _, err := service.StageArtwork(context.Background(), "album", "album-1", folder, ArtworkArtistPrimary, []ArtworkCandidate{{
		URL: "https://images.example/artist.jpg", Source: "manual", Width: 1200, Height: 1200, Score: 80,
	}}); err == nil {
		t.Fatal("artist primary artwork accepted for album scope")
	}
}

func TestListenBrainzSimilarArtistsIsBoundedAndDeduplicated(t *testing.T) {
	seed := "cc197bad-dc9c-440d-a5b5-d52ba2e14234"
	peer := "1b022e01-4da6-387b-8658-8678046e4cef"
	peer2 := "2b022e01-4da6-387b-8658-8678046e4cef"
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.URL.Path != "/1/lb-radio/artist/"+seed || r.URL.Query().Get("max_similar_artists") != "25" || r.Header.Get("User-Agent") != musicBrainzUserAgent {
			t.Errorf("unexpected request: %s user-agent=%q", r.URL.String(), r.Header.Get("User-Agent"))
		}
		_, _ = w.Write([]byte(`{"` + seed + `":[
			{"similar_artist_mbid":"` + peer + `","similar_artist_name":"Peer","total_listen_count":50},
			{"similar_artist_mbid":"` + peer + `","similar_artist_name":"Peer duplicate","total_listen_count":40},
			{"similar_artist_mbid":"` + peer2 + `","similar_artist_name":"Peer 2","total_listen_count":25},
			{"similar_artist_mbid":"invalid","similar_artist_name":"Bad","total_listen_count":100}
		]}`))
	}))
	defer server.Close()
	client := NewListenBrainzClient()
	client.baseURL = server.URL
	values, err := client.SimilarArtists(context.Background(), seed, 100)
	if err != nil {
		t.Fatal(err)
	}
	if len(values) != 2 || values[0].SimilarMBID != peer || values[0].Score != 1 || values[1].Score != .5 {
		t.Fatalf("values=%#v", values)
	}
	if _, err := client.SimilarArtists(context.Background(), "../bad", 10); err == nil {
		t.Fatal("invalid MBID accepted")
	}
}
