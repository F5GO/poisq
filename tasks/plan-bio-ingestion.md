# Implementation Plan: `bio-ingestion`

Source of truth: [`SPEC-bio-ingestion.md`](../SPEC-bio-ingestion.md).

## Approach

Build a background-only ingestion service around the completed identity resolver and biography store. Each provider adapter performs a bounded identity lookup, fetches only attributable prose, and returns a common candidate structure. The orchestrator isolates provider failures, coalesces work per artist, and persists every valid candidate without choosing the final biography.

Navidrome contributes only the artist inventory (`id`, `name`) and local release/track context already synchronized into `poisq.db`; its existing biography data is not trusted or copied.

## Dependency Graph

```text
bounded source contract + safe HTTP policy
        ├──→ exact-MBID sources (Wikipedia, TheAudioDB, Last.fm)
        ├──→ catalog-match sources (Yandex, Deezer)
        └──→ credentialed sources (Apple Music, Discogs)
                              ↓
                    ingestion fan-out/store
                              ↓
                 library inventory + background wiring
```

## Components

### 1. Bounded source contract and orchestration core

- Add `BiographySource`, `BiographySourceResult`, and a small provider-neutral ingestion service in `internal/artistbio`.
- Validate provider, source ID, HTTPS provenance URL, text size, language hint, confidence, and compact evidence before persistence.
- Run at most four sources per artist and cap global active ingestion with one semaphore.
- Coalesce concurrent ingestion by artist ID, propagate cancellation, and guarantee in-flight cleanup.
- Treat `nil, nil` as a valid no-biography result and isolate all other provider failures.
- Reuse `Store.PutCandidate`; do not select or translate candidates in this module.

Checkpoint: fake-source tests cover partial outage, concurrency limits, cancellation, duplicate upsert, invalid output, and absence of generated text.

### 2. Shared provider HTTP safety

- Reuse `httpclient.ResilientClient` and extend it minimally so provider construction can enforce a same-host HTTPS redirect policy.
- Keep production base origins fixed and permit local test origins only through package-private constructors.
- Build paths from escaped stable IDs and queries through `url.Values`.
- Sanitize transport/auth errors at the adapter boundary so tokens, response bodies, full URLs, and biography text never escape.
- Cap decoded result collections before conversion and reject truncated/malformed responses.

Checkpoint: offline HTTP tests prove redirect rejection, response bounds, cancellation, and error redaction without changing existing HTTP-client behavior.

### 3. Exact-identity sources

- Extend the existing MusicBrainz client with one bounded relationship lookup for Wikidata/Wikipedia links by resolved MBID.
- Resolve the Russian Wikipedia page through an exact MusicBrainz/Wikidata link and fetch its API summary; never select a same-name page directly.
- Query TheAudioDB by MusicBrainz ID, preferring `strBiographyRU` and retaining another biography only when Russian is absent.
- Query Last.fm `artist.getInfo` by MBID with `lang=ru`, using the existing encrypted key pool and no username context.
- Persist each provider's declared language only as a hint.

Checkpoint: fixture tests cover exact linkage, missing Russian text, missing keys, malformed provider records, and no name-only fallback.

### 4. Yandex Music adapter

- Reuse the existing Yandex request conventions and configured token access without coupling to lyrics types.
- Search a bounded artist list, fetch bounded direct-album evidence, and submit real candidates to the shared deterministic identity rules.
- Fetch `GET https://api.music.yandex.net/artists/{id}/brief-info` only after decisive identity resolution.
- Read only `result.artist.description.text`, source artist ID/name, and safe catalog provenance; an absent description returns no candidate.
- Prefer the public unauthenticated call and use a configured OAuth token only when necessary.

Checkpoint: offline contracts reproduce the manually verified `КИНО` response, an empty-description artist, same-name ambiguity, and provider failure.

### 5. Deezer adapter and approved HTML extractor

- Use the public JSON search/artist/albums endpoints for stable ID and release evidence.
- Require decisive album/identity evidence before requesting the public Russian artist page.
- Fetch only `https://www.deezer.com/ru/artist/{numeric-id}` with a fixed Russian locale and browser-neutral User-Agent.
- Extract the bounded `itemprop="description"` span only inside `naboo_artist_biography_resume_premium`; decode entities and convert documented presentation breaks to plain text.
- Reject missing/reordered semantic markers, scripts, oversized sections, nonnumeric IDs, cross-host redirects, and empty extracted text.
- Keep parsing in one small function with captured HTML fixtures so layout drift fails closed and is easy to repair.

Checkpoint: fixtures from the manually verified КИНО/Земфира shape plus adversarial HTML prove correct extraction and no arbitrary-page scraping.

### 6. Credentialed Apple Music and Discogs adapters

- Enable each adapter only when its provider key/token exists in the current encrypted key pool.
- Apple Music searches the Russian storefront, compares bounded release evidence, and stores documented artist editorial notes after removing documented presentation markup.
- Discogs searches bounded artist results, verifies release evidence, and stores the selected artist profile.
- Reuse the key-pool success/rate-limit/auth reporting behavior; never persist tokens in source URLs or evidence.

Checkpoint: fake-key and HTTP fixtures cover missing credentials, success, 401/403, 429, malformed data, and same-name rejection.

### 7. Background library integration and runtime composition

- Add a schema-tolerant Navidrome artist inventory reader returning only bounded `id` and `name` values in deterministic pages.
- Reuse the existing background synchronization loop as the scheduler; do not create another worker framework or timer.
- After local catalog synchronization, process only artists missing fresh candidates/identity state, with bounded batches so MusicBrainz rate limits cannot monopolize a cycle.
- Compose the existing `poisqDB`, `trackRepo`, `artistbio.Store`, `mbClient`, shared `LLMArtistJudge`, key pool, and provider adapters in `cmd/proxy/main.go`.
- Start no external work during dependency construction and add nothing to the synchronous proxy request path.

Checkpoint: integration tests prove inventory paging, cache skips, bounded batches, clean shutdown, and zero provider calls from `getArtistInfo2`.

## Implementation Order

1. Source contract/orchestrator.
2. HTTP safety extension.
3. Exact-MBID sources.
4. Yandex adapter.
5. Deezer adapter.
6. Apple Music and Discogs adapters.
7. Background inventory/wiring.
8. Full security, race, regression, and manual contract verification.

The orchestration contract comes first so every adapter shares identical validation and persistence. Yandex and Deezer follow the exact-ID sources because their identity matching needs the shared scoring contract. Runtime wiring is last so incomplete providers cannot perform network work.

## Reuse Decisions

- Reuse `artistbio.IdentityResolver`, deterministic scoring, `Store`, and provider enums.
- Reuse `ai.LLMArtistJudge`; no provider may call a separate AI client.
- Reuse `metadata.MusicBrainzClient`, `httpclient.ResilientClient`, provider limiter/circuit breaker, and `keypool.Manager`.
- Reuse the current background sync loop and Navidrome database lifecycle.
- Use a small bounded marker-based Deezer extractor with `html.UnescapeString`; do not add an HTML parser dependency or general scraping framework.
- Do not add a provider SDK, cache framework, database, migration framework, or worker system.

## Risks and Mitigations

| Risk | Mitigation |
|---|---|
| Same-name biography attached to wrong artist | Require exact provider/MBID linkage or decisive release evidence before fetch |
| Deezer changes server-rendered markup | Require semantic container/span markers, fixture tests, fail closed |
| Provider body/token leaks through shared retry errors | Sanitize at adapter boundary and test secret markers |
| Provider fan-out exhausts sockets or rate limits | Per-provider limiter, four-source per-artist cap, global semaphore, bounded batches |
| Background scan takes too long | Fresh-state skips, deterministic paging, batch cap, cancellation between artists |
| Russian label is incorrect | Store only as hint; actual detection belongs to `bio-validation` |
| AI invents identity or biography | Allow only supplied candidate/evidence IDs; prose never enters an AI generation step here |
| Copyright/provenance is lost | Persist canonical human-facing source URL and original text/provider separately |
| New dependency or duplicated infrastructure | Standard library plus existing clients/store/key pool only |

## Verification Checkpoints

1. Core: focused fake-source tests and race detector.
2. HTTP boundary: offline redirect, body-limit, malformed-response, and leakage tests.
3. Each provider: recorded minimal fixtures; no live network required for the suite.
4. Integration: background inventory batching and cache behavior.
5. Manual smoke: Yandex brief-info and Deezer Russian page for two known artists, logging only IDs/status/length.
6. Completion: full `go test`, race tests, `go vet`, proxy build to `/tmp`, and `git diff --check`.
7. Diff audit: no unrelated user files, raw secrets/bodies, unsafe URL construction, prompt logging, request-path network work, or speculative abstraction.

## Out of Scope

- Actual language detection, translation, quality scoring, or automatic winner selection.
- Subsonic JSON/XML response rewriting.
- Studio manual-override handlers and UI.
- Browser automation, session cookies, login bypass, or scraping beyond the approved Deezer section.
- A guarantee that every provider has prose for every artist; missing prose remains an explicit no-candidate result.
