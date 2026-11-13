package db

import (
	"context"
	"path/filepath"
	"testing"
)

func TestTrackRepositoryArtistIdentityContextIsBounded(t *testing.T) {
	database, err := OpenPoisqDatabase(filepath.Join(t.TempDir(), "poisq.db"))
	if err != nil {
		t.Fatalf("open database: %v", err)
	}
	defer database.Close()
	repo := NewTrackRepository(database)
	for i, track := range []TrackRecord{
		{ID: "1", FilePath: "/secret/1.flac", Artist: "Кино", AlbumArtist: "Кино", Album: "Группа крови", Title: "Группа крови", Year: 1988, ActiveLyrics: "secret lyrics"},
		{ID: "2", FilePath: "/secret/2.flac", Artist: "Кино", AlbumArtist: "Кино", Album: "Звезда по имени Солнце", Title: "Пачка сигарет", Year: 1989},
		{ID: "3", FilePath: "/secret/3.flac", Artist: "Другой", AlbumArtist: "Другой", Album: "Не брать", Title: "Не брать", Year: 2000},
	} {
		track.TrackNumber = i + 1
		if err := repo.UpsertTrack(track); err != nil {
			t.Fatalf("upsert track %d: %v", i, err)
		}
	}

	got, err := repo.ArtistIdentityContext(context.Background(), "Кино", 1, 1)
	if err != nil {
		t.Fatalf("artist identity context: %v", err)
	}
	if len(got.Albums) != 1 || len(got.Tracks) != 1 {
		t.Fatalf("bounds not enforced: %#v", got)
	}
	if got.Albums[0].Title == "Не брать" || got.Tracks[0] == "Не брать" {
		t.Fatalf("another artist leaked into context: %#v", got)
	}
}

func TestTrackRepositoryArtistIdentityContextTreatsNameAsData(t *testing.T) {
	database, err := OpenPoisqDatabase(filepath.Join(t.TempDir(), "poisq.db"))
	if err != nil {
		t.Fatalf("open database: %v", err)
	}
	defer database.Close()
	repo := NewTrackRepository(database)
	if _, err := repo.ArtistIdentityContext(context.Background(), `Кино' OR 1=1 --`, 30, 50); err != nil {
		t.Fatalf("SQL metacharacters were not treated as data: %v", err)
	}
	var count int
	if err := database.QueryRow(`SELECT COUNT(*) FROM tracks`).Scan(&count); err != nil {
		t.Fatalf("tracks table changed: %v", err)
	}
}
