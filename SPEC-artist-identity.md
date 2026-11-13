# Spec: `artist-identity`

## Objective

Resolve a Navidrome library artist to a real external artist identity with explicit evidence and conservative confidence.

The resolver must:

- use the Navidrome artist ID/name plus bounded local album, track, year, alias, MBID, and ISRC context;
- generate candidates only from real catalog providers, initially the existing MusicBrainz client;
- accept exact identifiers and sufficiently strong deterministic evidence without an AI call;
- use the existing text-generation fallback only to arbitrate genuinely ambiguous real candidates;
- return `uncertain` instead of guessing when evidence is insufficient or AI is unavailable;
- persist the result and compact evidence through `bio-store` so repeated work is avoided;
- expose provider-neutral scoring/resolution logic that later biography connectors can reuse.

The module does not fetch biography text, translate content, detect biography language, modify Subsonic responses, or expose Studio handlers.

## Tech Stack

- Existing Go module and standard library.
- Existing `internal/ai.TextGenerator` backed by the configured Groq/Gemini fallback and key pool.
- Existing rate-limited `internal/metadata.MusicBrainzClient` and resilient HTTP client.
- Existing `internal/db.TrackRepository` for local library evidence.
- Existing `internal/artistbio.Store` for persistence.
- No new runtime dependencies.

## Commands

```sh
# Format touched files
gofmt -w internal/artistbio/*.go internal/ai/*.go internal/metadata/musicbrainz_client.go internal/db/repo_tracks.go

# Focused tests
go test -count=1 ./internal/artistbio ./internal/ai ./internal/metadata ./internal/db

# Race checks for resolver/cache concurrency
go test -race -count=1 ./internal/artistbio

# Complete verification
go test -count=1 ./...
go vet ./...
go build ./cmd/proxy
```

## Project Structure

```text
internal/artistbio/identity.go       Bounded context/candidate/result types and deterministic scoring
internal/artistbio/resolver.go       Resolution orchestration, cache use, thresholds, persistence
internal/artistbio/identity_test.go  Scoring, ambiguity, prompt-injection, bounds, and cache tests
internal/ai/artist_judge.go          Existing artist judge generalized for strict identity arbitration
internal/ai/artist_judge_test.go     Shared parser and supplied-candidate enforcement tests
internal/metadata/musicbrainz_client.go
                                     Reused client extended to return bounded artist candidates
internal/db/repo_tracks.go           Reused repository method for bounded local artist context
cmd/proxy/main.go                    Dependency wiring using the existing LLM and MusicBrainz instances
```

Do not add another AI client, HTTP stack, key pool, cache database, worker framework, or generic repository layer.

## Code Style

Deterministic evidence is computed first. AI sees immutable candidate data and may select only an existing ID.

```go
result, err := resolver.Resolve(ctx, ArtistContext{
	ArtistID: "navidrome-id",
	Name:     "МакSим",
	Albums:   []string{"Трудный возраст"},
})
if err != nil {
	return fmt.Errorf("resolve artist identity: %w", err)
}
if result.Status != ResolutionResolved {
	// Preserve uncertainty; do not guess.
}
```

Rules:

- normalize for comparison without changing the stored display spelling;
- keep scoring functions pure and deterministic;
- reuse and strengthen `LLMArtistJudge` instead of introducing a parallel AI judge;
- use stable candidate IDs, never list positions, across internal boundaries;
- cap and sort input before building an AI request;
- validate every AI field and discard explanations that cite unavailable evidence;
- preserve context cancellation and avoid caching cancellations/transient failures;
- log IDs, provider, score, and timing only—never raw prompts or full library context.

## Identity Contract

### Input bounds

- artist ID: 512 bytes;
- artist name/alias: 2 KiB each;
- aliases: at most 20;
- albums: at most 30;
- representative tracks: at most 50;
- ISRCs: at most 100 valid normalized codes;
- candidates passed to AI: at most 8;
- serialized AI evidence payload: at most 64 KiB.

Duplicate values are normalized and removed before scoring.

### Deterministic evidence

Evidence is ordered by reliability:

1. Exact MusicBrainz artist ID or another provider-stable linked ID.
2. Exact ISRC overlap tied to the candidate.
3. Multiple matching releases, with compatible years/track context.
4. One matching release plus a compatible alias/country.
5. Name, transliteration, alias, country, and genre similarity as supporting—not decisive—signals.

Name-only matches never become automatically resolved, regardless of provider search rank.

### AI arbitration

AI is called only when at least two candidates have meaningful deterministic evidence and no candidate has an exact identifier win.

The model receives JSON data with opaque candidate IDs. Its response contract is:

```json
{
  "decision": "same_artist|different_artist|uncertain",
  "candidate_id": "existing-id-or-empty",
  "confidence": 0.0,
  "evidence_codes": ["album:0", "alias:1"],
  "contradiction_codes": []
}
```

The parser rejects:

- unknown candidate IDs;
- unknown evidence codes;
- extra candidates or free-form factual claims;
- non-finite or out-of-range confidence;
- malformed, oversized, fenced, or trailing non-JSON output;
- a resolved decision below the deterministic evidence floor.

AI confidence can lower or preserve deterministic confidence but cannot promote a name-only candidate to resolved.

### Resolution thresholds

- Exact stable identifier: resolved without AI.
- Final confidence `>= 0.95` with decisive evidence: automatically resolved.
- Confidence `0.80–0.95` or conflicting decisive evidence: uncertain.
- Confidence `< 0.80`: uncertain/no match.
- Provider/AI outage: retain the best bounded evidence as uncertain and schedule retry in a later orchestration module.

## Testing Strategy

All focused tests are offline and use fakes for candidate search and `ai.TextGenerator`.

Required tests:

- exact MBID resolves without calling AI;
- name-only match remains uncertain even if provider and AI report high confidence;
- two matching releases can resolve when no contradictions exist;
- conflicting release/ISRC evidence cannot be overridden by AI;
- Cyrillic/Latin aliases and Unicode normalization score consistently;
- same-name artists remain separate;
- AI may return only supplied candidate/evidence IDs;
- prompt-injection strings in names/albums remain quoted data and cannot alter the response contract;
- malformed, fenced, oversized, NaN-like, and trailing AI responses are rejected;
- context and candidate caps are enforced before serialization;
- cached resolved identities skip provider and AI calls;
- uncertain/transient results follow the documented retry/cache policy;
- concurrent resolution of the same artist coalesces or remains race-free without duplicate AI calls where the existing cache permits it;
- errors do not contain raw prompts, API keys, or the complete library context.

Network contract tests for MusicBrainz use a bounded test server and the existing resilient-client patterns; no live network is required.

## Boundaries

- Always:
  - reuse the existing AI, key-pool, MusicBrainz, HTTP, track-repository, and `bio-store` code;
  - perform deterministic scoring before AI;
  - bound, normalize, deduplicate, and JSON-encode all untrusted metadata;
  - require evidence stronger than artist-name similarity for automatic resolution;
  - validate AI output against the exact supplied candidate/evidence sets;
  - persist compact machine-readable evidence codes and confidence.
- Ask first:
  - adding a new AI/provider dependency;
  - changing global LLM ordering or model configuration;
  - sending file paths, lyrics, biography bodies, or user-identifying data to AI;
  - automatically accepting thresholds below this spec.
- Never:
  - let AI invent, search for, or rewrite candidate identities;
  - resolve on name/transliteration alone;
  - place external strings into an instruction role;
  - log raw prompts, full AI responses, API keys, or unnecessary library contents;
  - block a client request on identity resolution or an external call.

## Success Criteria

- Strong exact evidence resolves without AI or redundant provider calls.
- Ambiguous real candidates are evaluated through the existing LLM fallback using a strict bounded JSON contract.
- A malicious or malformed AI response cannot select an unknown artist or bypass the evidence floor.
- Same-name and name-only matches remain uncertain without corroborating releases/identifiers.
- Resolved identity, confidence, provider ID, and compact evidence are stored in `bio-store` and reused.
- The resolver performs no work on the `getArtistInfo2` request path.
- Focused tests, race tests, full tests, vet, and build pass.

## Open Questions

None. Provider-specific biography matching can submit its own real candidates to the same resolver contract during `bio-ingestion`.
