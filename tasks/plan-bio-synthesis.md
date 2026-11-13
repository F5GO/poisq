# Implementation Plan: `bio-synthesis`

Source of truth: [`SPEC-bio-synthesis.md`](../SPEC-bio-synthesis.md), with delivery and Studio amendments in [`SPEC-bio-delivery.md`](../SPEC-bio-delivery.md) and [`SPEC-studio-overrides.md`](../SPEC-studio-overrides.md).

## Approach

Replace the periodic artist-wide biography scan and `local_library` filler with one persistent cache-aside coordinator. A real `getArtistInfo2` miss or an explicit Studio rebuild admits one detached, keyed job. That job reuses the existing identity resolver, catalog sources, HTTP safeguards, key pool, LLM fallback, strict JSON patterns, candidate store, and proxy response rewriting.

The pipeline compacts catalog evidence deterministically, searches Tavily only when catalog evidence is insufficient, asks isolated AI roles to collect evidence, edit a Russian summary, and verify every sentence, then commits a new `ai_summary` atomically. Manual text and the last accepted automatic result remain visible until a complete replacement succeeds.

## Dependency Graph

```text
persistent job/evidence state + legacy cleanup
                    ↓
deterministic evidence compaction + versioning
                    ↓
bounded Tavily adapter + encrypted key support
                    ↓
strict collector/editor/verifier AI contracts
                    ↓
detached keyed coordinator + quota pacing
             ┌──────┴──────┐
             ↓             ↓
 proxy trigger/wait   Studio per-artist rebuild
             └──────┬──────┘
                    ↓
 integration, security, and latency calibration
```

## Components

### 1. Persistent lifecycle and atomic presentation

- Extend the existing biography schema/store only where current candidate evidence cannot represent `queued`, `running`, `ready`, `no_data`, `retryable`, stale rebuild state, web evidence versions, and cooldowns safely.
- Persist `ai_summary` as a distinct automatic candidate with bounded claim/source evidence and no fabricated source URL.
- Preserve precedence `manual → ai_summary → provider biography → none` and the prior accepted automatic result during rebuild or failure.
- Remove selected and stored `local_library` fallbacks idempotently without touching manual or external candidates.
- Recover stale queued/running jobs on startup without starting an inventory scan.

Checkpoint: restart and cleanup tests prove terminal-state stability, atomic replacement, correct precedence, and zero startup provider/AI calls.

### 2. Deterministic evidence preparation

- Reuse identity-verified accepted catalog candidates and language analysis.
- Select at most four origins, compact each to 1,200 characters and all catalog evidence to 4 KiB, remove duplicate/boilerplate sentences, and compute a stable SHA-256 version.
- Decide without AI whether two independent providers and 1,200 useful characters make catalog evidence sufficient.
- Exclude manual text, local tags/filenames, prior summaries, rejected candidates, and ambiguous identities.

Checkpoint: deterministic fixtures produce stable bounds/hashes and unchanged inputs cause zero model or search calls.

### 3. Bounded public-web discovery

- Add Tavily Basic Search through the existing resilient HTTP and encrypted key-pool patterns, with no SDK or search-page scraping.
- Send at most 2 KiB of bounded identity context and accept only bounded HTTPS result records with source-native identifiers.
- Reject private/local destinations, malformed URLs, redirects outside policy, low-quality domains/categories, duplicate results, oversized bodies, and results that do not match the resolved artist.
- Cache successful discovery for 90 days and safe empty results for 30 days; transport/quota failures remain retryable and honor provider cooldown metadata.
- Add `tavily` to existing Studio/key management without exposing key material.

Checkpoint: offline adapter tests cover quota handling, SSRF/redirect defense, response bounds, citation provenance, key redaction, and catalog-only degradation.

### 4. Isolated AI contracts

- Add strict bounded JSON contracts for evidence collection, Russian summary editing, and sentence-level grounding verification using the existing `TextGenerator` chain.
- Collector may only cite supplied Tavily results; editor may only cite supplied evidence IDs; verifier receives only each sentence and its cited evidence.
- Enforce payload/output limits, duplicate-key/trailing-data rejection, Russian and length checks, exact source-union mapping, and `>= 0.95` grounding for every sentence.
- Treat source text as inert untrusted data and keep prompts, evidence prose, summaries, credentials, and paths out of logs/errors.
- Prefer role-specific Groq models while retaining existing safe fallback behavior and provider cooldowns.

Checkpoint: hostile prompt, invented citation, unsupported fact, contradiction, malformed JSON, outage, and multilingual grounding tests fail closed.

### 5. Detached on-demand coordinator

- Replace the scheduled inventory processor with a bounded in-process queue of at most 100 distinct artist IDs, one active pipeline, and at most three starts per minute.
- Reuse/adapt the existing singleflight behavior so concurrent proxy and Studio callers join one artist job.
- Persist admission before external work; validate terminal/cooldown state before queueing; detach accepted work from request cancellation under a maximum 90-second service timeout.
- Run identity resolution, ingestion, evidence preparation, conditional search, AI roles, and atomic commit in order, preserving old output on every failure.
- Store only bounded error classes and timing/outcome metadata.

Checkpoint: concurrency, restart, cancellation, full queue, cooldown, quota, and no-data tests prove no request storms or duplicate external calls.

### 6. Proxy cache-miss integration and configurable wait

- Extend the current biography reader boundary with the smallest coordinator trigger/wait contract needed by `getArtistInfo2`.
- Admit work only after a valid authenticated artist request and successful upstream artist response; never copy credentials, request data, or user identity into jobs.
- On a miss, enqueue/join, wait for `BIO_FIRST_REQUEST_WAIT`, re-read once when notified, and otherwise return the safe upstream response while the job continues.
- Parse only 5–30 seconds, provisionally default to 20 seconds, and keep provider/search/AI work outside the request goroutine.

Checkpoint: proxy tests prove bounded latency, exact artist isolation, one admission, pass-through on every failure, and immediate cache hits.

### 7. Studio per-artist rebuild

- Add `Пересобрать описание` and status display to the existing artist biography detail UI/API.
- Reuse current authentication, CSRF, method, ID validation, and store wiring.
- Invalidate only the selected artist's derived evidence/cooldown, preserve its manual and last accepted automatic text, enqueue one job, and poll bounded status.
- Keep global reset as invalidation only; it must not enqueue all artists.

Checkpoint: handler/UI tests prove authorization, CSRF safety, selected-artist scope, idempotent joining, manual preservation, and no global fan-out.

### 8. Release verification and latency calibration

- Run at least five cold runs for each class: catalog-rich, web-required, multilingual, no-data, and quota/retry failure.
- Capture stage and total `p50/p90/p95` without content, secrets, or prompts.
- Set the release default to `clamp(total p90 + 2s, 5s, 30s)` and verify supported Subsonic clients tolerate it; otherwise choose the safe lower client limit and rely on background completion.
- Manually exercise Tavily and current catalog endpoints, then run focused/full/race/vet/build/vulnerability/diff checks.

Checkpoint: the chosen default and measurements are recorded in the handoff, and all security/regression gates pass.

## Implementation Order

1. Persistent lifecycle and legacy cleanup.
2. Deterministic evidence preparation.
3. Tavily/key-pool integration.
4. AI contracts.
5. Coordinator and synthesis orchestration.
6. Proxy cache-miss trigger/wait.
7. Studio per-artist rebuild.
8. Integration, live endpoint checks, security audit, and latency calibration.

Storage contracts precede orchestration so accepted output is never partially visible. Pure deterministic/AI contracts precede runtime wiring. Live endpoint tests and timing calibration happen only after offline correctness and security tests pass.

## Reuse Decisions

- Reuse `artistbio.Store`, identity resolver, ingestion sources, language helpers, provider HTTP safeguards, and candidate validation.
- Reuse `cache.SingleFlight` semantics, adapting it only if its result shape cannot represent detached job status safely.
- Reuse `ai.TextGenerator`, Groq/Gemini fallback, strict JSON parsing, key pool, encrypted Studio key storage, rate-limit reporting, and logger redaction.
- Reuse existing proxy `getArtistInfo2` capture and JSON/XML rewriting; add no plugin or second proxy.
- Reuse current Studio biography handlers/templates and global reset path.
- Add no SDK, browser scraper, message broker, vector database, embedding layer, or generic job framework.

## Risks and Mitigations

| Risk | Mitigation |
|---|---|
| First request exceeds client timeout | Configurable 5–30-second wait, measured release default, fail-open upstream, detached 90-second job |
| Same artist causes a cost storm | Persistent terminal/cooldown state, bounded queue, keyed singleflight, admission after successful upstream response |
| Model invents or misattributes facts | Supplied-ID-only contracts, exact claim mapping, separate verifier, whole-summary rejection |
| Search creates SSRF or prompt injection | Search API records only, no arbitrary page fetch, HTTPS/private-network policy, source text treated as inert JSON |
| Free quotas are exhausted | Conditional search, compact inputs, role-specific quotas, one pipeline, three starts/minute, retry-after cooldown |
| Rebuild erases useful text | Mark stale but retain last accepted result; atomic replacement only after full verification |
| Restart repeats work | Persist admission/state/version before external work and recover stale jobs without inventory fan-out |
| Legacy local filler remains visible | Idempotent cleanup of `local_library` selections/candidates with regression tests |
| Logs leak copyrighted/source content or secrets | Timing/outcome codes only; explicit redaction tests and error review |
| Feature duplicates existing code | Extend narrow current components and interfaces; no parallel store, HTTP stack, key manager, or parser framework |

## Verification Checkpoints

1. Store/state: focused schema, terminal-state, precedence, cleanup, restart, and atomicity tests.
2. Evidence/search: deterministic hash/bounds plus Tavily HTTP, SSRF, quota, cache, and redaction tests.
3. AI: strict JSON, attribution, multilingual grounding, injection, output bounds, and outage tests with fake generators.
4. Coordinator: singleflight, queue, pacing, cooldown, cancellation, persistence, and race tests.
5. Delivery/Studio: end-to-end proxy miss/hit/wait and authenticated per-artist rebuild tests.
6. Live/manual: Tavily and applicable catalog endpoints, free-tier failure behavior, and representative latency suite.
7. Completion: formatting, focused/full/race tests, vet, production build, vulnerability scan, `git diff --check`, secret/security review, and unrelated-file audit.

## Out of Scope

- Source-free AI biographies or model knowledge as evidence.
- Automatic refresh after terminal `ready`/`no_data` without a user rebuild.
- Immediate processing of every library artist after startup or global reset.
- Arbitrary page fetching, browser scraping, paid search by default, or automatic plan upgrades.
- Native Navidrome API enrichment beyond the existing Subsonic `getArtistInfo2` path.
- Making manual descriptions mandatory.
