package db

import (
	"context"
	"path/filepath"
	"testing"
)

func TestMediaEnrichmentRepositoryRoundTripAndAtomicReplacement(t *testing.T) {
	database, err := OpenPoisqDatabase(filepath.Join(t.TempDir(), "poisq.db"))
	if err != nil {
		t.Fatal(err)
	}
	defer database.Close()
	repo := NewMediaEnrichmentRepository(database)
	ctx := context.Background()

	art := ArtworkRecord{Scope: "album", ScopeID: "album-1", Role: "banner", FilePath: "/music/a/banner.jpg", Source: "fanarttv", Width: 2160, Height: 400, Score: 92}
	if err := repo.PutArtwork(ctx, art); err != nil {
		t.Fatal(err)
	}
	artwork, err := repo.ListArtwork(ctx, art.Scope, art.ScopeID)
	if err != nil || len(artwork) != 1 || artwork[0].Role != art.Role || artwork[0].Width != art.Width {
		t.Fatalf("artwork=%#v err=%v", artwork, err)
	}

	seed := "cc197bad-dc9c-440d-a5b5-d52ba2e14234"
	valid := []SimilarArtistRecord{{ReferenceMBID: seed, SimilarMBID: "1b022e01-4da6-387b-8658-8678046e4cef", Name: "Peer", Score: .8, ListenCount: 80}}
	if err := repo.ReplaceSimilarArtists(ctx, seed, valid); err != nil {
		t.Fatal(err)
	}
	invalid := []SimilarArtistRecord{{ReferenceMBID: seed, SimilarMBID: seed, Name: "self", Score: 1}}
	if err := repo.ReplaceSimilarArtists(ctx, seed, invalid); err == nil {
		t.Fatal("invalid replacement accepted")
	}
	values, err := repo.ListSimilarArtists(ctx, seed, 10)
	if err != nil || len(values) != 1 || values[0].SimilarMBID != valid[0].SimilarMBID {
		t.Fatalf("previous rows were not preserved: %#v err=%v", values, err)
	}
	if err := repo.DeleteArtwork(ctx, art.Scope, art.ScopeID, art.Role); err != nil {
		t.Fatal(err)
	}
}
