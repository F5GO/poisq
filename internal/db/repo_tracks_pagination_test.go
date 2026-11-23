package db

import (
	"fmt"
	"path/filepath"
	"testing"
)

func TestListTracksAfterIDPagesEntireLibrary(t *testing.T) {
	database, err := OpenPoisqDatabase(filepath.Join(t.TempDir(), "poisq.db"))
	if err != nil {
		t.Fatal(err)
	}
	defer database.Close()
	repo := NewTrackRepository(database)
	batch := make([]TrackRecord, 0, 1205)
	for index := 0; index < 1205; index++ {
		id := fmt.Sprintf("track-%04d", index)
		batch = append(batch, TrackRecord{ID: id, FilePath: "/music/" + id + ".flac", Title: id})
	}
	if err := repo.UpsertTracksBatch(batch); err != nil {
		t.Fatal(err)
	}
	after := ""
	seen := make(map[string]struct{}, len(batch))
	for {
		page, err := repo.ListTracksAfterID(after, 500)
		if err != nil {
			t.Fatal(err)
		}
		for _, track := range page {
			if track.ID <= after {
				t.Fatalf("unstable page order: %q after %q", track.ID, after)
			}
			if _, duplicate := seen[track.ID]; duplicate {
				t.Fatalf("duplicate track %q", track.ID)
			}
			seen[track.ID] = struct{}{}
		}
		if len(page) < 500 {
			break
		}
		after = page[len(page)-1].ID
	}
	if len(seen) != len(batch) {
		t.Fatalf("paged %d of %d tracks", len(seen), len(batch))
	}
}
