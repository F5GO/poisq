package metadata

import (
	"context"
	"encoding/json"
	"image"
	"image/png"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"testing"

	"github.com/F5GO/poisq/internal/db"
	"github.com/F5GO/poisq/internal/security"
)

func TestTrackAuditStagesCompleteMusicBrainzIdentity(t *testing.T) {
	const releaseID = "aaaaaaaa-aaaa-aaaa-aaaa-aaaaaaaaaaaa"
	const groupID = "bbbbbbbb-bbbb-bbbb-bbbb-bbbbbbbbbbbb"
	const artistID = "cccccccc-cccc-cccc-cccc-cccccccccccc"
	const releaseTrackID = "dddddddd-dddd-dddd-dddd-dddddddddddd"
	const recordingID = "eeeeeeee-eeee-eeee-eeee-eeeeeeeeeeee"
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		switch r.URL.Path {
		case "/release":
			_, _ = w.Write([]byte(`{"releases":[{
				"id":"` + releaseID + `","title":"Album","score":98,"date":"2020-01-01","track-count":1,
				"release-group":{"id":"` + groupID + `"},
				"artist-credit":[{"name":"Artist","artist":{"id":"` + artistID + `","name":"Artist"}}]
			}]}`))
		case "/release/" + releaseID:
			_, _ = w.Write([]byte(`{"id":"` + releaseID + `","release-group":{"id":"` + groupID + `"},"media":[{"position":1,"tracks":[{
				"id":"` + releaseTrackID + `","position":1,"title":"Song","length":180000,
				"recording":{"id":"` + recordingID + `","title":"Song"}
			}]}]}`))
		default:
			http.NotFound(w, r)
		}
	}))
	defer server.Close()

	root := t.TempDir()
	folder := filepath.Join(root, "Artist", "Album")
	if err := os.MkdirAll(folder, 0o755); err != nil {
		t.Fatal(err)
	}
	cover, err := os.Create(filepath.Join(folder, "cover.png"))
	if err != nil {
		t.Fatal(err)
	}
	if err := png.Encode(cover, image.NewNRGBA(image.Rect(0, 0, 1200, 1200))); err != nil {
		cover.Close()
		t.Fatal(err)
	}
	_ = cover.Close()

	database, err := db.OpenPoisqDatabase(filepath.Join(t.TempDir(), "poisq.db"))
	if err != nil {
		t.Fatal(err)
	}
	defer database.Close()
	tracks := db.NewTrackRepository(database)
	staging := db.NewStagingRepository(database)
	if err := tracks.UpsertTrack(db.TrackRecord{
		ID: "t", FilePath: filepath.Join(folder, "01 Song.flac"), FolderPath: folder,
		Artist: "Artist", AlbumArtist: "Artist", Album: "Album", Title: "Song", TrackNumber: 1, Year: 2020, Duration: 180,
	}); err != nil {
		t.Fatal(err)
	}
	guard, err := security.NewPathGuard(root)
	if err != nil {
		t.Fatal(err)
	}
	client := NewMusicBrainzClient()
	client.baseURL = server.URL
	scanner := NewLibraryScanner(root, tracks, staging, client, NewCoverFetcher(guard))
	created, err := scanner.RunTrackAudit(context.Background(), "t")
	if err != nil || created != 1 {
		t.Fatalf("created=%d err=%v", created, err)
	}
	items, err := staging.List("pending", 10)
	if err != nil || len(items) != 1 {
		t.Fatalf("items=%#v err=%v", items, err)
	}
	var proposal map[string]any
	if err := json.Unmarshal([]byte(items[0].ProposedJSON), &proposal); err != nil {
		t.Fatal(err)
	}
	for key, want := range map[string]string{
		"releaseMbid": releaseID, "releaseGroupMbid": groupID, "artistMbid": artistID,
		"recordingMbid": recordingID, "releaseTrackMbid": releaseTrackID,
	} {
		if proposal[key] != want {
			t.Fatalf("%s=%v want %s; proposal=%#v", key, proposal[key], want, proposal)
		}
	}
}
