# Spec: local-metadata-import

Module: `local-metadata-import`
Depends on: `metadata-core`
Initiative index: `CAPABILITY-MAP-music-metadata.md`

## Objective

Make supported audio files the authoritative local source of embedded music metadata while retaining Navidrome as the inventory and fallback source.

The module must:

- probe FLAC, MP3, M4A/MP4, OGG, and Opus files directly through the installed `ffprobe` executable;
- map embedded tags into `musicmeta.Document`, including ordered credits, genres, partial dates, disc/track totals, and distinct typed external IDs;
- persist bounded technical media facts and embedded artwork/lyrics presence;
- reconcile file metadata over the current Navidrome-derived document without erasing non-empty fallback values when a tag is absent;
- skip unchanged files using a stable path/size/mtime fingerprint;
- scan incrementally with bounded concurrency, cancellation, timeouts, response limits, and per-track error isolation;
- never modify audio, artwork, lyrics, NFO, or Navidrome data;
- preserve existing sync/API behavior if `ffprobe` is missing or one file is malformed.

## Tech Stack

- Existing Go standard library and `internal/musicmeta`/`internal/db` contracts.
- Installed FFmpeg `ffprobe`; no new Go runtime dependency.
- SQLite through the existing metadata repository and one additive versioned migration.

## Commands

```sh
gofmt -w internal/metadata/audio_probe*.go internal/db/media_info*.go cmd/proxy/main.go
go test -count=1 ./internal/metadata ./internal/db ./internal/musicmeta
go test -race -count=1 ./internal/metadata ./internal/db
go test -count=1 ./...
go vet ./...
go build ./cmd/proxy
```

## Project Structure

```text
internal/metadata/audio_probe.go       ffprobe execution, bounded JSON DTOs, tag mapping
internal/metadata/local_importer.go    fingerprinting, reconciliation, bounded scan coordinator
internal/metadata/*_test.go            fake-prober, mapping, cancellation, malformed/oversized data tests
internal/db/media_info.go              technical/fingerprint persistence
internal/db/metadata_schema.go         additive migration for track_media_info
cmd/proxy/main.go                      attach importer to the existing library scanner
```

## Import Contract

`AudioProber` returns a normalized local result rather than provider-specific JSON:

```go
type LocalAudioMetadata struct {
	Document         musicmeta.Document
	Container        string
	AudioCodec       string
	Bitrate          int64
	SampleRate       int
	BitDepth         int
	Channels         int
	DurationMillis   int64
	HasEmbeddedCover bool
	HasEmbeddedLyrics bool
}
```

Production probing executes one `ffprobe` process per changed file with `-v error`, JSON output, format/stream tags, no shell, and a validated local path. Each process has a 10-second timeout and a 4 MiB combined output ceiling. Stderr included in errors is sanitized and bounded to 2 KiB.

The executable name defaults to `ffprobe` and may be configured for tests/deployment; it is never derived from request input.

## Tag Mapping

Tag keys are matched case-insensitively with explicit aliases. The initial mapping includes:

- title, subtitle/version, album, sort title, sort album;
- artist/ARTISTS and album artist/ALBUMARTISTS, composer, lyricist, conductor, performer, producer, remixer;
- genre with safe multi-value splitting;
- date/year and original date with preserved precision;
- track number/total and disc number/total, including `N/T` forms;
- compilation, label/publisher, catalog number, barcode/UPC, ISRC, grouping, comment, copyright;
- MusicBrainz artist, album artist, release, release group, recording, and release-track IDs as distinct types;
- AudioDB artist/album IDs when embedded.

Unknown tags are ignored, not persisted as arbitrary fields. Invalid individual IDs/dates are skipped with a bounded warning while other valid tags from the same file remain importable. Invalid structural metadata or resource-limit violations fail that file only.

Multi-value behavior is deterministic: native ffprobe values are split only by NUL/unit-separator or configured unambiguous separators (`;`); commas, ampersands, slashes, and words such as `feat.` are preserved inside a credit.

Every imported field receives `SourceAudioFile`, provider `ffprobe`, confidence `1.0`, unless it is retained from a fallback source. Existing locked fields are not overwritten; this early lock enforcement protects manual work before the full merge-policy module lands.

## Reconciliation

For each Navidrome inventory track:

1. Validate the file path through the existing `PathGuard` and supported-audio allowlist.
2. Read size and nanosecond mtime without following an out-of-root target.
3. Skip probing when the stored fingerprint and probe schema version match.
4. Load the normalized document (or synthesize it from the legacy row).
5. Overlay non-empty valid local fields unless that field is locked.
6. Persist the document, technical facts, fingerprint, and success state.
7. On failure, retain the last valid document/facts and persist only bounded error status/timestamp.

Disappearance or temporary inaccessibility of a file never clears previously imported metadata.

The existing `SyncFromNavidromeDB` remains successful when probing is unavailable. It reports inventory count as before; importer statistics are exposed separately for later Studio integration.

## Persistence

Add `track_media_info` keyed by `track_id` with cascade deletion:

- container, audio codec, bitrate, sample rate, bit depth, channels, duration milliseconds;
- embedded cover and embedded lyrics booleans;
- file size, file mtime nanoseconds, probe schema version;
- last probe status (`ready`, `failed`, `missing`, `unsupported`), bounded last error, and timestamps.

The document and media-info success state are committed in a coordinated operation. If fully atomic cross-repository persistence would require duplicating SQL, the importer must write the normalized document first and mark the fingerprint ready only after success; an unmarked fingerprint forces safe reprobe after a crash.

## Code Style

```go
func (i *LocalImporter) ImportTrack(ctx context.Context, track db.TrackRecord) (ImportResult, error) {
	fingerprint, err := i.fingerprint(track.FilePath)
	if err != nil {
		return ImportResult{}, fmt.Errorf("fingerprint %q: %w", track.ID, err)
	}
	if i.mediaInfo.IsCurrent(ctx, track.ID, fingerprint, probeSchemaVersion) {
		return ImportResult{Skipped: true}, nil
	}
	// Probe, reconcile, persist document, then mark the fingerprint ready.
}
```

Use explicit DTOs, no reflection-based tag binding, parameterized SQL, bounded slices/maps, and wrapped errors without leaking full paths through public API responses.

## Testing Strategy

- Golden ffprobe JSON for FLAC, MP3, M4A, OGG, and Opus tag variants.
- Case-insensitive aliases and correct typed-ID separation.
- Multi-value credits, genres, `N/T` track/disc numbers, partial dates, compilation parsing.
- Locked fields retain their value and provenance.
- Missing tags retain Navidrome fallback; present tags win.
- Unchanged fingerprint skips the prober; changed size/mtime/version reprobes.
- Timeout, cancellation, missing executable, malformed JSON, oversized output, unsupported file, and vanished file affect one track only.
- Bounded worker concurrency and race-free parallel imports.
- Existing scanner, metadata, DB, tagger, admin, and full suites remain green.

## Boundaries

### Always

- Validate paths and formats before process execution.
- Use fixed ffprobe arguments with the file path as one argument.
- Bound process time, output, errors, concurrency, and tag collections.
- Preserve last valid metadata on any probe failure.
- Respect existing field locks.

### Ask first

- Adding a Go tag-parsing dependency instead of ffprobe.
- Increasing probe concurrency above four or output above 4 MiB.
- Treating ambiguous punctuation as an artist separator.

### Never

- Write to the audio file, Navidrome DB, artwork, lyrics, or NFO.
- Execute a shell or interpolate paths into command strings.
- Clear metadata because ffprobe failed or a file temporarily disappeared.
- Store arbitrary unknown tags or unbounded ffprobe JSON.
- Follow paths outside the configured music root.

## Success Criteria

1. A changed supported audio file produces a normalized document with correct credits, dates, numbers, genres, and typed IDs.
2. Recording and release-track IDs remain separate from probe JSON through SQLite round trip.
3. Technical facts and embedded cover/lyrics presence are persisted with a successful fingerprint.
4. Unchanged files are not reprobed; changed files are.
5. Locked fields and missing-tag fallback values are preserved.
6. One timeout/malformed/missing file cannot abort inventory synchronization or erase prior metadata.
7. Probe execution is bounded by timeout, output, stderr, formats, paths, and worker count.
8. New/legacy databases migrate idempotently and reset cleans media-info rows.
9. Focused, race, full tests, vet, and proxy build pass.
10. No GPL code or new runtime dependency is introduced.

## Open Questions

None. The module deliberately uses the existing FFmpeg runtime instead of adding a tag library; a later approved spec may replace the prober behind the interface without changing import semantics.
