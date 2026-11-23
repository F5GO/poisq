# Spec: Expanded tag editing

Module: `expanded-tag-editing`
Initiative index: `CAPABILITY-MAP-music-metadata.md`

## Objective

Complete the metadata editor that is already present in the working tree. Operators must be able to edit the full supported tag set for one track or apply an explicit patch to many selected tracks, inspect the exact diff, and send every file mutation through the existing Staging and crash-recoverable writeback pipeline.

The first release adds BPM, normalized rating, every existing credit role, and bounded custom tags to the normalized model. It does not rename files, add audio formats, or write directly to `navidrome.db`.

## Tech Stack

- Go 1.26.11, standard library, existing `modernc.org/sqlite` dependency.
- Existing `internal/musicmeta`, `internal/db`, `internal/tagger`, `internal/admin`, and embedded vanilla Studio UI.
- Existing FFprobe import and FFmpeg atomic tag writer; no new runtime dependency.

## Commands

```sh
gofmt -w internal/musicmeta internal/db internal/metadata internal/tagger internal/admin
go test -count=1 ./internal/musicmeta ./internal/db ./internal/metadata ./internal/tagger ./internal/admin
go test -race -count=1 ./internal/musicmeta ./internal/db ./internal/tagger ./internal/admin
go test -count=1 ./...
go vet ./...
go build ./cmd/proxy
git diff --check
```

## Project Structure

```text
internal/musicmeta/                 BPM, rating, custom-tag domain and validation
internal/db/metadata_schema.go      Additive migration for normalized custom tags
internal/db/metadata_repository.go  Atomic persistence with legacy preservation
internal/metadata/audio_probe.go    Import supported embedded fields/custom tags
internal/tagger/                    Canonical mapping, verification, Staging patches
internal/admin/                     Single/bulk preview and apply APIs
internal/admin/ui/                  Selection, diff preview, editor and result summary
```

## Domain and API Contract

- `BPM` is an integer in `0..999`; zero means absent.
- `Rating` is normalized to `0..100`; its presence is represented separately so an explicit zero rating is distinguishable from an omitted bulk-patch field.
- Credits support the existing roles `artist`, `album_artist`, `composer`, `lyricist`, `conductor`, `performer`, `producer`, and `remixer`, preserving order and join phrases.
- Custom tags are stored as ordered `key/value` entries. Keys are trimmed, case-insensitively unique, at most 128 bytes, and contain only letters, digits, spaces, `_`, or `-`. Values are at most 2 KiB. At most 128 custom tags are accepted per track.
- Known managed, technical, ReplayGain, lyrics, and MusicBrainz keys cannot be shadowed through custom tags.
- A bulk request contains an explicit patch: omitted fields are preserved; provided empty values clear only that field. The request targets explicit track IDs, deduplicated and bounded to 500 tracks.
- Preview performs all validation and returns per-track before/after changed fields without writing files or the database.
- Apply revalidates the same patch, creates one Staging proposal per affected track, and returns created, unchanged, and failed results. It is not an all-filesystem transaction; one failed track does not roll back successful unrelated tracks.
- Single-track editing uses the same patch builder as bulk editing.
- Applying a proposal continues to use backup, atomic write, post-write verification, reconciliation, and existing whole-operation Undo.

## Code Style

Use typed patches and explicit presence flags rather than untyped maps:

```go
type MetadataPatch struct {
	Title  *string `json:"title,omitempty"`
	BPM    *int    `json:"bpm,omitempty"`
	Rating *int    `json:"rating,omitempty"`
}
```

Validate the entire request before creating proposals. SQL remains parameterized; collections and request bodies remain bounded.

## Testing Strategy

- Domain tests for BPM/rating bounds, credit roles, custom-tag key collisions, duplicates, limits, and defensive copies.
- Migration and repository round trips, including legacy writes preserving new fields.
- FFprobe import and FFmpeg argument/verification tests for every added field.
- Patch tests proving omitted-versus-cleared behavior and no-op detection.
- API tests for preview/apply, duplicate IDs, 500-track limit, partial file failures, path guards, and unknown JSON fields.
- Studio contract tests for selection, diff preview, keyboard operation, responsive layout, and visible per-track failures.
- Focused, race, full, vet, build, and diff checks before completion.

## Boundaries

### Always

- Preserve existing metadata and unknown technical tags unless the patch explicitly changes a managed custom key.
- Route file changes through Staging/writeback and retain the existing backup and FLAC integrity checks.
- Validate and bound IDs, strings, arrays, and custom keys before writes.
- Preserve current API fields and single-track workflows.

### Ask first

- Raising the 500-track bulk limit.
- Adding a tagging dependency or supporting a new audio container.
- Changing rating normalization or renaming files from tags.

### Never

- Write audio files from preview.
- Modify `navidrome.db`.
- Treat a semicolon-delimited credit as proof of separate artist identities when the source does not provide structure.
- Allow custom tags to overwrite protected technical, lyrics, ReplayGain, or managed MusicBrainz keys.

## Success Criteria

1. BPM, rating, all credit roles, genres, and custom tags round-trip through normalized SQLite storage and supported audio files.
2. Existing legacy writes cannot erase the new fields.
3. Studio can edit these fields for one track and apply an explicit patch to up to 500 selected tracks.
4. Preview shows the exact per-field diff and performs no mutation.
5. Apply creates Staging proposals and reports unchanged/failed tracks without bypassing writeback safeguards.
6. Existing tag, staging, lyrics, Navidrome, and Studio behavior remains compatible.
7. Focused, race, full, vet, build, and diff checks pass.

## Open Questions

None.
