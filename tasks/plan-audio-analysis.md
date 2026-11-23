# Implementation Plan: `audio-analysis`

Source of truth: [`SPEC-audio-analysis.md`](../SPEC-audio-analysis.md).

1. Add analysis schema/repositories and ReplayGain import fields.
2. Implement bounded ffmpeg EBU R128 parser and analyzer boundary.
3. Add fingerprint-aware bounded track batches and album aggregation.
4. Create ReplayGain Staging proposals from calculated results.
5. Run focused, race, full, vet, build, and diff checks.
