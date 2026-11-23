package db

import (
	"context"
	"database/sql"
	"fmt"
	"time"

	"github.com/F5GO/poisq/internal/musicmeta"
)

// MetadataRepository persists normalized metadata beside the legacy tracks projection.
type MetadataRepository struct {
	db *sql.DB
}

type metadataQuerier interface {
	QueryContext(context.Context, string, ...any) (*sql.Rows, error)
	QueryRowContext(context.Context, string, ...any) *sql.Row
}

func NewMetadataRepository(database *sql.DB) *MetadataRepository {
	return &MetadataRepository{db: database}
}

func (r *MetadataRepository) SetFieldLock(ctx context.Context, trackID string, field musicmeta.Field, locked bool) error {
	doc, err := r.GetTrackDocument(ctx, trackID)
	if err != nil {
		return err
	}
	state, ok := doc.FieldStates[field]
	if !ok {
		state = musicmeta.FieldState{Source: musicmeta.SourceManual, Confidence: 1}
	}
	state.Locked = locked
	state.UpdatedAt = time.Now().UTC()
	doc.FieldStates[field] = state
	if _, err := musicmeta.ValidateDocument(doc); err != nil {
		return fmt.Errorf("validate metadata field lock: %w", err)
	}
	return r.PutTrackDocument(ctx, doc)
}

func (r *MetadataRepository) PutTrackDocument(ctx context.Context, input musicmeta.Document) error {
	doc, err := musicmeta.ValidateDocument(input)
	if err != nil {
		return fmt.Errorf("validate track metadata %q: %w", input.TrackID, err)
	}
	tx, err := r.db.BeginTx(ctx, nil)
	if err != nil {
		return fmt.Errorf("begin track metadata write: %w", err)
	}
	defer tx.Rollback()

	legacy := musicmeta.ToLegacy(doc)
	result, err := tx.ExecContext(ctx, `
		UPDATE tracks SET artist=?, album=?, album_artist=?, title=?, track_number=?, year=?, updated_at=?
		WHERE id=?`, legacy.Artist, legacy.Album, legacy.AlbumArtist, legacy.Title, legacy.TrackNumber, legacy.Year, time.Now().Unix(), doc.TrackID)
	if err != nil {
		return fmt.Errorf("update legacy track metadata: %w", err)
	}
	if affected, _ := result.RowsAffected(); affected != 1 {
		return fmt.Errorf("track %q not found", doc.TrackID)
	}

	if _, err := tx.ExecContext(ctx, `
		INSERT INTO track_metadata(
			track_id,subtitle,sort_title,sort_album,release_date,original_release_date,
			track_total,disc_number,disc_total,compilation,label,catalog_number,barcode,isrc,
			grouping_name,comment,copyright,bpm,rating,updated_at
		) VALUES(?,?,?,?,?,?,?,?,?,?,?,?,?,?,?,?,?,?,?,?)
		ON CONFLICT(track_id) DO UPDATE SET
			subtitle=excluded.subtitle,sort_title=excluded.sort_title,sort_album=excluded.sort_album,
			release_date=excluded.release_date,original_release_date=excluded.original_release_date,
			track_total=excluded.track_total,disc_number=excluded.disc_number,disc_total=excluded.disc_total,
			compilation=excluded.compilation,label=excluded.label,catalog_number=excluded.catalog_number,
			barcode=excluded.barcode,isrc=excluded.isrc,grouping_name=excluded.grouping_name,
			comment=excluded.comment,copyright=excluded.copyright,bpm=excluded.bpm,rating=excluded.rating,updated_at=excluded.updated_at`,
		doc.TrackID, doc.Subtitle, doc.SortTitle, doc.SortAlbum, doc.ReleaseDate.Value, doc.OriginalReleaseDate.Value,
		doc.TrackTotal, doc.DiscNumber, doc.DiscTotal, boolInt(doc.Compilation), doc.Label, doc.CatalogNumber,
		doc.Barcode, doc.ISRC, doc.Grouping, doc.Comment, doc.Copyright, doc.BPM, doc.Rating, time.Now().Unix()); err != nil {
		return fmt.Errorf("write track metadata extension: %w", err)
	}

	for _, table := range []string{"track_credit_external_ids", "track_credits", "track_genres", "track_custom_tags"} {
		if _, err := tx.ExecContext(ctx, `DELETE FROM `+table+` WHERE track_id=?`, doc.TrackID); err != nil {
			return fmt.Errorf("clear %s: %w", table, err)
		}
	}
	if _, err := tx.ExecContext(ctx, `DELETE FROM metadata_external_ids WHERE scope='track' AND scope_id=?`, doc.TrackID); err != nil {
		return fmt.Errorf("clear track external ids: %w", err)
	}
	if _, err := tx.ExecContext(ctx, `DELETE FROM metadata_field_state WHERE scope='track' AND scope_id=?`, doc.TrackID); err != nil {
		return fmt.Errorf("clear track field states: %w", err)
	}

	for position, credit := range doc.Credits {
		if _, err := tx.ExecContext(ctx, `INSERT INTO track_credits(track_id,role,position,name,sort_name,join_phrase) VALUES(?,?,?,?,?,?)`,
			doc.TrackID, credit.Role, position, credit.Name, credit.SortName, credit.JoinPhrase); err != nil {
			return fmt.Errorf("insert track credit: %w", err)
		}
		for idPosition, externalID := range credit.ExternalIDs {
			if _, err := tx.ExecContext(ctx, `
				INSERT INTO track_credit_external_ids(track_id,role,credit_position,id_type,id_position,value)
				VALUES(?,?,?,?,?,?)`, doc.TrackID, credit.Role, position, externalID.Type, idPosition, externalID.Value); err != nil {
				return fmt.Errorf("insert credit external id: %w", err)
			}
		}
	}
	for position, genre := range doc.Genres {
		if _, err := tx.ExecContext(ctx, `INSERT INTO track_genres(track_id,position,name) VALUES(?,?,?)`, doc.TrackID, position, genre); err != nil {
			return fmt.Errorf("insert track genre: %w", err)
		}
	}
	for position, value := range doc.CustomTags {
		if _, err := tx.ExecContext(ctx, `INSERT INTO track_custom_tags(track_id,position,key,value) VALUES(?,?,?,?)`, doc.TrackID, position, value.Key, value.Value); err != nil {
			return fmt.Errorf("insert track custom tag: %w", err)
		}
	}
	for position, externalID := range doc.ExternalIDs {
		if _, err := tx.ExecContext(ctx, `
			INSERT INTO metadata_external_ids(scope,scope_id,id_type,position,value) VALUES('track',?,?,?,?)`,
			doc.TrackID, externalID.Type, position, externalID.Value); err != nil {
			return fmt.Errorf("insert track external id: %w", err)
		}
	}
	for field, state := range doc.FieldStates {
		updatedAt := int64(0)
		if !state.UpdatedAt.IsZero() {
			updatedAt = state.UpdatedAt.Unix()
		}
		if _, err := tx.ExecContext(ctx, `
			INSERT INTO metadata_field_state(scope,scope_id,field,source,provider,confidence,locked,updated_at)
			VALUES('track',?,?,?,?,?,?,?)`, doc.TrackID, field, state.Source, state.Provider, state.Confidence,
			boolInt(state.Locked), updatedAt); err != nil {
			return fmt.Errorf("insert track field state: %w", err)
		}
	}
	if err := tx.Commit(); err != nil {
		return fmt.Errorf("commit track metadata: %w", err)
	}
	return nil
}

func (r *MetadataRepository) GetTrackDocument(ctx context.Context, trackID string) (musicmeta.Document, error) {
	tx, err := r.db.BeginTx(ctx, &sql.TxOptions{ReadOnly: true})
	if err != nil {
		return musicmeta.Document{}, fmt.Errorf("begin track metadata read: %w", err)
	}
	defer tx.Rollback()
	var legacy musicmeta.LegacyTrack
	legacy.TrackID = trackID
	if err := tx.QueryRowContext(ctx, `SELECT artist,album,album_artist,title,year,track_number FROM tracks WHERE id=?`, trackID).
		Scan(&legacy.Artist, &legacy.Album, &legacy.AlbumArtist, &legacy.Title, &legacy.Year, &legacy.TrackNumber); err != nil {
		return musicmeta.Document{}, fmt.Errorf("read legacy track metadata %q: %w", trackID, err)
	}
	doc := musicmeta.DocumentFromLegacy(legacy)
	var compilation int
	err = tx.QueryRowContext(ctx, `
		SELECT subtitle,sort_title,sort_album,release_date,original_release_date,track_total,
		       disc_number,disc_total,compilation,label,catalog_number,barcode,isrc,grouping_name,comment,copyright,bpm,rating
		FROM track_metadata WHERE track_id=?`, trackID).Scan(
		&doc.Subtitle, &doc.SortTitle, &doc.SortAlbum, &doc.ReleaseDate.Value, &doc.OriginalReleaseDate.Value,
		&doc.TrackTotal, &doc.DiscNumber, &doc.DiscTotal, &compilation, &doc.Label, &doc.CatalogNumber,
		&doc.Barcode, &doc.ISRC, &doc.Grouping, &doc.Comment, &doc.Copyright, &doc.BPM, &doc.Rating)
	if err != nil && err != sql.ErrNoRows {
		return musicmeta.Document{}, fmt.Errorf("read track metadata extension: %w", err)
	}
	if err == nil {
		doc.Compilation = compilation == 1
		doc.FieldStates = make(map[musicmeta.Field]musicmeta.FieldState)
		if parsed, parseErr := musicmeta.ParsePartialDate(doc.ReleaseDate.Value); parseErr == nil {
			doc.ReleaseDate = parsed
		}
		if parsed, parseErr := musicmeta.ParsePartialDate(doc.OriginalReleaseDate.Value); parseErr == nil {
			doc.OriginalReleaseDate = parsed
		}
	}

	credits, err := r.readCredits(ctx, tx, trackID)
	if err != nil {
		return musicmeta.Document{}, err
	}
	if len(credits) > 0 {
		doc.Credits = credits
	}
	if doc.Genres, err = r.readGenres(ctx, tx, trackID); err != nil {
		return musicmeta.Document{}, err
	}
	if doc.ExternalIDs, err = r.readExternalIDs(ctx, tx, trackID); err != nil {
		return musicmeta.Document{}, err
	}
	if doc.CustomTags, err = r.readCustomTags(ctx, tx, trackID); err != nil {
		return musicmeta.Document{}, err
	}
	states, err := r.readFieldStates(ctx, tx, trackID)
	if err != nil {
		return musicmeta.Document{}, err
	}
	for field, state := range states {
		doc.FieldStates[field] = state
	}
	if err := tx.Commit(); err != nil {
		return musicmeta.Document{}, fmt.Errorf("commit track metadata read: %w", err)
	}
	return doc.Clone(), nil
}

func (r *MetadataRepository) readCustomTags(ctx context.Context, query metadataQuerier, trackID string) ([]musicmeta.CustomTag, error) {
	rows, err := query.QueryContext(ctx, `SELECT key,value FROM track_custom_tags WHERE track_id=? ORDER BY position`, trackID)
	if err != nil {
		return nil, fmt.Errorf("read track custom tags: %w", err)
	}
	defer rows.Close()
	var values []musicmeta.CustomTag
	for rows.Next() {
		var value musicmeta.CustomTag
		if err := rows.Scan(&value.Key, &value.Value); err != nil {
			return nil, err
		}
		values = append(values, value)
	}
	return values, rows.Err()
}

func (r *MetadataRepository) readCredits(ctx context.Context, query metadataQuerier, trackID string) ([]musicmeta.Credit, error) {
	rows, err := query.QueryContext(ctx, `SELECT role,position,name,sort_name,join_phrase FROM track_credits WHERE track_id=? ORDER BY position`, trackID)
	if err != nil {
		return nil, fmt.Errorf("read track credits: %w", err)
	}
	defer rows.Close()
	var credits []musicmeta.Credit
	positions := make([]int, 0)
	for rows.Next() {
		var credit musicmeta.Credit
		var position int
		if err := rows.Scan(&credit.Role, &position, &credit.Name, &credit.SortName, &credit.JoinPhrase); err != nil {
			return nil, fmt.Errorf("scan track credit: %w", err)
		}
		credits = append(credits, credit)
		positions = append(positions, position)
	}
	if err := rows.Err(); err != nil {
		return nil, err
	}
	if err := rows.Close(); err != nil {
		return nil, err
	}
	for index := range credits {
		idRows, err := query.QueryContext(ctx, `
			SELECT id_type,value FROM track_credit_external_ids
			WHERE track_id=? AND role=? AND credit_position=? ORDER BY id_position`, trackID, credits[index].Role, positions[index])
		if err != nil {
			return nil, fmt.Errorf("read credit external ids: %w", err)
		}
		for idRows.Next() {
			var externalID musicmeta.ExternalID
			if err := idRows.Scan(&externalID.Type, &externalID.Value); err != nil {
				idRows.Close()
				return nil, err
			}
			credits[index].ExternalIDs = append(credits[index].ExternalIDs, externalID)
		}
		if err := idRows.Close(); err != nil {
			return nil, err
		}
	}
	return credits, nil
}

func (r *MetadataRepository) readGenres(ctx context.Context, query metadataQuerier, trackID string) ([]string, error) {
	rows, err := query.QueryContext(ctx, `SELECT name FROM track_genres WHERE track_id=? ORDER BY position`, trackID)
	if err != nil {
		return nil, fmt.Errorf("read track genres: %w", err)
	}
	defer rows.Close()
	var values []string
	for rows.Next() {
		var value string
		if err := rows.Scan(&value); err != nil {
			return nil, err
		}
		values = append(values, value)
	}
	return values, rows.Err()
}

func (r *MetadataRepository) readExternalIDs(ctx context.Context, query metadataQuerier, trackID string) (musicmeta.ExternalIDs, error) {
	rows, err := query.QueryContext(ctx, `
		SELECT id_type,value FROM metadata_external_ids
		WHERE scope='track' AND scope_id=? ORDER BY position`, trackID)
	if err != nil {
		return nil, fmt.Errorf("read track external ids: %w", err)
	}
	defer rows.Close()
	var values musicmeta.ExternalIDs
	for rows.Next() {
		var value musicmeta.ExternalID
		if err := rows.Scan(&value.Type, &value.Value); err != nil {
			return nil, err
		}
		values = append(values, value)
	}
	return values, rows.Err()
}

func (r *MetadataRepository) readFieldStates(ctx context.Context, query metadataQuerier, trackID string) (map[musicmeta.Field]musicmeta.FieldState, error) {
	rows, err := query.QueryContext(ctx, `
		SELECT field,source,provider,confidence,locked,updated_at FROM metadata_field_state
		WHERE scope='track' AND scope_id=?`, trackID)
	if err != nil {
		return nil, fmt.Errorf("read track field states: %w", err)
	}
	defer rows.Close()
	values := make(map[musicmeta.Field]musicmeta.FieldState)
	for rows.Next() {
		var field musicmeta.Field
		var state musicmeta.FieldState
		var locked int
		var updatedAt int64
		if err := rows.Scan(&field, &state.Source, &state.Provider, &state.Confidence, &locked, &updatedAt); err != nil {
			return nil, err
		}
		state.Locked = locked == 1
		if updatedAt != 0 {
			state.UpdatedAt = time.Unix(updatedAt, 0).UTC()
		}
		values[field] = state
	}
	return values, rows.Err()
}

func boolInt(value bool) int {
	if value {
		return 1
	}
	return 0
}
