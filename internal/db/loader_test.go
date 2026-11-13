package db

import (
	"context"
	"strings"
	"testing"
)

func TestLoadArtistInventoryReturnsBoundedDeterministicPages(t *testing.T) {
	database, _ := createTestDB(t)
	defer database.Close()
	_, err := database.Exec(`
		INSERT INTO artist(id, name) VALUES ('c', ' Третий '), ('a', 'Первый'), ('b', 'Второй'), ('empty', ' ');
	`)
	if err != nil {
		t.Fatal(err)
	}
	first, err := LoadArtistInventoryPage(context.Background(), database, "", 2)
	if err != nil {
		t.Fatal(err)
	}
	if len(first) != 2 || first[0].ID != "a" || first[1].ID != "b" {
		t.Fatalf("first page: %#v", first)
	}
	second, err := LoadArtistInventoryPage(context.Background(), database, first[1].ID, 2)
	if err != nil {
		t.Fatal(err)
	}
	if len(second) != 1 || second[0].ID != "c" || second[0].Name != "Третий" {
		t.Fatalf("second page: %#v", second)
	}
	if _, err := LoadArtistInventoryPage(context.Background(), database, strings.Repeat("x", 513), 2); err == nil {
		t.Fatal("oversized cursor accepted")
	}
}

func TestSearchArtistInventoryEscapesWildcardsAndOrders(t *testing.T) {
	database, _ := createTestDB(t)
	defer database.Close()
	_, err := database.Exec(`INSERT INTO artist(id, name) VALUES
		('percent', '100% Живой'), ('plain', '1000 Живой'), ('under', 'A_B'),
		('under2', 'ACB'), ('z', ' кино '), ('a', 'Кино');`)
	if err != nil {
		t.Fatal(err)
	}
	percent, err := SearchArtistInventory(context.Background(), database, "%", 25)
	if err != nil || len(percent) != 1 || percent[0].ID != "percent" {
		t.Fatalf("literal percent search: %#v err=%v", percent, err)
	}
	underscore, err := SearchArtistInventory(context.Background(), database, "_", 25)
	if err != nil || len(underscore) != 1 || underscore[0].ID != "under" {
		t.Fatalf("literal underscore search: %#v err=%v", underscore, err)
	}
	ordered, err := SearchArtistInventory(context.Background(), database, "кино", 25)
	if err != nil || len(ordered) != 2 || ordered[0].ID != "a" || ordered[1].ID != "z" {
		t.Fatalf("ordered search: %#v err=%v", ordered, err)
	}
	if _, err := SearchArtistInventory(context.Background(), database, strings.Repeat("x", MaxArtistSearchQueryBytes+1), 25); err == nil {
		t.Fatal("oversized query accepted")
	}
}

func TestFindLibraryArtistValidatesAndFindsExactID(t *testing.T) {
	database, _ := createTestDB(t)
	defer database.Close()
	if _, err := database.Exec(`INSERT INTO artist(id, name) VALUES ('artist-1', ' Кино ')`); err != nil {
		t.Fatal(err)
	}
	artist, found, err := FindLibraryArtist(context.Background(), database, "artist-1")
	if err != nil || !found || artist.Name != "Кино" {
		t.Fatalf("exact lookup: %#v found=%v err=%v", artist, found, err)
	}
	if _, found, err := FindLibraryArtist(context.Background(), database, "missing"); err != nil || found {
		t.Fatalf("missing lookup: found=%v err=%v", found, err)
	}
	if _, _, err := FindLibraryArtist(context.Background(), database, "bad\nvalue"); err == nil {
		t.Fatal("control-character id accepted")
	}
	cancelled, cancel := context.WithCancel(context.Background())
	cancel()
	if _, _, err := FindLibraryArtist(cancelled, database, "artist-1"); err == nil {
		t.Fatal("cancelled lookup succeeded")
	}
}
