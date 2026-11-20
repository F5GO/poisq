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

func TestTrackRepositoryArtistIdentityContextMatchesCollaborativeCreditsSafely(t *testing.T) {
	database, err := OpenPoisqDatabase(filepath.Join(t.TempDir(), "poisq.db"))
	if err != nil {
		t.Fatalf("open database: %v", err)
	}
	defer database.Close()
	repo := NewTrackRepository(database)
	for i, track := range []TrackRecord{
		{ID: "1", FilePath: "/music/1.flac", Artist: "Grebz • 4atty aka Tilla • Sимптом", AlbumArtist: "Grebz • 4atty aka Tilla • Sимптом", Album: "STYLE IN MC", Title: "Юрфак", Year: 2019},
		{ID: "2", FilePath: "/music/2.flac", Artist: "Grebz feat. Другой", AlbumArtist: "Grebz", Album: "Рэп", Title: "Сталь мс", Year: 2019},
		{ID: "3", FilePath: "/music/3.flac", Artist: "GrebzX", AlbumArtist: "GrebzX", Album: "Чужой", Title: "Чужой", Year: 2020},
	} {
		track.TrackNumber = i + 1
		if err := repo.UpsertTrack(track); err != nil {
			t.Fatalf("upsert track %d: %v", i, err)
		}
	}

	got, err := repo.ArtistIdentityContext(context.Background(), "Grebz", 10, 10)
	if err != nil {
		t.Fatalf("collaborative context: %v", err)
	}
	if len(got.Albums) != 1 || got.Albums[0].Title != "Рэп" {
		// Exact match on track #2 ("Grebz" as AlbumArtist) hits fast path first; verify collaborative-only artist too.
		t.Fatalf("unexpected exact+collab result: %#v", got)
	}

	gotCollab, err := repo.ArtistIdentityContext(context.Background(), "4atty aka Tilla", 10, 10)
	if err != nil {
		t.Fatalf("collab-only context: %v", err)
	}
	if len(gotCollab.Albums) != 1 || gotCollab.Albums[0].Title != "STYLE IN MC" || len(gotCollab.Tracks) != 1 || gotCollab.Tracks[0] != "Юрфак" {
		t.Fatalf("expected collaborative credit match, got %#v", gotCollab)
	}

	noSubstring, err := repo.ArtistIdentityContext(context.Background(), "Greb", 10, 10)
	if err != nil {
		t.Fatalf("substring check: %v", err)
	}
	if len(noSubstring.Albums) != 0 || len(noSubstring.Tracks) != 0 {
		t.Fatalf("substring matched unexpectedly: %#v", noSubstring)
	}
}

func TestTrackRepositoryArtistIdentityContextExcludesGuestAlbums(t *testing.T) {
	database, err := OpenPoisqDatabase(filepath.Join(t.TempDir(), "poisq.db"))
	if err != nil {
		t.Fatalf("open database: %v", err)
	}
	defer database.Close()
	repo := NewTrackRepository(database)
	for i, track := range []TrackRecord{
		{ID: "1", FilePath: "/music/1.flac", Artist: "Host", AlbumArtist: "Host", Album: "Host Album", Title: "Solo", Year: 2025},
		{ID: "2", FilePath: "/music/2.flac", Artist: "Guest", AlbumArtist: "Host", Album: "Host Album", Title: "Exact Guest Credit", Year: 2025},
		{ID: "3", FilePath: "/music/3.flac", Artist: "Host, Other", AlbumArtist: "Host", Album: "Host Album 2", Title: "Collab", Year: 2025},
		{ID: "4", FilePath: "/music/4.flac", Artist: "Untagged", AlbumArtist: "", Album: "Untagged Album", Title: "Untagged Song", Year: 2024},
	} {
		track.TrackNumber = i + 1
		if err := repo.UpsertTrack(track); err != nil {
			t.Fatalf("upsert track %d: %v", i, err)
		}
	}

	guest, err := repo.ArtistIdentityContext(context.Background(), "Guest", 10, 10)
	if err != nil {
		t.Fatalf("guest context: %v", err)
	}
	if len(guest.Albums) != 0 || len(guest.Tracks) != 1 || guest.Tracks[0] != "Exact Guest Credit" {
		t.Fatalf("guest appearance must keep the track but not the host album: %#v", guest)
	}

	other, err := repo.ArtistIdentityContext(context.Background(), "Other", 10, 10)
	if err != nil {
		t.Fatalf("collaborative guest context: %v", err)
	}
	if len(other.Albums) != 0 || len(other.Tracks) != 1 || other.Tracks[0] != "Collab" {
		t.Fatalf("collaborative guest must keep the track but not the host album: %#v", other)
	}

	host, err := repo.ArtistIdentityContext(context.Background(), "Host", 10, 10)
	if err != nil {
		t.Fatalf("host context: %v", err)
	}
	if len(host.Albums) != 2 {
		t.Fatalf("host albums must remain: %#v", host)
	}

	untagged, err := repo.ArtistIdentityContext(context.Background(), "Untagged", 10, 10)
	if err != nil {
		t.Fatalf("untagged context: %v", err)
	}
	if len(untagged.Albums) != 1 || untagged.Albums[0].Title != "Untagged Album" {
		t.Fatalf("album without album_artist must fall back to track artist: %#v", untagged)
	}
}
