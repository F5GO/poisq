# Spec: audio-analysis

Module: `audio-analysis`
Depends on: `metadata-core`, `local-metadata-import`, `staging-writeback`

## Objective

Import existing ReplayGain metadata and provide bounded, reproducible track/album loudness analysis with optional reviewable gain-tag writes.

## Requirements

- Import ReplayGain track/album gain and peak tags from embedded metadata without overwriting calculated LUFS.
- Run `ffmpeg` EBU R128 analysis with fixed arguments, validated paths, cancellation, a ten-minute per-track timeout, bounded stderr, and at most two workers.
- Persist integrated LUFS, loudness range, true peak, source/provenance, analyzer version, fingerprint, status, bounded error, and timestamps.
- Skip unchanged files for the same analyzer version.
- Aggregate album LUFS by duration-weighted linear energy and album peak by maximum linear peak; persist separately by folder.
- Isolate one failed track from a batch.
- Create `audio_gain` Staging proposals containing standard ReplayGain tags; never write gain tags directly.
- Reuse crash-safe writeback and exact Undo for approved gain proposals.

## Commands

```sh
gofmt -w internal/metadata/audio_analysis*.go internal/db/audio_analysis*.go
go test -count=1 ./internal/metadata ./internal/db ./internal/tagger
go test -race -count=1 ./internal/metadata ./internal/db
go test -count=1 ./...
go vet ./...
go build ./cmd/proxy
```

## Success Criteria

1. ReplayGain tag variants parse with strict finite ranges.
2. Analyzer time/output/concurrency/fingerprint bounds have tests.
3. Album aggregation matches the duration-weighted formula.
4. Analysis and imported ReplayGain round-trip through SQLite.
5. Gain writes are Staging proposals and exact Undo remains available.
