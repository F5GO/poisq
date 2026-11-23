package db

import (
	"context"
	"path/filepath"
	"reflect"
	"testing"

	"github.com/F5GO/poisq/internal/musicmeta"
)

func TestMetadataPolicyRepositoryDefaultsAndRoundTrip(t *testing.T) {
	database, err := OpenPoisqDatabase(filepath.Join(t.TempDir(), "poisq.db"))
	if err != nil {
		t.Fatal(err)
	}
	defer database.Close()
	repo := NewMetadataPolicyRepository(database)
	defaults, err := repo.Get(context.Background())
	if err != nil || len(defaults.Rules) == 0 {
		t.Fatalf("defaults=%#v err=%v", defaults, err)
	}
	custom := musicmeta.MergePolicy{Rules: []musicmeta.ProviderRule{
		{Source: musicmeta.SourceManual, Provider: "studio", Priority: 1, RefreshMode: musicmeta.RefreshPriority, Enabled: true, MinConfidence: 1},
		{Source: musicmeta.SourceMusicBrainz, Priority: 2, RefreshMode: musicmeta.RefreshNewer, Enabled: false, MinConfidence: .8},
	}}
	if err := repo.Replace(context.Background(), custom); err != nil {
		t.Fatal(err)
	}
	got, err := repo.Get(context.Background())
	if err != nil {
		t.Fatal(err)
	}
	want, _ := musicmeta.ValidateMergePolicy(custom)
	if !reflect.DeepEqual(got, want) {
		t.Fatalf("got=%#v want=%#v", got, want)
	}
	if err := repo.Replace(context.Background(), musicmeta.MergePolicy{}); err == nil {
		t.Fatal("invalid replacement accepted")
	}
	still, _ := repo.Get(context.Background())
	if !reflect.DeepEqual(still, want) {
		t.Fatal("invalid replacement modified stored policy")
	}
}

func TestMetadataRepositorySetFieldLock(t *testing.T) {
	database, err := OpenPoisqDatabase(filepath.Join(t.TempDir(), "poisq.db"))
	if err != nil {
		t.Fatal(err)
	}
	defer database.Close()
	tracks := NewTrackRepository(database)
	if err := tracks.UpsertTrack(TrackRecord{ID: "t", Title: "Title"}); err != nil {
		t.Fatal(err)
	}
	repo := NewMetadataRepository(database)
	if err := repo.SetFieldLock(context.Background(), "t", musicmeta.FieldTitle, true); err != nil {
		t.Fatal(err)
	}
	doc, _ := repo.GetTrackDocument(context.Background(), "t")
	if !doc.FieldStates[musicmeta.FieldTitle].Locked {
		t.Fatal("field was not locked")
	}
	if err := repo.SetFieldLock(context.Background(), "t", musicmeta.FieldTitle, false); err != nil {
		t.Fatal(err)
	}
	doc, _ = repo.GetTrackDocument(context.Background(), "t")
	if doc.FieldStates[musicmeta.FieldTitle].Locked {
		t.Fatal("field was not unlocked")
	}
	if err := repo.SetFieldLock(context.Background(), "t", musicmeta.Field("unknown"), true); err == nil {
		t.Fatal("unknown field lock accepted")
	}
}
