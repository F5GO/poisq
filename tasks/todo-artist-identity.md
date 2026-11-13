# Tasks: `artist-identity`

- [x] Task 1: Define bounded identity types, normalization, and deterministic scoring.
  - Acceptance:
    - Context, candidate, evidence, and result types enforce all count/byte limits from the spec.
    - Normalization handles Unicode, whitespace, case, Cyrillic/Latin aliases, releases, years, and ISRCs without changing display values.
    - Exact stable IDs win without AI.
    - Name-only matches can never return `resolved`.
    - Release/ISRC contradictions lower or block resolution deterministically.
  - Verify: `go test -count=1 -run 'TestIdentity_(Bounds|Normalize|Score|Contradictions)' ./internal/artistbio`
  - Files: `internal/artistbio/identity.go`, `internal/artistbio/identity_test.go`

- [x] Task 2: Generalize and harden the existing AI artist judge.
  - Acceptance:
    - Existing lyrics `SameArtist` API and behavior remain compatible.
    - Identity arbitration accepts only supplied candidate IDs and evidence codes.
    - Prompt data is JSON-encoded and bounded; untrusted values never enter the instruction role.
    - Fenced/trailing/malformed/oversized output, unknown IDs, invalid confidence, and unsupported decisions are rejected.
    - AI cannot promote a name-only candidate above the deterministic evidence floor.
  - Verify: `go test -count=1 ./internal/ai`
  - Files: `internal/ai/artist_judge.go`, `internal/ai/artist_judge_test.go`

- [x] Task 3: Extend the existing MusicBrainz client with bounded multi-candidate search.
  - Acceptance:
    - `SearchArtists` reuses the existing resilient client, limiter, User-Agent, and normalization.
    - Candidate count and response body are bounded.
    - Aliases are normalized/deduplicated and stable IDs are preserved.
    - `LookupArtist` remains behaviorally compatible and is implemented through the shared search path.
    - Cancellation, malformed responses, low scores, and provider failures return bounded errors without response-body leakage.
  - Verify: `go test -count=1 -run 'TestMusicBrainz.*Artist' ./internal/metadata`
  - Files: `internal/metadata/musicbrainz_client.go`, `internal/metadata/musicbrainz_client_test.go`

- [x] Task 4: Add bounded local artist context and indexed identity-cache reads.
  - Acceptance:
    - `TrackRepository` returns distinct bounded albums, representative tracks, and years without paths or lyrics.
    - Queries are parameterized and use existing artist/album-artist fields.
    - `artistbio.Store` can read cached identity by artist ID in one indexed query.
    - Cache reads preserve compact evidence and never expose mutable shared state.
  - Verify: `go test -count=1 ./internal/db ./internal/artistbio`
  - Files: `internal/db/repo_tracks.go`, `internal/db/repo_tracks_test.go`, `internal/artistbio/store.go`, `internal/artistbio/store_test.go`

- [x] Task 5: Implement resolver orchestration, coalescing, and persistence.
  - Acceptance:
    - Fresh resolved cache skips candidate source and AI.
    - Exact-ID candidates resolve without AI.
    - Ambiguous candidates invoke the shared strict arbiter only after deterministic scoring.
    - Provider/AI outage yields `uncertain` without guessing or blocking client requests.
    - Concurrent work for the same artist is coalesced and cancellation cannot strand an in-flight entry.
    - The resolver accepts the existing `llm`, `mbClient`, `trackRepo`, and store instances; runtime composition is deferred to its first live consumer in `bio-ingestion` so this module does not add unused wiring or external startup work.
  - Verify: `go test -race -count=1 ./internal/artistbio && go test -count=1 ./cmd/proxy ./internal/artistbio`
  - Files: `internal/artistbio/resolver.go`, `internal/artistbio/resolver_test.go`, `internal/artistbio/store.go`

- [x] Task 6: Complete security, regression, and performance verification.
  - Acceptance:
    - Adversarial tag/prompt cases, unknown IDs, oversized data, cancellation, and concurrency tests pass.
    - Errors/logs contain no prompts, API keys, paths, lyrics, or complete library context.
    - No new dependency, parallel AI client, cache framework, unsafe SQL/URL handling, or unrelated file change is introduced.
    - Focused tests, race tests, full tests, vet, build, and diff checks pass.
  - Verify:
    - `gofmt -w internal/artistbio/*.go internal/ai/*.go internal/metadata/musicbrainz_client.go internal/db/repo_tracks.go cmd/proxy/main.go`
    - `go test -race -count=1 ./internal/artistbio ./internal/ai`
    - `go test -count=1 ./...`
    - `go vet ./...`
    - `go build ./cmd/proxy`
    - `git diff --check`
  - Files: only files already touched by Tasks 1–5 plus this checklist
