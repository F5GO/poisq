# Implementation Plan: `artist-identity`

Source of truth: [`SPEC-artist-identity.md`](../SPEC-artist-identity.md).

## Approach

Build identity resolution as a bounded, offline-testable pipeline around existing components:

1. Normalize a compact local artist context.
2. Generate real candidates through the existing MusicBrainz client.
3. Score deterministic evidence.
4. Resolve decisive evidence without AI.
5. Reuse and harden the existing `LLMArtistJudge` only for ambiguous candidates.
6. Persist compact identity state in the existing `artistbio.Store`.

External work is background-only. No resolver call is added to the Subsonic request path.

## Dependency Graph

```text
bounded identity types + deterministic scorer
        ├──→ MusicBrainz multi-candidate search
        ├──→ local context loader
        └──→ strict shared AI artist judge
                         ↓
                resolver orchestration
                         ↓
                store cache + wiring
```

## Components

### 1. Bounded identity model and deterministic scorer

- Add provider-neutral artist context, identity candidate, evidence, and resolution result types.
- Normalize Unicode, whitespace, case, aliases, release names, years, and ISRCs without changing display values.
- Deduplicate and cap every collection before scoring or serialization.
- Implement pure evidence scoring with exact-ID, ISRC, release, alias, country, and contradiction signals.
- Make name-only evidence structurally incapable of producing `resolved`.

Checkpoint: pure table-driven tests cover same-name artists, transliteration, exact IDs, release overlap, contradictions, and all caps.

### 2. Reuse and harden the existing AI artist judge

- Preserve the current lyrics-facing `SameArtist` behavior.
- Extract/reuse a strict provider-neutral arbitration method instead of adding another AI client or judge.
- Serialize untrusted tags as JSON data, never interpolate them into instruction text.
- Require a single JSON object, known candidate ID, known evidence codes, finite confidence, and no trailing output.
- Enforce a response-size cap before parsing.

Checkpoint: malicious tag text, unknown IDs, fenced/trailing JSON, duplicate IDs, and oversized responses are rejected without raw prompt logging.

### 3. Extend the existing MusicBrainz client for candidate search

- Reuse its resilient client, User-Agent, limiter, normalization, and response structs.
- Add a bounded `SearchArtists` method returning several real candidates and aliases rather than only the first hit.
- Keep `LookupArtist` behavior compatible by selecting from the new method under its current threshold.
- Add release evidence only through existing/bounded MusicBrainz calls; do not fan out unbounded requests.

Checkpoint: offline HTTP tests verify limits, response-size handling, cancellation, malformed JSON, aliases, and compatibility of `LookupArtist`.

### 4. Reuse the track repository for local context and store for cache

- Add one aggregate `TrackRepository` query for distinct bounded albums/tracks/years by normalized artist and album-artist names.
- Do not load lyrics, paths, or unrelated user data.
- Add indexed identity read support to `artistbio.Store`; reuse the existing identity upsert.
- Define resolved-cache freshness and avoid caching cancellation/transient provider failure as a final match.

Checkpoint: query count/bounds and cache-hit tests show that a fresh resolved identity skips provider and AI calls.

### 5. Resolver orchestration and dependency wiring

- Compose candidate source, deterministic scorer, optional shared AI arbiter, store, and local context source.
- Coalesce concurrent resolution for the same artist with a small keyed in-flight map; do not add a generic caching framework.
- Persist resolved/uncertain state with compact evidence codes.
- Wire existing `llm`, `mbClient`, `trackRepo`, and `artistbio.Store` instances in `cmd/proxy/main.go` without starting network work on startup or request paths.

Checkpoint: end-to-end resolver tests prove exact-ID no-AI resolution, ambiguous AI arbitration, outage fallback to uncertain, cache reuse, cancellation, and concurrency safety.

## Implementation Order

1. Identity model/scorer.
2. Shared strict AI arbitration.
3. MusicBrainz candidate search.
4. Local context/cache reads.
5. Resolver orchestration/wiring.
6. Full security/performance verification.

The scorer is implemented first so AI and provider adapters target a stable contract. Wiring is last so incomplete background behavior cannot affect the running service.

## Reuse Decisions

- Reuse `ai.TextGenerator`, `FallbackGenerator`, and `LLMArtistJudge`.
- Reuse the configured Groq/Gemini chain and key pool unchanged.
- Reuse `MusicBrainzClient`, its limiter, resilient HTTP client, and normalization helpers.
- Reuse `TrackRepository` and `artistbio.Store`.
- Reuse existing logging and context/error conventions.
- Do not add a dependency, AI client, generic repository, new database, migration framework, or new worker framework.

## Risks and Mitigations

| Risk | Mitigation |
|---|---|
| AI prompt injection through tags | JSON data envelope, fixed system instruction, strict output allowlists, deterministic evidence floor |
| Same-name artist false positive | Name-only matches cannot resolve; require ID/ISRC/release evidence |
| AI hallucinates an artist | Accept only supplied opaque candidate IDs and evidence codes |
| Excess provider/AI latency | Exact matches skip AI; cache resolved identity; cap candidates; background-only execution |
| MusicBrainz throttling | Reuse current single-request limiter/cache and bounded search |
| Context or response memory abuse | Byte/count caps before allocation/serialization; bounded HTTP/AI response parsing |
| Duplicate concurrent calls | Per-artist in-flight coalescing with cancellation-safe cleanup |
| Sensitive data leakage | Exclude paths/lyrics; log only IDs, score, provider, and duration |
| Existing lyrics matching regression | Preserve `SameArtist` API and add compatibility tests before refactor |

## Verification Checkpoints

1. Scorer: table-driven focused tests and fuzz tests for normalization/bounds.
2. AI judge: strict parser/security tests with fake `TextGenerator`.
3. MusicBrainz: offline bounded HTTP tests; no live network.
4. Cache/context: SQLite focused tests and query-bound assertions.
5. Resolver: race detector and concurrent-call tests.
6. Module completion: full `go test`, `go vet`, and build.
7. Diff audit: no prompt logging, new secrets, unbounded payloads, unsafe URLs/SQL, duplicate infrastructure, or unrelated user-file changes.

## Out of Scope

- Fetching or selecting biography text.
- Language detection and translation.
- Source-specific biography connectors beyond MusicBrainz identity candidates.
- Background scheduling policy.
- Subsonic response interception.
- Studio UI/API.
