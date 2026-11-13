# Spec: `bio-synthesis`

## Objective

Create a short Russian artist summary with AI from several real, already identity-verified biography sources, and deliver it with an explicit AI disclosure.

The module must:

- synthesize from attributable provider texts already collected by `bio-ingestion`, including verified originals in Russian, English, German, and other confidently identified languages;
- discover additional public-web evidence through a bounded search API, including official artist/label sites, interviews, encyclopedias, and reputable editorial publications;
- require at least two independent external origins (catalog providers and/or cited public websites) before synthesis;
- produce 400–1200 Unicode characters of Russian prose, excluding the disclosure and source line;
- append `Саммари создано ИИ на основе проверенных источников: …` to every synthesized biography;
- retain the exact contributing candidate IDs, provider names, URLs, content versions, and claim-to-source mapping;
- reject additions, unsupported claims, unresolved contradictions, promotional language, and model knowledge absent from the supplied sources;
- prefer a grounded AI summary over an individual automatic provider biography, while retaining manual Studio text as the highest priority;
- stop creating `local_library` descriptions and remove previously selected local fallbacks without touching manual overrides;
- remove periodic all-artist biography scans and start collection only from a real artist-info request or an explicit per-artist Studio rebuild;
- allow the first client request to wait for a short bounded result while the actual job remains detached from request cancellation.

If fewer than two eligible external sources exist, the best accepted provider biography remains the automatic result. When neither synthesis nor an accepted provider biography is possible, no automatic biography is created; source-free AI prose is forbidden.

## Assumptions

1. “Several sources” means at least two distinct external origins: catalog providers and independently cited public websites may be combined.
2. Existing identity confidence plus deterministic source-quality and actual-language validation remain mandatory before a source may enter synthesis; a source does not need to be Russian.
3. The client-visible disclosure is part of the biography text because Subsonic exposes no portable structured field for AI provenance.
4. Provider names are shown in the disclosure; canonical URLs and detailed evidence remain available in Studio/storage.
5. A previously accepted summary remains visible during temporary LLM/provider outages and is replaced only by a newly grounded result.
6. The model may compress and rephrase supported facts but may not fill gaps from its pretrained knowledge.

## Tech Stack

- Existing Go module and standard library, including `crypto/sha256` for bounded input-version keys.
- Existing `artistbio.Store`, source candidates, provider ranking, background processor, and manual precedence.
- Existing `ai.TextGenerator` Groq/Gemini fallback chain.
- Tavily Basic Search as the preferred zero-cost web retrieval path, using an optional key from the existing encrypted key pool and direct bounded HTTP JSON without an SDK.
- Optional Gemini Google Search grounding only when the project has billing/search quota enabled; it is never required for free mode.
- Existing deterministic language detection and strict JSON decoding patterns.
- No search SDK, browser scraper, embedding store, vector database, or new runtime dependency; Tavily uses the existing bounded HTTP/key-pool patterns.

## Commands

```sh
gofmt -w internal/artistbio/*.go internal/ai/*.go cmd/proxy/main.go
go test -count=1 ./internal/artistbio ./internal/ai ./internal/proxy ./internal/admin
go test -race -count=1 ./internal/artistbio ./internal/ai ./internal/proxy
go test -count=1 ./...
go vet ./...
go build -trimpath -o /tmp/poisq-proxy ./cmd/proxy
go run golang.org/x/vuln/cmd/govulncheck@latest ./...
git diff --check
```

## Project Structure

```text
internal/ai/biography_synthesis.go       Strict synthesis and grounding contracts
internal/ai/biography_synthesis_test.go  Injection, attribution, malformed output, and grounding tests
internal/artistbio/web_search.go          Bounded Tavily/Gemini search adapters and normalized evidence
internal/artistbio/web_search_test.go     Citation, identity, URL, quota, and response-bound tests
internal/artistbio/synthesis.go          Source selection, versioning, persistence, and retry orchestration
internal/artistbio/synthesis_test.go     Multi-source behavior, priority, caching, and failure tests
internal/artistbio/store.go               Summary persistence/selection and legacy fallback cleanup
internal/artistbio/store_test.go          Atomic priority and cleanup tests
internal/artistbio/background.go          Replace periodic scan with bounded on-demand artist jobs
cmd/proxy/main.go                         Compose the synthesizer from the existing LLM chain
```

Existing files may be extended instead of creating a listed file when that keeps the implementation smaller and clearer.

## Cost-Aware Pipeline

The feature uses three isolated AI roles. They share persisted evidence IDs, not conversation history or unrestricted raw payloads.

### 1. Deterministic catalog preparation

No model call is made while collecting or initially compacting existing provider biographies.

- Select at most four distinct catalog providers by identity confidence, reliability, and text quality.
- Normalize whitespace and split text into bounded sentences using the existing biography helpers.
- Retain the introduction plus sentences containing concrete names, dates, places, genres, labels, releases, collaborations, or achievements; never rewrite them at this stage.
- Cap each catalog extract at 1,200 Unicode characters and all catalog extracts at 4 KiB.
- Deduplicate normalized identical sentences and near-identical provider boilerplate deterministically without embeddings.
- Compute a version hash before any AI work. Unchanged evidence reuses the previous discovery and summary results.

Catalog evidence is considered sufficient without web discovery when it contains at least two independent eligible providers, at least 1,200 useful characters in total, and a previous grounding attempt has not reported missing support or a source conflict.

### 2. Web evidence collector

The search adapter retrieves bounded results; a separate collector model validates identity and turns only those results into evidence cards. The collector does not write the final biography.

- Invoke it only when catalog evidence is insufficient, contradictory during a prior grounding attempt, or explicitly refreshed by the user.
- Send the search API at most 2 KiB of identity context: canonical name/aliases, country, stable provider IDs, and no more than three album plus five track titles.
- Send the collector at most 4 KiB of combined search snippets and identity evidence.
- Request strict evidence-card JSON rather than narrative prose.
- Return at most six cards and four independent web origins, with each card capped at 500 characters and the complete collector output capped at 4 KiB.
- Every card contains a stable local ID, one bounded factual statement, detected source language, cited HTTPS URL/title/domain, identity evidence, and source-native result/citation coordinates.
- Reject collector output that cites a URL/result not present in the bounded search response.
- Prefer `qwen/qwen3.8-27b` for collection, with `openai/gpt-oss-20b` as fallback; neither model receives a search tool.

A successful web result is cached for 90 days; a safe empty result is cached for 30 days. Transport/quota failures use the existing short retry/cooldown behavior and do not invalidate earlier evidence.

### 3. Summary editor

The editor uses the existing Groq/Gemini text fallback but has no search tool and cannot add sources.

- Input is only the compact catalog extracts and web evidence cards, never full pages or full 64 KiB provider biographies.
- Select at most eight evidence items from at most four origins and cap the complete JSON input at 6 KiB.
- Prefer complementary facts and omit duplicate, weak, promotional, time-sensitive, or contradictory cards.
- Return the strict sentence/source-ID mapping defined below and 400–1200 Russian characters.
- Cache by the sorted evidence-version hash; unchanged evidence causes zero editor calls.
- Prefer `openai/gpt-oss-20b` for editing, falling back through the existing chain.

### 4. Grounding verifier

The verifier is a separate request and prompt from the editor. When possible, the existing fallback should choose a different provider/model than the successful editor call; lack of a second provider must degrade to a separate stateless verification call, never self-approval inside the editor response.

- Send only each generated sentence and the evidence items it cites.
- Never resend unused sources or search context.
- Cap the verification payload at 8 KiB.
- Require per-sentence multilingual entailment at confidence `>= 0.95`.
- Reject the whole new summary if any sentence is unsupported, contradictory, or uncertain.
- Prefer `openai/gpt-oss-120b` for verification so collection, editing, and verification use separate free-model quotas when available.

Run at most one artist synthesis pipeline concurrently and start no more than three new artist pipelines per minute. Honor Groq rate-limit headers and `retry-after`; never spin or immediately retry `429`. This design bounds a normal catalog-only artist to two small AI calls (editor and verifier), a cache hit to zero calls, and an under-documented artist to one search plus one additional collector call.

## On-Demand Lifecycle

Biography processing is cache-aside and demand-driven rather than an inventory-wide background scan.

### Client trigger

- A valid authenticated Subsonic `getArtistInfo2`/`.view` request with a real Navidrome artist ID first performs the indexed local effective-biography lookup.
- A manual override or completed automatic result is returned immediately and starts no work.
- A missing terminal result validates that the upstream Navidrome response represents the requested artist, then enqueues exactly one detached job for that artist.
- Concurrent requests, Studio actions, and retries for the same artist join one keyed singleflight job; they never duplicate provider, search, or AI calls.
- The first client request waits for a configurable bounded duration. Use a provisional 20-second default during implementation, accept only 5–30 seconds from configuration, and choose the release default from measured cold-path latency before handoff. If the job completes, the new biography is injected into that response. If not, poisq returns the safe upstream response and the job continues under a service-owned timeout of at most 90 seconds.
- Request cancellation stops only that request's wait; it does not cancel a job already accepted by the bounded queue.
- A full queue, quota cooldown, or unavailable AI returns the upstream response immediately rather than extending client latency.

The proxy path may trigger/enqueue and bounded-wait, but it never performs provider/search/AI work in the request goroutine itself. Logs record only per-stage duration, total job duration, cache/search path, and outcome code; they never include prompts or source/summary text.

### First-request latency calibration

- Expose the client wait as `BIO_FIRST_REQUEST_WAIT`; accept only durations from 5 through 30 seconds and use 20 seconds as the provisional implementation default.
- Before release, measure at least five cold runs in each representative class: catalog-rich, web-discovery-required, multilingual, safely empty, and quota/retry failure.
- Record `p50`, `p90`, and `p95` for the complete detached job and for catalog collection, web discovery, evidence collection, editing, and verification separately. Logs and test reports contain durations and outcome codes only, never source or generated text.
- Set the release default to the measured total-job `p90` plus a 2-second scheduling/network margin, clamped to the allowed 5–30-second range. This protects most first openings without letting rare slow runs hold clients indefinitely.
- Verify the selected value against the supported Subsonic clients. If measured `p90 + 2s` exceeds 30 seconds or a client times out earlier, keep the safe lower limit for that client path: return the upstream response and let the accepted job finish in the background.
- The 90-second service-owned job timeout is independent from the client wait and is not increased automatically by calibration.

### Persistent terminal state

- `ready`: manual or accepted automatic biography/summary exists; return it indefinitely.
- `no_data`: a complete successful run found insufficient real evidence; store this terminal result indefinitely to prevent repeated cost.
- `retryable`: transport, quota, provider, or model failure; keep bounded error class and next retry time, never raw text.
- `queued` / `running`: observable non-terminal job states with timestamps and stale-job recovery.

`ready` and `no_data` are not automatically refreshed by age, source changes, application restart, or scheduled inventory processing. A `retryable` artist may enqueue again only after its persisted cooldown and a new real request or Studio action. Existing accepted output remains visible while a rebuild is running or fails.

### Bounded global queue

- Reuse existing process/job patterns and the singleflight abstraction; do not add a message broker.
- Queue at most 100 distinct artist jobs, run one synthesis pipeline concurrently in free mode, and apply the role/token pacing above.
- Reject duplicate artist IDs before queue admission and validate all IDs/names with existing bounds.
- Persist job state before external work so restart recovery cannot create a request storm.
- Do not let unauthenticated, failed upstream, malformed, or unknown artist requests consume search/AI quota.

### Studio per-artist rebuild

Artist Biography Studio adds `Пересобрать описание` for the selected artist in addition to the existing global reset.

- The action is authenticated, CSRF-protected, method-restricted, and accepts only the selected bounded artist ID.
- Atomically mark only that artist for rebuild, invalidate its stored catalog/web evidence version, clear its `no_data`/retry cooldown, mark the current automatic selection stale without deleting it, and preserve its manual override until the user explicitly removes it.
- Preserve the last valid automatic text while rebuilding; replace it only after a complete newly grounded result commits.
- Enqueue the artist immediately and return job status; Studio polls the existing detail endpoint for `queued`, `running`, `ready`, `no_data`, or `retryable`.
- The global reset remains available but does not automatically enqueue every artist; it only invalidates automatic states, so work still begins on demand.

## Code Style

Untrusted provider prose is JSON user data and every synthesized sentence carries explicit source IDs:

```go
result, err := model.Synthesize(ctx, ai.BiographySynthesisInput{
    ArtistName: artist.Name,
    Sources:    eligibleSources,
})
if err != nil || !result.Grounded || len(result.SourceIDs) < 2 {
    return ErrSynthesisNotGrounded
}
```

Rules:

- reuse existing bounds, provider enums, store transactions, in-flight coalescing, and strict JSON helpers;
- keep pure source selection/versioning separate from model calls and persistence;
- use stable candidate IDs in prompts and evidence, never free-form model-provided URLs;
- log only artist ID, provider count, status, latency, and compact error class; never log source or summary bodies;
- keep provider/source ordering deterministic for stable hashes and tests.

## Source Eligibility

A source may enter synthesis only when:

- its artist identity was resolved with final confidence `>= 0.95`;
- its original provider text passes the existing deterministic size, markup, repetition, and minimum-content checks;
- its actual language is deterministically identified or confirmed by the bounded classifier with confidence `>= 0.90` and is not `und` or materially mixed;
- it has a canonical provider, source artist ID, and persisted provenance;
- its provider is external: `local_library`, `manual`, and an earlier AI summary are excluded;
- it was not rejected because the provider text itself was unsafe or low quality; failure of a separate Russian translation does not disqualify an otherwise valid original source.

Use at most four highest-ranked distinct providers and at most 24 KiB of total source prose. Pass each source's detected language with its original text. Truncate only at sentence boundaries. Prefer complementary higher-reliability sources; do not send duplicate provider texts merely to satisfy the minimum count.

The eligible current source set is reused without a second fetch layer: Wikipedia, Yandex Music, Deezer, Apple Music, TheAudioDB, Last.fm, Discogs, and an explicitly attributable official source when available. Empty provider results remain empty and are never completed by AI.

## Public Web Discovery

When deterministic catalog coverage is insufficient or a prior grounding attempt reports a conflict/missing support, the background pipeline may perform one cached web search. The request includes only bounded identity context: canonical artist name/aliases, resolved provider IDs, country when known, and a small set of exact album/track titles. It never includes library paths, user identity, credentials, listening history, or unrelated collection data.

Search-provider order:

1. Tavily Basic Search when a `tavily` key exists. This is the default free mode: one basic request consumes one of the 1,000 free monthly credits and requires no payment card.
2. Gemini Google Search grounding only when the Gemini project reports usable search quota. Current Gemini 3 API free-tier projects have no Search grounding quota; enabling this fallback therefore requires a billing-enabled project even though paid projects currently receive a monthly included search allowance.
3. No web search. Catalog-only synthesis continues safely when neither search path is configured.

The search stage must:

- search the public web in any language and return bounded result snippets/evidence with source URLs;
- use the resolved identity context to distinguish homonyms and reject pages that contradict the known artist;
- prefer official artist and label sites, direct interviews, established encyclopedias, and reputable editorial publications;
- reject social posts, forums, anonymous user-generated pages, lyric mirrors, ticket/event listings, SEO biography farms, AI-generated pages, and pages without enough artist-specific evidence;
- accept only cited `https` web URLs with a normal public hostname, no userinfo, no literal/private/link-local IP, no non-web scheme, and bounded title/URL lengths;
- require every retained web evidence item to be connected to a source-native Tavily result or Gemini citation annotation and at least one identity signal beyond a bare name, such as a matching release, track, real name, official domain, label, or resolved catalog ID;
- retain only a bounded paraphrasable evidence fragment and citation metadata, never a complete copyrighted page;
- cap one discovery operation to one Tavily Basic request or at most two observed Gemini search calls, four cited web origins, 4 KiB of grounded evidence, and a 30-second child timeout;
- cache a successful result for 90 days and a safe empty result for 30 days per resolved identity/query-version so normal background passes make zero repeated search calls.

Web retrieval is optional enrichment. If Tavily/Gemini search is unavailable, exhausted, rate-limited, unconfigured, or returns unsafe/insufficient citations, synthesis continues from eligible catalog sources. Groq may collect/synthesize catalog or retrieved evidence already supplied to it, but it never performs or pretends to perform web search.

The response must expose actual search results or search-call steps with source URLs. A plain model answer claiming that it searched the web is not web evidence, and poisq never scrapes a public search-engine HTML page as a fallback.

## Synthesis Contract

The model receives bounded JSON containing artist name and sources identified only by stable supplied IDs, provider/domain names, detected languages, and original catalog text or cited web evidence. All source prose in every language is untrusted data; instructions inside it are inert. The output is always Russian, so the model performs bounded cross-language synthesis rather than requiring separate full-text translations first.

The model returns strict JSON with:

- `summary`: Russian prose only, 400–1200 characters;
- `sentences`: the exact ordered summary sentences;
- for each sentence, one or more supplied source IDs that support the complete sentence;
- `used_source_ids`: the union of cited supplied source IDs;
- no URLs, confidence explanations, Markdown, or additional fields.

Every Russian factual sentence must be semantically supported in full by at least one supplied catalog source or cited web evidence item. A sentence combining facts from several sources must cite all required sources. The model must omit:

- facts contradicted by another eligible source;
- uncertain dates, real names, places, genres, memberships, awards, and relationships;
- rankings, listener counts, promotional adjectives, and time-sensitive claims unless directly supported and uncontradicted;
- conclusions or causal claims not stated by a source.

The application appends the disclosure itself after the summary; the model cannot change, move, or omit it.

## Grounding and Safety

- First run deterministic checks: exact sentence mapping, supplied-ID subset, source-union equality, Russian-language thresholds, length bounds, and preservation of explicit numbers/dates/names.
- Then run a separate strict multilingual grounding verdict over each Russian sentence and only its cited catalog texts/web evidence items.
- The verifier may return only `grounded`, `not_grounded`, or `uncertain`, confidence, sentence IDs, and allowlisted evidence codes.
- Every sentence must be `grounded` with confidence `>= 0.95`; one failed sentence rejects the entire new summary.
- Prompt injection in provider text, malformed/fenced/trailing JSON, duplicate keys, unknown IDs, omitted sentences, invented citations, excessive output, and non-Russian text fail closed.
- Source texts, prompts, and generated summaries never appear in logs or errors.
- Client requests never execute synthesis, validation, providers, or LLMs inline; a cache miss may only enqueue and wait on the detached job for the configured 5–30-second budget.

## Persistence, Priority, and Refresh

- Persist a synthesized result as a distinct `ai_summary` automatic candidate with no model-generated source URL.
- Evidence stores the sorted contributing candidate/web-evidence IDs, provider/domain names, canonical cited URLs, source content-version hash, sentence-to-source mapping, source-native search/citation metadata, model-grounding code, and timestamps within existing size limits.
- The displayed text is constructed as:

```text
[grounded Russian summary]

Саммари создано ИИ на основе проверенных источников: Wikipedia, Яндекс Музыка, system108.com.
```

- Read priority is: non-empty manual override → accepted current `ai_summary` → best accepted provider biography → no automatic biography.
- A summary input-version is a SHA-256 hash of sorted candidate/web-evidence IDs, update times, detected languages, original source texts, and cited evidence fragments. Unchanged versions cause zero model calls.
- No age- or source-change-based automatic refresh runs after a terminal `ready` or `no_data` result.
- An explicit per-artist rebuild invalidates that artist's catalog/web evidence versions and performs a fresh collection; a failed rebuild leaves the previous accepted summary selected and records bounded retry state.
- On deployment, clear automatic selections pointing to `local_library`, delete those candidates, and preserve identities, external candidates, summaries, and manual text.

## Free-Tier Operating Profile

The feature must be fully usable without attaching a payment method when the user supplies a free Tavily key and at least one free Groq key.

- Tavily Researcher currently provides 1,000 API credits per month without a credit card; one Basic Search costs one credit. When exhausted, Tavily requests stop until the monthly reset and poisq falls back to catalogs.
- Groq's current free limits for `qwen/qwen3.8-27b`, `openai/gpt-oss-20b`, and `openai/gpt-oss-120b` are each 30 RPM, 1,000 RPD, 8,000 TPM, and 200,000 TPD at organization level. Exact account limits from response headers/dashboard take precedence because providers may change them.
- Gemini free text generation remains an optional fallback with model/project-specific RPM/TPM/RPD limits. Gemini 3 Google Search grounding is not part of the API Free Tier and must never be required for free operation.
- Free-tier `429` or exhausted daily/monthly quota pauses that role, honors `retry-after`/reset metadata, and leaves the artist retryable without deleting an earlier biography or summary.
- No automatic upgrade, paid endpoint, or request beyond a free quota is attempted. Enabling billing/search providers is an explicit Studio configuration action.
- Free Gemini requests may be used by Google to improve its products under the current Free Tier terms; only bounded public artist evidence is sent, never credentials, paths, listening history, or personal user data.

For a 34-artist library, no work occurs until pages are opened. If the user eventually opens all 34 artists and every one needs web discovery, the total is bounded to 34 Tavily credits and at most 102 compact LLM calls split across three preferred Groq models. The specified byte budgets keep each role comfortably below the published 200,000-token daily allowance for that library size, while the one-pipeline/three-starts-per-minute scheduler protects the 8,000-token-per-minute limits. Actual provider token accounting and account-specific limits are authoritative.

## Testing Strategy

All model tests use fake `TextGenerator`; provider tests remain offline.

Required tests:

- startup and scheduled maintenance perform zero artist provider/search/AI calls;
- the first valid artist-info cache miss enqueues one job and returns its result when completed within the configured wait budget;
- invalid `BIO_FIRST_REQUEST_WAIT` values fail safely to the provisional/release default, and valid boundary values of 5 and 30 seconds are accepted;
- a slow job returns the upstream response at the wait deadline and continues to a persisted result after request cancellation;
- concurrent client/Studio requests for one artist join one job, while distinct artists obey the bounded queue;
- `ready` and `no_data` survive restart and repeated requests with zero external calls;
- retryable quota/transport failures obey persisted cooldown before another real request can enqueue;
- per-artist Studio rebuild is authenticated/CSRF-safe, invalidates only that artist, preserves manual and last valid automatic text, and starts one job;
- global reset enqueues no artists and subsequent collection remains demand-driven;
- two eligible independent sources in different languages produce a disclosed Russian summary;
- one catalog source plus one independently cited public website can satisfy the two-origin requirement;
- catalog-rich artists skip public-web discovery when two independent providers and enough grounded text already cover the summary;
- Tavily results expose real result URLs/snippets and optional Gemini grounding exposes search-call/citation steps; an uncited model answer is rejected;
- unsafe, private-network, malformed, non-HTTPS, oversized, duplicate, and identity-ambiguous web citations are rejected;
- official/editorial web evidence is accepted while forums, social posts, ticket pages, lyric mirrors, and SEO farms are excluded;
- web discovery timeout, quota failure, or missing Tavily/search-enabled Gemini key falls back to catalog-only synthesis without erasing prior output;
- the same resolved identity/query version performs at most one discovery interaction per 90-day successful or 30-day empty-result cache period;
- one source retains the provider biography and makes no synthesis call;
- zero sources produce no automatic AI biography;
- manual text remains above AI summary; AI summary remains above individual provider candidates;
- local-library candidates/selections are removed without deleting manual or external data;
- unchanged source-version makes no repeated model call;
- an explicit per-artist rebuild invalidates stored evidence and creates a new grounded summary atomically;
- source/model outage or rejected refresh preserves the prior accepted summary;
- an English, German, or other confidently identified source can support a Russian sentence without first becoming a full stored translation;
- every sentence cites supplied source IDs and unknown/missing/duplicate citations reject output;
- unsupported, contradictory, invented, promotional, or model-knowledge facts reject the full summary;
- Russian, length, mapping, JSON, response-size, and evidence-size bounds are enforced;
- prompt injection remains inert JSON data;
- concurrent synthesis for one artist is coalesced and race-free;
- logs/errors contain no source prose, summary, prompt, secrets, or library paths;
- client delivery performs a bounded local read, at most one queue trigger, and a configured 5–30-second detached-job wait with zero provider/AI calls in the request goroutine;
- focused, full, race, vet, build, vulnerability, and diff checks pass.
- the release wait default is selected from the documented cold-path calibration as `clamp(p90 + 2s, 5s, 30s)` and remains configurable.

## Boundaries

- Always:
  - use only attributable, identity-verified, quality-checked external source texts with a confidently identified actual language;
  - require actual Tavily result records or Gemini Search execution steps with URL citations for public-web evidence;
  - keep web discovery in the background and cache it independently from client requests;
  - disclose AI synthesis in every client-visible summary;
  - keep claim-level source mapping and a reproducible source-version hash;
  - verify grounding separately from generation and fail closed;
  - preserve manual precedence and the last valid summary during transient failures;
  - bound provider count, input bytes, output size, retries, and concurrency.
- Ask first:
  - adding a search provider beyond Tavily and optional Gemini, a direct arbitrary-page fetcher, browser scraping, or an undocumented endpoint;
  - allowing a one-source AI summary;
  - changing the disclosure wording, its final position, or the 400–1200 character range;
  - lowering identity, Russian-language, or grounding thresholds;
  - adding a database column/table or runtime dependency if existing candidate/evidence storage cannot safely represent the result.
- Never:
  - use the model's pretrained knowledge as a source;
  - treat an uncited model answer or claimed search as public-web evidence;
  - fetch arbitrary search-result URLs from the poisq host or allow web discovery to become SSRF;
  - synthesize from local filenames/tags, unsafe or low-quality provider text, manual prose, or another summary;
  - invent a source ID, provider, URL, quotation, fact, or confidence value;
  - hide or let the model author the AI disclosure;
  - erase a valid prior summary because a refresh failed;
  - execute provider or AI work in a Subsonic request goroutine.

## Success Criteria

- Every AI summary ends with a visible statement that it was created by AI and names its contributing providers.
- Every Russian factual sentence is traceable to at least one stored catalog source or cited public-web evidence item and passes strict multilingual grounding at `>= 0.95`.
- No summary is created from fewer than two independent eligible external origins (catalog providers and cited websites combined).
- Public-web evidence is searched conditionally through free Tavily Basic Search or optional billing-enabled Gemini grounding, cached for 90 days when successful or 30 days when safely empty, and degrades to catalog-only behavior when unavailable.
- Manual text remains highest priority; a grounded summary is preferred over a single long provider biography.
- Artists without sufficient real data receive no fabricated automatic description.
- Legacy local-library filler is no longer generated or delivered.
- Repeated client requests cause no AI cost after `ready` or `no_data`; only an explicit rebuild or cooldown-eligible retry can run again, and outages preserve the last valid output.
- All safety, concurrency, full-suite, build, and vulnerability checks pass.

## Open Questions

None. Tavily is the approved default web-search provider, and the bounded first-request wait will be finalized from cold-path measurements rather than guessed in advance.
