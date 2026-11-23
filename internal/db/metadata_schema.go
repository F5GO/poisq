package db

import (
	"context"
	"database/sql"
	"fmt"
	"time"
)

type schemaMigration struct {
	version int
	name    string
	up      string
}

var metadataMigrations = []schemaMigration{
	{
		version: 1,
		name:    "normalized_music_metadata",
		up: `
		CREATE TABLE track_metadata (
			track_id TEXT PRIMARY KEY REFERENCES tracks(id) ON DELETE CASCADE,
			subtitle TEXT NOT NULL DEFAULT '',
			sort_title TEXT NOT NULL DEFAULT '',
			sort_album TEXT NOT NULL DEFAULT '',
			release_date TEXT NOT NULL DEFAULT '',
			original_release_date TEXT NOT NULL DEFAULT '',
			track_total INTEGER NOT NULL DEFAULT 0 CHECK(track_total >= 0),
			disc_number INTEGER NOT NULL DEFAULT 0 CHECK(disc_number >= 0),
			disc_total INTEGER NOT NULL DEFAULT 0 CHECK(disc_total >= 0),
			compilation INTEGER NOT NULL DEFAULT 0 CHECK(compilation IN (0, 1)),
			label TEXT NOT NULL DEFAULT '',
			catalog_number TEXT NOT NULL DEFAULT '',
			barcode TEXT NOT NULL DEFAULT '',
			isrc TEXT NOT NULL DEFAULT '',
			grouping_name TEXT NOT NULL DEFAULT '',
			comment TEXT NOT NULL DEFAULT '',
			copyright TEXT NOT NULL DEFAULT '',
			updated_at INTEGER NOT NULL DEFAULT 0
		);

		CREATE TABLE track_credits (
			track_id TEXT NOT NULL REFERENCES tracks(id) ON DELETE CASCADE,
			role TEXT NOT NULL,
			position INTEGER NOT NULL CHECK(position >= 0),
			name TEXT NOT NULL,
			sort_name TEXT NOT NULL DEFAULT '',
			join_phrase TEXT NOT NULL DEFAULT '',
			PRIMARY KEY(track_id, role, position)
		);
		CREATE INDEX idx_track_credits_name ON track_credits(role, name, track_id);

		CREATE TABLE track_credit_external_ids (
			track_id TEXT NOT NULL,
			role TEXT NOT NULL,
			credit_position INTEGER NOT NULL,
			id_type TEXT NOT NULL,
			id_position INTEGER NOT NULL CHECK(id_position >= 0),
			value TEXT NOT NULL,
			PRIMARY KEY(track_id, role, credit_position, id_type, id_position),
			FOREIGN KEY(track_id, role, credit_position)
				REFERENCES track_credits(track_id, role, position) ON DELETE CASCADE
		);

		CREATE TABLE track_genres (
			track_id TEXT NOT NULL REFERENCES tracks(id) ON DELETE CASCADE,
			position INTEGER NOT NULL CHECK(position >= 0),
			name TEXT NOT NULL,
			PRIMARY KEY(track_id, position)
		);
		CREATE INDEX idx_track_genres_name ON track_genres(name, track_id);

		CREATE TABLE metadata_external_ids (
			scope TEXT NOT NULL,
			scope_id TEXT NOT NULL,
			id_type TEXT NOT NULL,
			position INTEGER NOT NULL CHECK(position >= 0),
			value TEXT NOT NULL,
			PRIMARY KEY(scope, scope_id, id_type, position)
		);
		CREATE INDEX idx_metadata_external_ids_lookup
			ON metadata_external_ids(id_type, value, scope);

		CREATE TABLE metadata_field_state (
			scope TEXT NOT NULL,
			scope_id TEXT NOT NULL,
			field TEXT NOT NULL,
			source TEXT NOT NULL,
			provider TEXT NOT NULL DEFAULT '',
			confidence REAL NOT NULL CHECK(confidence >= 0 AND confidence <= 1),
			locked INTEGER NOT NULL DEFAULT 0 CHECK(locked IN (0, 1)),
			updated_at INTEGER NOT NULL DEFAULT 0,
			PRIMARY KEY(scope, scope_id, field)
		);
		CREATE INDEX idx_metadata_field_state_locked
			ON metadata_field_state(scope, locked, field);

		CREATE TRIGGER cleanup_track_metadata_external_ids
		AFTER DELETE ON tracks BEGIN
			DELETE FROM metadata_external_ids WHERE scope='track' AND scope_id=OLD.id;
		END;
		CREATE TRIGGER cleanup_track_metadata_field_state
		AFTER DELETE ON tracks BEGIN
			DELETE FROM metadata_field_state WHERE scope='track' AND scope_id=OLD.id;
		END;
		`,
	},
	{
		version: 2,
		name:    "local_audio_media_info",
		up: `
		CREATE TABLE track_media_info (
			track_id TEXT PRIMARY KEY REFERENCES tracks(id) ON DELETE CASCADE,
			container TEXT NOT NULL DEFAULT '',
			audio_codec TEXT NOT NULL DEFAULT '',
			bitrate INTEGER NOT NULL DEFAULT 0 CHECK(bitrate >= 0),
			sample_rate INTEGER NOT NULL DEFAULT 0 CHECK(sample_rate >= 0),
			bit_depth INTEGER NOT NULL DEFAULT 0 CHECK(bit_depth >= 0),
			channels INTEGER NOT NULL DEFAULT 0 CHECK(channels >= 0),
			duration_ms INTEGER NOT NULL DEFAULT 0 CHECK(duration_ms >= 0),
			has_embedded_cover INTEGER NOT NULL DEFAULT 0 CHECK(has_embedded_cover IN (0,1)),
			has_embedded_lyrics INTEGER NOT NULL DEFAULT 0 CHECK(has_embedded_lyrics IN (0,1)),
			file_size INTEGER NOT NULL DEFAULT 0 CHECK(file_size >= 0),
			file_mtime_ns INTEGER NOT NULL DEFAULT 0,
			probe_version INTEGER NOT NULL DEFAULT 0,
			status TEXT NOT NULL DEFAULT 'missing',
			last_error TEXT NOT NULL DEFAULT '',
			probed_at INTEGER NOT NULL DEFAULT 0,
			updated_at INTEGER NOT NULL DEFAULT 0
		);
		CREATE INDEX idx_track_media_info_status ON track_media_info(status, updated_at);
		`,
	},
	{
		version: 3,
		name:    "metadata_provider_policy",
		up: `
		CREATE TABLE metadata_provider_policy (
			source TEXT NOT NULL,
			provider TEXT NOT NULL DEFAULT '',
			priority INTEGER NOT NULL CHECK(priority >= 0 AND priority <= 100000),
			refresh_mode TEXT NOT NULL CHECK(refresh_mode IN ('missing_only','priority','newer','never')),
			enabled INTEGER NOT NULL CHECK(enabled IN (0,1)),
			min_confidence REAL NOT NULL CHECK(min_confidence >= 0 AND min_confidence <= 1),
			updated_at INTEGER NOT NULL,
			PRIMARY KEY(source, provider)
		);
		CREATE INDEX idx_metadata_provider_policy_priority
			ON metadata_provider_policy(enabled, priority, source);
		`,
	},
	{
		version: 4,
		name:    "writeback_transactions",
		up: `
		CREATE TABLE writeback_transactions (
			transaction_id TEXT PRIMARY KEY,
			proposal_id INTEGER NOT NULL UNIQUE REFERENCES staging_changes(id) ON DELETE CASCADE,
			kind TEXT NOT NULL CHECK(kind IN ('tags','cover','nfo')),
			target_path TEXT NOT NULL,
			backup_path TEXT NOT NULL DEFAULT '',
			original_exists INTEGER NOT NULL CHECK(original_exists IN (0,1)),
			original_size INTEGER NOT NULL DEFAULT 0 CHECK(original_size >= 0),
			original_sha256 TEXT NOT NULL DEFAULT '',
			original_json TEXT NOT NULL DEFAULT '{}',
			state TEXT NOT NULL CHECK(state IN ('prepared','written','verified','committed','undoing','rolled_back','failed')),
			reconciled INTEGER NOT NULL DEFAULT 0 CHECK(reconciled IN (0,1)),
			last_error TEXT NOT NULL DEFAULT '',
			created_at INTEGER NOT NULL,
			updated_at INTEGER NOT NULL
		);
		CREATE INDEX idx_writeback_transactions_recovery
			ON writeback_transactions(state, reconciled, updated_at);
		`,
	},
	{
		version: 5,
		name:    "audio_loudness_analysis",
		up: `
		CREATE TABLE track_audio_analysis (
			track_id TEXT PRIMARY KEY REFERENCES tracks(id) ON DELETE CASCADE,
			replaygain_track_gain_db REAL,
			replaygain_track_peak REAL,
			replaygain_album_gain_db REAL,
			replaygain_album_peak REAL,
			integrated_lufs REAL,
			loudness_range_lu REAL,
			true_peak_db REAL,
			file_size INTEGER NOT NULL DEFAULT 0 CHECK(file_size >= 0),
			file_mtime_ns INTEGER NOT NULL DEFAULT 0,
			analyzer_version INTEGER NOT NULL DEFAULT 0,
			source TEXT NOT NULL DEFAULT '',
			status TEXT NOT NULL DEFAULT 'pending' CHECK(status IN ('pending','ready','failed','missing','unsupported')),
			last_error TEXT NOT NULL DEFAULT '',
			analyzed_at INTEGER NOT NULL DEFAULT 0,
			updated_at INTEGER NOT NULL DEFAULT 0
		);
		CREATE INDEX idx_track_audio_analysis_status ON track_audio_analysis(status, updated_at);

		CREATE TABLE album_audio_analysis (
			folder_path TEXT PRIMARY KEY,
			integrated_lufs REAL NOT NULL,
			true_peak_db REAL NOT NULL,
			track_count INTEGER NOT NULL CHECK(track_count > 0),
			source TEXT NOT NULL,
			updated_at INTEGER NOT NULL
		);
		`,
	},
	{
		version: 6,
		name:    "media_enrichment",
		up: `
		CREATE TABLE media_artwork (
			scope TEXT NOT NULL CHECK(scope IN ('album','artist')),
			scope_id TEXT NOT NULL,
			role TEXT NOT NULL CHECK(role IN ('album_primary','artist_primary','disc','logo','banner','backdrop')),
			file_path TEXT NOT NULL,
			source TEXT NOT NULL,
			width INTEGER NOT NULL CHECK(width > 0),
			height INTEGER NOT NULL CHECK(height > 0),
			score INTEGER NOT NULL,
			updated_at INTEGER NOT NULL,
			PRIMARY KEY(scope,scope_id,role)
		);
		CREATE INDEX idx_media_artwork_role ON media_artwork(role,updated_at);

		CREATE TABLE similar_artists (
			reference_mbid TEXT NOT NULL,
			similar_mbid TEXT NOT NULL,
			name TEXT NOT NULL,
			score REAL NOT NULL CHECK(score >= 0 AND score <= 1),
			listen_count INTEGER NOT NULL DEFAULT 0 CHECK(listen_count >= 0),
			provider TEXT NOT NULL DEFAULT 'listenbrainz',
			fetched_at INTEGER NOT NULL,
			PRIMARY KEY(reference_mbid,similar_mbid)
		);
		CREATE INDEX idx_similar_artists_rank ON similar_artists(reference_mbid,score DESC,listen_count DESC);
		`,
	},
	{
		version: 7,
		name:    "expanded_track_tags",
		up: `
		ALTER TABLE track_metadata ADD COLUMN bpm INTEGER NOT NULL DEFAULT 0 CHECK(bpm >= 0 AND bpm <= 999);
		ALTER TABLE track_metadata ADD COLUMN rating INTEGER CHECK(rating IS NULL OR (rating >= 0 AND rating <= 100));
		CREATE TABLE track_custom_tags (
			track_id TEXT NOT NULL REFERENCES tracks(id) ON DELETE CASCADE,
			position INTEGER NOT NULL CHECK(position >= 0),
			key TEXT NOT NULL,
			value TEXT NOT NULL,
			PRIMARY KEY(track_id, position)
		);
		CREATE UNIQUE INDEX idx_track_custom_tags_key
			ON track_custom_tags(track_id, lower(key));
		`,
	},
	{
		version: 8,
		name:    "metadata_field_history",
		up: `
		CREATE TABLE metadata_field_history (
			id INTEGER PRIMARY KEY AUTOINCREMENT,
			track_id TEXT NOT NULL REFERENCES tracks(id) ON DELETE CASCADE,
			proposal_id INTEGER NOT NULL REFERENCES staging_changes(id) ON DELETE CASCADE,
			writeback_transaction_id TEXT NOT NULL,
			field TEXT NOT NULL,
			old_value_json TEXT NOT NULL,
			new_value_json TEXT NOT NULL,
			source TEXT NOT NULL,
			provider TEXT NOT NULL DEFAULT '',
			actor TEXT NOT NULL DEFAULT 'system',
			created_at INTEGER NOT NULL,
			reverted_by_proposal_id INTEGER REFERENCES staging_changes(id),
			UNIQUE(proposal_id, field)
		);
		CREATE INDEX idx_metadata_field_history_track
			ON metadata_field_history(track_id, created_at DESC, id DESC);
		CREATE INDEX idx_metadata_field_history_field
			ON metadata_field_history(field, created_at DESC, id DESC);
		`,
	},
	{
		version: 9,
		name:    "metadata_history_revert_links",
		up: `
		CREATE TABLE metadata_history_revert_links (
			history_id INTEGER PRIMARY KEY REFERENCES metadata_field_history(id) ON DELETE CASCADE,
			proposal_id INTEGER NOT NULL REFERENCES staging_changes(id) ON DELETE CASCADE
		);
		CREATE INDEX idx_metadata_history_revert_proposal ON metadata_history_revert_links(proposal_id);
		`,
	},
}

func applyMetadataMigrations(ctx context.Context, database *sql.DB) error {
	if _, err := database.ExecContext(ctx, `
		CREATE TABLE IF NOT EXISTS schema_migrations (
			version INTEGER PRIMARY KEY,
			name TEXT NOT NULL,
			applied_at INTEGER NOT NULL
		)`); err != nil {
		return fmt.Errorf("create schema migrations table: %w", err)
	}
	return applySchemaMigrations(ctx, database, metadataMigrations)
}

func applySchemaMigrations(ctx context.Context, database *sql.DB, migrations []schemaMigration) error {
	for _, migration := range migrations {
		var exists int
		err := database.QueryRowContext(ctx, `SELECT 1 FROM schema_migrations WHERE version = ?`, migration.version).Scan(&exists)
		if err == nil {
			continue
		}
		if err != sql.ErrNoRows {
			return fmt.Errorf("check schema migration %d: %w", migration.version, err)
		}
		tx, err := database.BeginTx(ctx, nil)
		if err != nil {
			return fmt.Errorf("begin schema migration %d: %w", migration.version, err)
		}
		if _, err = tx.ExecContext(ctx, migration.up); err != nil {
			_ = tx.Rollback()
			return fmt.Errorf("apply schema migration %d (%s): %w", migration.version, migration.name, err)
		}
		if _, err = tx.ExecContext(ctx,
			`INSERT INTO schema_migrations(version, name, applied_at) VALUES (?, ?, ?)`,
			migration.version, migration.name, time.Now().Unix()); err != nil {
			_ = tx.Rollback()
			return fmt.Errorf("record schema migration %d: %w", migration.version, err)
		}
		if err = tx.Commit(); err != nil {
			return fmt.Errorf("commit schema migration %d: %w", migration.version, err)
		}
	}
	return nil
}
