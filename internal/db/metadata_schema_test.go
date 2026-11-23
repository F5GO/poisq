package db

import (
	"context"
	"database/sql"
	"path/filepath"
	"testing"

	_ "modernc.org/sqlite"
)

func TestMetadataMigrationCreatesSchemaAndIsIdempotent(t *testing.T) {
	database, err := OpenPoisqDatabase(filepath.Join(t.TempDir(), "poisq.db"))
	if err != nil {
		t.Fatal(err)
	}
	defer database.Close()
	if err := applyMetadataMigrations(context.Background(), database); err != nil {
		t.Fatalf("second migration run: %v", err)
	}
	for _, table := range []string{"track_metadata", "track_credits", "track_credit_external_ids", "track_genres", "track_custom_tags", "metadata_external_ids", "metadata_field_state", "track_media_info", "metadata_provider_policy", "writeback_transactions", "track_audio_analysis", "album_audio_analysis", "media_artwork", "similar_artists", "metadata_field_history", "metadata_history_revert_links"} {
		var name string
		if err := database.QueryRow(`SELECT name FROM sqlite_master WHERE type='table' AND name=?`, table).Scan(&name); err != nil || name != table {
			t.Fatalf("table %s missing: %v", table, err)
		}
	}
	var count int
	if err := database.QueryRow(`SELECT COUNT(*) FROM schema_migrations WHERE version=1`).Scan(&count); err != nil || count != 1 {
		t.Fatalf("migration rows = %d, %v", count, err)
	}
	if err := database.QueryRow(`SELECT COUNT(*) FROM schema_migrations WHERE version=2`).Scan(&count); err != nil || count != 1 {
		t.Fatalf("media-info migration rows = %d, %v", count, err)
	}
	if err := database.QueryRow(`SELECT COUNT(*) FROM schema_migrations WHERE version=3`).Scan(&count); err != nil || count != 1 {
		t.Fatalf("policy migration rows = %d, %v", count, err)
	}
	if err := database.QueryRow(`SELECT COUNT(*) FROM schema_migrations WHERE version=4`).Scan(&count); err != nil || count != 1 {
		t.Fatalf("writeback migration rows = %d, %v", count, err)
	}
	if err := database.QueryRow(`SELECT COUNT(*) FROM schema_migrations WHERE version=5`).Scan(&count); err != nil || count != 1 {
		t.Fatalf("audio analysis migration rows = %d, %v", count, err)
	}
	if err := database.QueryRow(`SELECT COUNT(*) FROM schema_migrations WHERE version=6`).Scan(&count); err != nil || count != 1 {
		t.Fatalf("media enrichment migration rows = %d, %v", count, err)
	}
	for _, version := range []int{7, 8, 9} {
		if err := database.QueryRow(`SELECT COUNT(*) FROM schema_migrations WHERE version=?`, version).Scan(&count); err != nil || count != 1 {
			t.Fatalf("migration %d rows = %d, %v", version, count, err)
		}
	}
}

func TestMetadataMigrationPreservesLegacyRows(t *testing.T) {
	path := filepath.Join(t.TempDir(), "poisq.db")
	database, err := sql.Open("sqlite", path)
	if err != nil {
		t.Fatal(err)
	}
	if err := initPoisqSchema(database); err != nil {
		t.Fatal(err)
	}
	if _, err := database.Exec(`INSERT INTO tracks(id,file_path,artist,album,title,year) VALUES('t','/m/t.flac','A','B','C',2001)`); err != nil {
		t.Fatal(err)
	}
	if err := applyMetadataMigrations(context.Background(), database); err != nil {
		t.Fatal(err)
	}
	var artist, title string
	var year int
	if err := database.QueryRow(`SELECT artist,title,year FROM tracks WHERE id='t'`).Scan(&artist, &title, &year); err != nil {
		t.Fatal(err)
	}
	if artist != "A" || title != "C" || year != 2001 {
		t.Fatalf("legacy row changed: %q %q %d", artist, title, year)
	}
	database.Close()
}

func TestMetadataMigrationFailureRollsBackVersion(t *testing.T) {
	database, err := sql.Open("sqlite", filepath.Join(t.TempDir(), "fail.db"))
	if err != nil {
		t.Fatal(err)
	}
	defer database.Close()
	if _, err := database.Exec(`CREATE TABLE schema_migrations(version INTEGER PRIMARY KEY,name TEXT,applied_at INTEGER)`); err != nil {
		t.Fatal(err)
	}
	err = applySchemaMigrations(context.Background(), database, []schemaMigration{{version: 99, name: "bad", up: `CREATE TABLE x(id); THIS IS NOT SQL;`}})
	if err == nil {
		t.Fatal("expected migration failure")
	}
	var count int
	_ = database.QueryRow(`SELECT COUNT(*) FROM schema_migrations WHERE version=99`).Scan(&count)
	if count != 0 {
		t.Fatal("failed migration was recorded")
	}
	var tableCount int
	_ = database.QueryRow(`SELECT COUNT(*) FROM sqlite_master WHERE type='table' AND name='x'`).Scan(&tableCount)
	if tableCount != 0 {
		t.Fatal("failed migration left partial schema")
	}
}

func TestMetadataResetClearsNormalizedRows(t *testing.T) {
	database, err := OpenPoisqDatabase(filepath.Join(t.TempDir(), "poisq.db"))
	if err != nil {
		t.Fatal(err)
	}
	defer database.Close()
	if _, err := database.Exec(`
		INSERT INTO tracks(id,file_path) VALUES('t','/m/t.flac');
		INSERT INTO track_metadata(track_id,subtitle) VALUES('t','sub');
		INSERT INTO track_credits(track_id,role,position,name) VALUES('t','artist',0,'A');
		INSERT INTO metadata_external_ids(scope,scope_id,id_type,position,value) VALUES('track','t','audiodb_artist',0,'1');
		INSERT INTO metadata_field_state(scope,scope_id,field,source,confidence) VALUES('track','t','title','legacy',1);
		INSERT INTO track_media_info(track_id,status) VALUES('t','ready');
	`); err != nil {
		t.Fatal(err)
	}
	if err := ResetDatabase(database, true); err != nil {
		t.Fatal(err)
	}
	for _, table := range []string{"track_metadata", "track_credits", "track_custom_tags", "metadata_external_ids", "metadata_field_state", "track_media_info", "metadata_field_history"} {
		var count int
		if err := database.QueryRow(`SELECT COUNT(*) FROM ` + table).Scan(&count); err != nil || count != 0 {
			t.Fatalf("%s count=%d err=%v", table, count, err)
		}
	}
}
