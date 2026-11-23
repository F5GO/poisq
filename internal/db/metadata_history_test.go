package db

import (
	"context"
	"path/filepath"
	"testing"

	"github.com/F5GO/poisq/internal/musicmeta"
)

func TestMetadataHistoryAppendIsIdempotentAndPaged(t *testing.T) {
	database, err := OpenPoisqDatabase(filepath.Join(t.TempDir(), "p.db"))
	if err != nil {
		t.Fatal(err)
	}
	defer database.Close()
	tracks := NewTrackRepository(database)
	if err := tracks.UpsertTrack(TrackRecord{ID: "t", FilePath: "/music/t.flac"}); err != nil {
		t.Fatal(err)
	}
	staging := NewStagingRepository(database)
	proposal, err := staging.CreateProposal(StagingChangeRecord{TargetID: "t", ChangeType: "manual", CurrentJSON: `{}`, ProposedJSON: `{}`})
	if err != nil {
		t.Fatal(err)
	}
	repo := NewMetadataHistoryRepository(database)
	changes := []musicmeta.FieldChange{{Field: "title", Before: []byte(`"a"`), After: []byte(`"b"`)}}
	if err := repo.Append(context.Background(), "t", proposal, "tx", "manual", "studio", "studio", changes); err != nil {
		t.Fatal(err)
	}
	if err := repo.Append(context.Background(), "t", proposal, "tx", "manual", "studio", "studio", changes); err != nil {
		t.Fatal(err)
	}
	items, err := repo.List(context.Background(), "t", "", 0, 0, 10)
	if err != nil {
		t.Fatal(err)
	}
	if len(items) != 1 || items[0].Field != "title" {
		t.Fatalf("items=%#v", items)
	}
	if _, err := repo.List(context.Background(), "", "bad:field", 0, 0, 10); err == nil {
		t.Fatal("expected invalid field")
	}
}
