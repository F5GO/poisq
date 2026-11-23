package db

import (
	"context"
	"fmt"
	"path/filepath"
	"reflect"
	"sync"
	"testing"
	"time"

	"github.com/F5GO/poisq/internal/musicmeta"
)

func TestMetadataRepositoryRoundTrip(t *testing.T) {
	database, err := OpenPoisqDatabase(filepath.Join(t.TempDir(), "poisq.db"))
	if err != nil {
		t.Fatal(err)
	}
	defer database.Close()
	tracks := NewTrackRepository(database)
	if err := tracks.UpsertTrack(TrackRecord{ID: "t", FilePath: "/m/t.flac"}); err != nil {
		t.Fatal(err)
	}
	repo := NewMetadataRepository(database)
	doc := musicmeta.Document{
		TrackID: "t", Title: "Song", Subtitle: "Live", Album: "Album", SortTitle: "Song, The", SortAlbum: "Album, The",
		ReleaseDate:         musicmeta.PartialDate{Value: "2001-02", Precision: musicmeta.PrecisionMonth},
		OriginalReleaseDate: musicmeta.PartialDate{Value: "1999-12-31", Precision: musicmeta.PrecisionDay},
		TrackNumber:         3, TrackTotal: 12, DiscNumber: 2, DiscTotal: 3, Compilation: true,
		Label: "Label", CatalogNumber: "CAT-1", Barcode: "12345678", ISRC: "USRC17607839", Grouping: "Suite", Comment: "Line 1\nLine 2", Copyright: "Copyright",
		Credits: []musicmeta.Credit{
			{Role: musicmeta.RoleArtist, Name: "Artist", JoinPhrase: " feat. ", ExternalIDs: musicmeta.ExternalIDs{{Type: musicmeta.IDMusicBrainzArtist, Value: "aaaaaaaa-aaaa-aaaa-aaaa-aaaaaaaaaaaa"}}},
			{Role: musicmeta.RoleComposer, Name: "Composer"},
		},
		Genres: []string{"Rock", "Alternative"},
		ExternalIDs: musicmeta.ExternalIDs{
			{Type: musicmeta.IDMusicBrainzRecording, Value: "bbbbbbbb-bbbb-bbbb-bbbb-bbbbbbbbbbbb"},
			{Type: musicmeta.IDMusicBrainzReleaseTrack, Value: "cccccccc-cccc-cccc-cccc-cccccccccccc"},
		},
		FieldStates: map[musicmeta.Field]musicmeta.FieldState{
			musicmeta.FieldTitle: {Source: musicmeta.SourceAudioFile, Provider: "tags", Confidence: .95, Locked: true, UpdatedAt: time.Unix(123, 0).UTC()},
		},
	}
	if err := repo.PutTrackDocument(context.Background(), doc); err != nil {
		t.Fatal(err)
	}
	got, err := repo.GetTrackDocument(context.Background(), "t")
	if err != nil {
		t.Fatal(err)
	}
	clean, _ := musicmeta.ValidateDocument(doc)
	if !reflect.DeepEqual(got, clean) {
		t.Fatalf("round trip mismatch\ngot:  %#v\nwant: %#v", got, clean)
	}
	legacy, err := tracks.GetByID("t")
	if err != nil {
		t.Fatal(err)
	}
	if legacy.Title != "Song" || legacy.Artist != "Artist" || legacy.Year != 2001 || legacy.TrackNumber != 3 {
		t.Fatalf("legacy projection not updated: %#v", legacy)
	}
}

func TestMetadataRepositorySynthesizesLegacyDocument(t *testing.T) {
	database, err := OpenPoisqDatabase(filepath.Join(t.TempDir(), "poisq.db"))
	if err != nil {
		t.Fatal(err)
	}
	defer database.Close()
	tracks := NewTrackRepository(database)
	if err := tracks.UpsertTrack(TrackRecord{ID: "t", FilePath: "/m/t.flac", Artist: "A; B", AlbumArtist: "A", Album: "Album", Title: "Song", Year: 2001, TrackNumber: 2}); err != nil {
		t.Fatal(err)
	}
	doc, err := NewMetadataRepository(database).GetTrackDocument(context.Background(), "t")
	if err != nil {
		t.Fatal(err)
	}
	if doc.Title != "Song" || doc.ReleaseDate.Precision != musicmeta.PrecisionYear || musicmeta.DisplayCredits(doc.Credits, musicmeta.RoleArtist) != "A; B" {
		t.Fatalf("unexpected legacy document: %#v", doc)
	}
}

func TestMetadataRepositoryInvalidWriteIsAtomic(t *testing.T) {
	database, err := OpenPoisqDatabase(filepath.Join(t.TempDir(), "poisq.db"))
	if err != nil {
		t.Fatal(err)
	}
	defer database.Close()
	tracks := NewTrackRepository(database)
	if err := tracks.UpsertTrack(TrackRecord{ID: "t", FilePath: "/m/t.flac", Title: "Before"}); err != nil {
		t.Fatal(err)
	}
	repo := NewMetadataRepository(database)
	if err := repo.PutTrackDocument(context.Background(), musicmeta.Document{TrackID: "t", Title: "After", ExternalIDs: musicmeta.ExternalIDs{{Type: musicmeta.IDMusicBrainzRecording, Value: "bad"}}}); err == nil {
		t.Fatal("expected validation failure")
	}
	track, _ := tracks.GetByID("t")
	if track.Title != "Before" {
		t.Fatalf("partial legacy update: %#v", track)
	}
	var count int
	_ = database.QueryRow(`SELECT COUNT(*) FROM track_metadata WHERE track_id='t'`).Scan(&count)
	if count != 0 {
		t.Fatal("partial normalized update")
	}
}

func TestMetadataRepositoryDeleteCascades(t *testing.T) {
	database, err := OpenPoisqDatabase(filepath.Join(t.TempDir(), "poisq.db"))
	if err != nil {
		t.Fatal(err)
	}
	defer database.Close()
	tracks := NewTrackRepository(database)
	_ = tracks.UpsertTrack(TrackRecord{ID: "t", FilePath: "/m/t.flac"})
	repo := NewMetadataRepository(database)
	if err := repo.PutTrackDocument(context.Background(), musicmeta.Document{
		TrackID: "t", Credits: []musicmeta.Credit{{Role: musicmeta.RoleArtist, Name: "A"}},
		ExternalIDs: musicmeta.ExternalIDs{{Type: musicmeta.IDAudioDBArtist, Value: "1"}},
		FieldStates: map[musicmeta.Field]musicmeta.FieldState{musicmeta.FieldTitle: {Source: musicmeta.SourceLegacy, Confidence: 1}},
	}); err != nil {
		t.Fatal(err)
	}
	if _, err := database.Exec(`DELETE FROM tracks WHERE id='t'`); err != nil {
		t.Fatal(err)
	}
	for _, table := range []string{"track_metadata", "track_credits", "metadata_external_ids", "metadata_field_state"} {
		var count int
		_ = database.QueryRow(`SELECT COUNT(*) FROM ` + table).Scan(&count)
		if count != 0 {
			t.Fatalf("orphan in %s", table)
		}
	}
}

func TestMetadataRepositoryLegacyUpdatePreservesRichFields(t *testing.T) {
	database, err := OpenPoisqDatabase(filepath.Join(t.TempDir(), "poisq.db"))
	if err != nil {
		t.Fatal(err)
	}
	defer database.Close()
	tracks := NewTrackRepository(database)
	if err := tracks.UpsertTrack(TrackRecord{ID: "t", FilePath: "/m/t.flac", Artist: "Old", Title: "Old"}); err != nil {
		t.Fatal(err)
	}
	repo := NewMetadataRepository(database)
	doc := musicmeta.Document{
		TrackID: "t", Title: "Old", DiscNumber: 2, Genres: []string{"Rock"},
		Credits:     []musicmeta.Credit{{Role: musicmeta.RoleArtist, Name: "Old"}, {Role: musicmeta.RoleComposer, Name: "Composer"}},
		ExternalIDs: musicmeta.ExternalIDs{{Type: musicmeta.IDMusicBrainzRecording, Value: "aaaaaaaa-aaaa-aaaa-aaaa-aaaaaaaaaaaa"}},
		FieldStates: map[musicmeta.Field]musicmeta.FieldState{musicmeta.FieldGenres: {Source: musicmeta.SourceManual, Confidence: 1, Locked: true}},
	}
	if err := repo.PutTrackDocument(context.Background(), doc); err != nil {
		t.Fatal(err)
	}
	if err := tracks.UpdateMetadata("t", "New", "Album", "New", "New title", 2002, false); err != nil {
		t.Fatal(err)
	}
	got, err := repo.GetTrackDocument(context.Background(), "t")
	if err != nil {
		t.Fatal(err)
	}
	if got.Title != "New title" || got.DiscNumber != 2 || len(got.Genres) != 1 || len(got.ExternalIDs) != 1 || !got.FieldStates[musicmeta.FieldGenres].Locked || musicmeta.DisplayCredits(got.Credits, musicmeta.RoleComposer) != "Composer" {
		t.Fatalf("legacy update lost metadata: %#v", got)
	}
}

func TestMetadataRepositoryConcurrentReadersSeeWholeDocument(t *testing.T) {
	database, err := OpenPoisqDatabase(filepath.Join(t.TempDir(), "poisq.db"))
	if err != nil {
		t.Fatal(err)
	}
	defer database.Close()
	tracks := NewTrackRepository(database)
	if err := tracks.UpsertTrack(TrackRecord{ID: "t", FilePath: "/m/t.flac"}); err != nil {
		t.Fatal(err)
	}
	repo := NewMetadataRepository(database)
	makeDoc := func(value string) musicmeta.Document {
		return musicmeta.Document{TrackID: "t", Title: value, Credits: []musicmeta.Credit{{Role: musicmeta.RoleArtist, Name: value}}, Genres: []string{value}}
	}
	if err := repo.PutTrackDocument(context.Background(), makeDoc("A")); err != nil {
		t.Fatal(err)
	}
	errCh := make(chan error, 8)
	var wg sync.WaitGroup
	wg.Add(1)
	go func() {
		defer wg.Done()
		for i := 0; i < 40; i++ {
			value := "A"
			if i%2 == 1 {
				value = "B"
			}
			if err := repo.PutTrackDocument(context.Background(), makeDoc(value)); err != nil {
				errCh <- err
				return
			}
		}
	}()
	for reader := 0; reader < 4; reader++ {
		wg.Add(1)
		go func() {
			defer wg.Done()
			for i := 0; i < 40; i++ {
				doc, err := repo.GetTrackDocument(context.Background(), "t")
				if err != nil {
					errCh <- err
					return
				}
				if len(doc.Genres) != 1 || len(doc.Credits) != 1 || doc.Genres[0] != doc.Title || doc.Credits[0].Name != doc.Title {
					errCh <- fmt.Errorf("torn document: %#v", doc)
					return
				}
			}
		}()
	}
	wg.Wait()
	close(errCh)
	for err := range errCh {
		if err != nil {
			t.Fatal(err)
		}
	}
}
