# Spec: `bio-ingestion`

## Objective

Collect real, attributable artist-biography candidates from several independent catalogs after `artist-identity` has resolved the artist.

The module must:

- query only real provider records tied to the resolved artist identity;
- preserve the provider's original biography text apart from safe Unicode/whitespace normalization and removal of provider-documented presentation markup;
- store source artist ID, canonical source URL, fetch time, identity evidence, and raw language hint with every candidate;
- prefer Russian endpoints/fields when providers expose them, while retaining non-Russian originals for the later `bio-validation` translation stage;
- run only in background orchestration, never while forwarding a client `getArtistInfo2` request;
- degrade per provider: one timeout, missing key, malformed response, or empty biography must not block other sources;
- never invent biography prose or treat catalog metadata alone as biography text.

This module does not decide the actual language, translate, select the winning biography, modify Subsonic responses, or expose Studio handlers.

## Source Policy

Initial connectors, in priority-independent fan-out order:

1. Russian Wikipedia/Wikidata-linked article summary.
2. Yandex Music artist description from the public `GET /artists/{id}/brief-info` JSON response, reusing the configured Yandex token only if the endpoint later requires it.
3. Apple Music editorial artist notes when a configured developer token permits access.
4. TheAudioDB Russian biography field, otherwise its original biography field.
5. Last.fm `artist.getInfo` biography using Russian locale where supported.
6. Discogs artist profile from its authenticated API.
7. Deezer artist/catalog data plus the explicitly approved public Russian artist page `GET /ru/artist/{id}`. The public JSON API supplies identity and releases; biography text is extracted only from the bounded server-rendered `itemprop="description"` section under `naboo_artist_biography_resume_premium`.

Provider support is capability-based. A connector that has no biography for an artist returns `no candidate`, not synthesized text. Current official documentation confirms artist editorial notes in Apple Music, locale-aware biographies in Last.fm, and Russian biography fields in TheAudioDB. Manual checks on 2026-11-12 confirmed public unauthenticated Russian descriptions from Yandex `brief-info` and Deezer's server-rendered Russian artist page. Deezer HTML parsing is the sole approved scraping exception and must fail closed when the expected semantic markers are absent. Browser automation, session-cookie capture, other undocumented endpoints, and bypassing provider access controls remain outside this module.

## Tech Stack

- Existing Go module and standard library.
- Existing `httpclient.ResilientClient`, `ProviderLimiter`, circuit breaker, retry, timeout, and response-size controls.
- Existing encrypted `keypool.Manager` for optional provider credentials.
- Existing `artistbio.IdentityResolver`, `artistbio.Store`, provider enums, and candidate validation.
- Existing JSON/XML and Unicode helpers where applicable.
- No new runtime dependency, HTTP stack, cache database, or worker framework.

## Commands

```sh
gofmt -w internal/artistbio/*.go internal/metadata/*.go cmd/proxy/main.go
go test -count=1 ./internal/artistbio ./internal/metadata
go test -race -count=1 ./internal/artistbio
go test -count=1 ./...
go vet ./...
go build -o /tmp/poisq-proxy ./cmd/proxy
```

## Project Structure

```text
internal/artistbio/ingestion.go          Provider-neutral bounded fan-out and persistence
internal/artistbio/ingestion_test.go     Failure isolation, deduplication, bounds, cancellation
internal/artistbio/source_wikipedia.go   Wikipedia API adapter
internal/artistbio/source_yandex.go      Yandex Music adapter using the existing token pool
internal/artistbio/source_catalogs.go    Small Apple/Deezer/TheAudioDB/Last.fm/Discogs adapters
internal/artistbio/source_test.go        Offline provider contract tests
cmd/proxy/main.go                        Composition using existing store, resolver, clients, and key pool
```

Files may be split further only when a provider adapter would otherwise become difficult to test or review. Do not introduce a generic provider framework beyond the small interface below.

## Provider Contract

```go
type BiographySource interface {
	Provider() Provider
	Fetch(ctx context.Context, identity ResolvedArtist) (*BiographySourceResult, error)
}
```

`BiographySourceResult` contains only bounded provider data:

- stable source artist ID: at most 512 bytes;
- canonical absolute `https` source URL: at most 4 KiB, no userinfo;
- original biography: 1 byte through 64 KiB;
- provider-declared language hint: at most 32 bytes;
- compact provider evidence codes: serialized maximum 32 KiB;
- fetch timestamp generated locally.

The interface returns `nil, nil` when the provider has a valid artist record but no biography. It returns an error for transport, authorization, rate-limit, or malformed-response failures. Errors must never include response bodies, tokens, cookies, biography text, or complete request URLs containing credentials.

## Identity and Matching Rules

- Exact provider ID already attached to the resolved identity is used directly.
- Otherwise the connector may perform one bounded artist search and must map results into `IdentityCandidate` values for the shared deterministic resolver contract.
- A provider result is fetched only after exact stable ID resolution or final confidence `>= 0.95` with decisive non-name evidence.
- Name-only search results, transliteration-only matches, and provider rank alone never authorize biography storage.
- AI may arbitrate supplied real candidates through the existing strict judge; it never searches, creates IDs, or creates biography text.
- The stored `match_confidence` is the resolver's confidence, not a provider popularity/search score.

## Fetch Orchestration

- Resolve identity before fan-out; unresolved artists produce no external biography fetch.
- Run at most four provider calls concurrently per artist and cap the global ingestion concurrency with a small semaphore.
- Give every provider a child timeout no longer than 12 seconds and honor parent cancellation.
- Coalesce concurrent ingestion for the same artist using a keyed in-flight map; do not add another cache framework.
- Collect all successful non-empty candidates even if another provider fails.
- Upsert candidates through `artistbio.Store`; uniqueness remains `(artist, provider, source artist ID)`.
- Do not select a winner here. Persist candidates with `fetched` status for `bio-validation`.
- A successful empty result receives a retry-after timestamp in compact provider state only if the existing schema can represent it without a new generic cache; otherwise it is retried by the later scheduler policy.
- Never start a provider request during process initialization merely because dependencies are composed.

## Request Safety

- Provider base URLs are compile-time allowlisted HTTPS origins. Tests may inject a local server through an unexported constructor.
- Provider IDs are escaped only as path segments; search text is encoded with `url.Values`.
- Redirects may remain only on the same allowlisted host and HTTPS scheme; cross-host or HTTPS-to-HTTP redirects are rejected.
- No connector accepts an arbitrary fetch URL from Navidrome tags, provider responses, the database, or AI output.
- Response bodies use the existing hard limit and provider decoders additionally cap result counts and biography fields.
- JSON decoders accept documented additive fields but reject malformed structure and non-scalar identity fields.
- Provider tokens are sent only in the documented header/query field and are never placed in persisted source URLs.
- Rate-limit and authentication outcomes update the existing key pool without storing raw response bodies.
- The Deezer page parser consumes a bounded body, requires both approved semantic markers, decodes entities, removes presentation tags, and returns no candidate rather than guessing when markup changes.

## Russian-First Behavior

- Request Russian locale/market fields when an API supports them (`ru`, `ru-RU`, or the provider's documented equivalent).
- Prefer a non-empty provider Russian biography field over its default-language field.
- Do not trust provider language labels as proof; persist them only as hints.
- Do not discard a real non-Russian biography. The later validation module performs actual language detection and a grounded translation.
- Do not ask an LLM to fill an empty provider result.

## Testing Strategy

All provider tests are offline and use bounded local HTTP servers or pure fakes.

Required tests:

- exact linked provider ID fetches without search or AI;
- an unresolved/name-only artist performs no biography fetch;
- one failing or timing-out provider does not suppress other candidates;
- concurrent requests for one artist are coalesced and race-free;
- duplicate refreshes update one stored candidate;
- Russian fields are preferred but non-Russian originals are retained;
- empty, whitespace-only, oversized, malformed, and truncated biographies are rejected;
- more than the documented search-result cap is ignored;
- arbitrary/cross-host URLs and unsafe redirects cannot be fetched;
- tokens and provider response bodies do not appear in errors, logs, source URLs, or evidence JSON;
- cancellation stops fan-out and does not strand the in-flight key;
- no connector or orchestration code generates biography prose;
- full existing lyrics, search, proxy, scanner, and Studio tests remain green.

## Boundaries

- Always:
  - reuse the shared identity resolver, store, HTTP resilience layer, key pool, and provider limiters;
  - retain exact source attribution and original text;
  - isolate provider failures and keep work off the client request path;
  - validate identity before fetching and validate bounds before persistence;
  - use fixed allowlisted provider origins and safe URL construction.
- Ask first:
  - adding a provider SDK or runtime dependency;
  - using an undocumented/private endpoint, scraping HTML beyond the approved public Deezer artist-page section, or requiring session cookies;
  - changing global retry, limiter, or key-pool behavior;
  - persisting any new secret or user-identifying data.
- Never:
  - invent, complete, summarize, or paraphrase biography prose in ingestion;
  - accept a name-only provider match;
  - let AI supply a provider ID, URL, evidence code, or biography body not present in real provider data;
  - log tokens, raw provider bodies, biography text, prompts, library paths, or full artist context;
  - block `getArtistInfo2` or another client request on external biography work.

## Success Criteria

- At least one configured/available provider can contribute a real attributable candidate without changing client latency.
- Every persisted candidate is tied to a resolved artist with decisive evidence and exact provenance.
- Russian provider text is retained as-is; non-Russian real text remains available for later grounded translation.
- Missing credentials and partial provider outages degrade cleanly.
- No connector can be turned into an arbitrary URL fetcher or leak credentials/body data.
- Repeated and concurrent ingestion avoids duplicate records and unnecessary calls.
- Focused tests, race tests, full tests, vet, build, and diff checks pass.

## Open Questions

None for the provider-neutral ingestion contract. Each credentialed connector remains disabled until its key exists in the current encrypted key pool.
