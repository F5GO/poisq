# Spec: metadata-core

Module: `metadata-core`
Initiative index: `CAPABILITY-MAP-music-metadata.md`

## Objective

Introduce one normalized, validated music-metadata domain model and persistence contract that every later metadata module can share.

The module must:

- represent track identity and descriptive metadata without collapsing multi-value credits into one string;
- keep MusicBrainz artist, album-artist, release, release-group, recording, and release-track identifiers distinct;
- represent partial release dates without inventing missing month/day values;
- record the selected source and confidence for every mutable field;
- support explicit per-field locks;
- migrate existing `poisq.db` files without losing or rewriting current track, staging, lyrics, biography, job, key, or settings data;
- preserve the existing `TrackRecord`, `CanonicalTagSet`, Studio API, and JSON contracts while downstream modules are migrated;
- contain no code copied from Jellyfin or any other GPL-licensed project.

Success means later import, provider, NFO, analysis, writeback, and Studio modules can exchange one `musicmeta.Document` rather than inventing provider-specific structures.

## Tech Stack

- Go version from the repository `go.mod`.
- SQLite through the existing `modernc.org/sqlite` dependency.
- Standard-library JSON, time, Unicode, and validation facilities.
- Existing `internal/db` transaction and repository patterns.
- No new runtime dependency in this module.

## Commands

Format:

```sh
gofmt -w internal/musicmeta/*.go internal/db/metadata_*.go internal/db/poisq_db.go
```

Focused tests:

```sh
go test -count=1 ./internal/musicmeta ./internal/db ./internal/tagger
go test -race -count=1 ./internal/musicmeta ./internal/db
```

Full applicable suite:

```sh
go test -count=1 ./...
```

Static checks:

```sh
go vet ./internal/musicmeta ./internal/db ./internal/tagger
```

## Project Structure

```text
internal/musicmeta/
  types.go          Normalized document, credits, dates, external IDs, provenance, locks
  validate.go       Bounds, enum validation, canonicalization, duplicate handling
  legacy.go         Loss-aware adapters to/from current TrackRecord and CanonicalTagSet shapes
  *_test.go         Domain, validation, and compatibility tests

internal/db/
  metadata_schema.go      Versioned, transactional poisq.db schema migrations
  metadata_repository.go  Atomic normalized metadata and field-state persistence
  metadata_*_test.go      Empty/legacy/current DB migration and repository tests
  poisq_db.go             Calls the versioned migration runner after base bootstrap

internal/tagger/
  picard_mapper.go        Retains the public CanonicalTagSet contract; delegates ID semantics to musicmeta adapters
```

Existing source files remain authoritative for their current behavior until their callers are migrated. The new module must not move unrelated lyrics, biography, cover, or admin code.

## Domain Model

### Document

`musicmeta.Document` represents the effective normalized metadata of one track:

```go
type Document struct {
	TrackID             string
	Title               string
	Subtitle            string
	Album               string
	SortTitle           string
	SortAlbum           string
	ReleaseDate         PartialDate
	OriginalReleaseDate PartialDate
	TrackNumber         int
	TrackTotal          int
	DiscNumber          int
	DiscTotal           int
	Compilation         bool
	Label               string
	CatalogNumber       string
	Barcode              string
	ISRC                 string
	Grouping             string
	Comment              string
	Copyright            string
	Credits              []Credit
	Genres               []string
	ExternalIDs          ExternalIDs
	FieldStates          map[Field]FieldState
}
```

The model must distinguish at least these credit roles:

- `artist`
- `album_artist`
- `composer`
- `lyricist`
- `conductor`
- `performer`
- `producer`
- `remixer`

Each credit contains `Name`, optional `SortName`, optional `JoinPhrase`, stable position, and optional typed artist external IDs. Ordering and join phrases must survive a database round trip.

### Partial dates

`PartialDate` accepts only valid canonical forms:

- `YYYY`
- `YYYY-MM`
- `YYYY-MM-DD`

It exposes precision explicitly. It must reject invalid calendar dates, year zero, years above 9999, whitespace, and alternative separators. Conversion to the legacy integer `Year` uses only the year component and never manufactures January 1.

### Typed external IDs

The initial closed ID-type registry is:

- `musicbrainz_artist`
- `musicbrainz_album_artist`
- `musicbrainz_release`
- `musicbrainz_release_group`
- `musicbrainz_recording`
- `musicbrainz_release_track`
- `isrc`
- `barcode`
- `audiodb_artist`
- `audiodb_album`
- `listenbrainz_artist`

MusicBrainz entity IDs must be canonical UUIDs. ISRC and barcode values receive format-specific validation. Other registered values are trimmed, bounded, and reject control characters. Unknown ID types are rejected until deliberately added to the registry.

The model must never map `musicbrainz_recording` to `musicbrainz_release_track`, or vice versa.

### Provenance and locks

Every mutable field has one selected `FieldState`:

```go
type FieldState struct {
	Source     Source
	Provider   string
	Confidence float64
	Locked     bool
	UpdatedAt  time.Time
}
```

The source registry initially includes `legacy`, `audio_file`, `navidrome`, `nfo`, `musicbrainz`, `manual`, and `derived`. Confidence is finite and in `[0,1]`. Provider is optional for local/manual values and otherwise bounded.

Only registered `Field` values may be persisted or locked. Composite fields include `credits`, `genres`, and `external_ids`; scalar fields use their domain names. A lock does not itself change a value. Lock enforcement during merging belongs to `metadata-merge-policy`, but this module owns storage and retrieval of the state.

## Persistence Contract

The migration introduces a `schema_migrations` table and applies each numbered migration exactly once inside a transaction. Reopening a current database is idempotent.

Normalized metadata is stored without deleting or repurposing existing `tracks` columns:

```text
track_metadata
  track_id PK/FK -> tracks(id) ON DELETE CASCADE
  scalar normalized fields not already represented losslessly in tracks

track_credits
  track_id FK, role, position, name, sort_name, join_phrase
  PK(track_id, role, position)

track_genres
  track_id FK, position, name
  PK(track_id, position)

metadata_external_ids
  scope, scope_id, id_type, value, position
  UNIQUE(scope, scope_id, id_type, position)

metadata_field_state
  scope, scope_id, field, source, provider, confidence, locked, updated_at
  PK(scope, scope_id, field)
```

For this module, persisted scope is at least `track`. The schema accepts `album` and `artist` scopes for later modules, but the repository must not create orphan scopes without a known stable entity ID.

Repository writes of a `Document` and its field states are atomic. Replacement of credits, genres, or external IDs uses one transaction and cannot expose partially replaced collections to readers.

Deleting a track cascades all normalized track metadata. Resetting poisq must clear the new tables while preserving the existing `keepAPIKeys` behavior.

## Legacy Compatibility

- Existing `tracks.artist` is the ordered display string derived from `artist` credits.
- Existing `tracks.album_artist` is the ordered display string derived from `album_artist` credits.
- Existing scalar columns (`title`, `album`, `track_number`, `year`) remain populated for current queries and APIs.
- Reading a legacy database lazily materializes a normalized document with provenance `legacy`; it must not require an eager rewrite of every row during startup.
- Writing a normalized document updates both the normalized tables and compatible legacy columns in one transaction.
- Writing through an old `TrackRecord` path updates only fields represented by that legacy type and must preserve disc data, additional credits, genres, typed IDs, dates beyond year precision, provenance, and locks.
- `CanonicalTagSet` remains source-compatible during this module. Its adapters may expose new optional ID fields, but existing JSON names and behavior remain accepted.
- No existing API response loses or renames a field in this module.

## Validation and Resource Bounds

- Track and scope IDs: non-empty, at most 512 bytes, no control characters.
- Titles, album names, credit names, sort names, labels, grouping, and copyright: at most 2 KiB each.
- Comments: at most 16 KiB.
- Provider names, catalog numbers, ISRC, and barcode values: at most 512 bytes unless a stricter format applies.
- At most 256 credits, 128 genres, and 64 external IDs per document.
- Duplicate genres are removed case-insensitively while preserving first-seen spelling and order.
- Duplicate credits are removed only when role, normalized name, join phrase, and typed IDs are equal; distinct roles and order remain intact.
- All text must be valid UTF-8, trimmed at its boundaries, and free of NUL/control characters other than permitted whitespace in comments.
- Validation failure aborts the entire repository write.

## Code Style

Use small value types, explicit validation, defensive copies for slices/maps, parameterized SQL, and wrapped errors with stable operation context:

```go
func (r *MetadataRepository) PutTrackDocument(ctx context.Context, doc musicmeta.Document) error {
	clean, err := musicmeta.ValidateDocument(doc)
	if err != nil {
		return fmt.Errorf("validate track metadata %q: %w", doc.TrackID, err)
	}
	return r.withTx(ctx, func(tx *sql.Tx) error {
		return r.replaceTrackDocument(ctx, tx, clean)
	})
}
```

Names use established Go conventions. Exported declarations have concise contract comments. SQL identifiers are constants owned by the repository; external input is never interpolated into SQL.

## Testing Strategy

### Domain unit tests

- partial-date precision and invalid calendar boundaries;
- each external ID type, including recording/release-track separation;
- credit ordering, roles, join phrases, duplicate rules, and bounds;
- genre normalization and stable order;
- invalid UTF-8, NULs, oversized fields, NaN/infinite confidence, and collection limits;
- defensive-copy behavior so callers cannot mutate stored documents through returned slices/maps;
- loss-aware legacy conversions in both directions.

### Database tests

- opening a brand-new database creates the complete current schema;
- opening a legacy fixture containing current production tables preserves every row and value;
- migration reruns are idempotent;
- a failed migration rolls back without advancing `schema_migrations`;
- normalized write/read round trips every field and state;
- legacy writes preserve normalized-only fields;
- collection replacement is atomic under injected failure;
- track deletion and database reset clean related rows;
- concurrent readers never observe half-replaced credits/genres/IDs;
- all queries use parameters and bounded result sets.

### Compatibility tests

- current track/staging/admin JSON fixtures remain unchanged;
- current tagger tests compile and pass;
- existing `internal/db`, `metadata`, `tagger`, `admin`, `artistbio`, and proxy tests continue to pass.

## Boundaries

### Always

- Preserve existing user data and backward-compatible API fields.
- Run migration and rollback tests against real temporary SQLite files.
- Keep schema changes additive in this module.
- Validate before beginning a write transaction where possible.
- Store typed IDs without semantic aliasing.
- Use parameterized SQL and bounded collections.

### Ask first

- Adding a third-party tag-reading or metadata dependency; that belongs to `local-metadata-import` and requires a separate decision.
- Removing or renaming an existing database column or JSON field.
- Changing public HTTP response semantics.
- Introducing eager whole-library data rewrites during startup.

### Never

- Copy Jellyfin GPL source code.
- Rewrite audio files, artwork, NFO, lyrics, or Navidrome data in this module.
- Automatically unlock a field.
- Treat confidence as proof of identity.
- Store arbitrary unregistered field names or external-ID types.
- Drop malformed legacy data silently; retain the legacy value and return/report a bounded compatibility warning.

## Success Criteria

1. A legacy production-shaped `poisq.db` opens and migrates without losing or changing existing rows.
2. Reopening the database applies no duplicate migration and produces the same schema version.
3. `musicmeta.Document` round-trips all specified scalar fields, ordered credits, genres, typed IDs, provenance, confidence, and locks.
4. MusicBrainz recording and release-track IDs have separate types, storage rows, adapters, and tests.
5. Partial dates retain year/month/day precision across validation, storage, and legacy conversion.
6. An existing `TrackRecord` write cannot erase normalized-only metadata.
7. Existing Studio/API JSON contracts and existing Go call sites compile without mandatory caller changes.
8. Invalid or oversized input cannot partially update normalized or legacy metadata.
9. Focused tests and the full applicable Go suite pass under the race detector where specified.
10. No new runtime dependency or GPL-derived source is introduced.

## Open Questions

None. Later modules will decide the concrete audio tag-reading dependency, provider ranking defaults, NFO dialect details, LUFS execution policy, and Studio presentation without changing this core contract.
