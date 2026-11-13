# Implementation Plan: `bio-validation`

Source of truth: [`SPEC-bio-validation.md`](../SPEC-bio-validation.md).

## Approach

Extend the existing `artistbio` background pipeline with a fail-closed validator. It first applies bounded deterministic text checks, accepts clearly Russian source prose without an AI call, and sends only ambiguous language or real non-Russian source text through the existing Groq/Gemini text-generation chain. Translation is a one-to-one transformation of numbered source segments, never a biography-generation fallback.

The validator writes language and translation evidence back to the existing candidate row, then selects the best accepted candidate in the same database transaction. Client delivery and Studio editing remain separate downstream modules.

Before this module can be considered release-safe, pin all build paths to Go 1.26.11 and rerun `govulncheck`; older 1.26 patch releases have reachable standard-library vulnerabilities in the current vulnerability database.

## Dependency Graph

```text
Go 1.26.11 release baseline
            ↓
bounded deterministic language analysis
            ├──→ direct-Russian validation
            └──→ strict AI language fallback
                         ↓
numbered source segmentation → strict translation → grounding checks
                         ↓
candidate validation/scoring → atomic automatic selection
                         ↓
background ingestion integration
```

## Components

### 1. Secure release baseline

- Pin `go.mod`, Docker builder, and GitHub Actions to Go 1.26.11 rather than a floating or older toolchain.
- Keep `GOTOOLCHAIN=auto` only as compatibility behavior; the required patch version remains explicit.
- Run the full suite and `govulncheck` after the pin.
- Do not update unrelated dependencies or deployment images.

Checkpoint: build reports Go 1.26.11 and `govulncheck ./...` reports no reachable known vulnerability.

### 2. Deterministic language and text-quality analysis

- Add a pure bounded analyzer for Unicode script shares, meaningful character count, Russian/Ukrainian/Belarusian distinctive letters, and small fixed function-word sets.
- Normalize only whitespace for analysis; preserve stored source prose.
- Exclude URLs, email addresses, markup, punctuation, and numbers from language evidence.
- Reject very short, repetitive, markup-heavy, promotional, or oversized text before any model call.
- Return only allowlisted language codes, confidence, Russian share, and compact stable evidence codes.

Checkpoint: table-driven tests cover Russian, Ukrainian, Belarusian, mixed Cyrillic/Latin, English, short, repeated, markup, and adversarial inputs without network or AI.

### 3. Strict AI language fallback and translation contract

- Reuse the existing `ai.TextGenerator` composition and fallback order; add no model client or dependency.
- Invoke AI language classification only for deterministic ambiguity.
- Split a real source into at most 120 bounded numbered segments and serialize untrusted prose as JSON user data.
- Require strict JSON with exactly one translated result for every supplied ID, no extra IDs or trailing content.
- Reject fenced, malformed, oversized, reordered, missing, duplicated, or non-Russian output.
- Preserve all numbers, dates, URLs, email addresses, identifiers, and quoted names deterministically.
- Ask the existing model chain for a strict grounding verdict only when deterministic preservation checks cannot establish safety.
- Never log source prose, translated prose, prompts, responses, or credentials.

Checkpoint: fake-generator tests prove prompt isolation, exact ID mapping, preservation checks, fallback behavior, cancellation, response bounds, and hallucination rejection.

### 4. Store operations for validation and atomic selection

- Reuse the existing candidate table and status model; add only the minimal query/update methods missing from `Store`.
- List candidates in deterministic bounded pages and validate that each still belongs to the requested artist.
- Persist actual language, Russian text, confidence, evidence, status, and timestamps without overwriting original text.
- In one transaction, update the candidate and choose the highest accepted automatic candidate using deterministic ordering.
- Preserve the previous valid selection during transient provider/AI failure and never alter manual overrides.
- Use parameterized SQL and bounded fields throughout.

Checkpoint: store tests cover atomicity, refresh, rollback, deterministic ties, stale candidates, manual precedence, and concurrent writers.

### 5. Validator orchestration and scoring

- Build a small validator service around pure language, translation, scoring, and store interfaces.
- Coalesce concurrent validation per artist and share a bounded global semaphore with background work.
- Accept clear Russian directly; translate only a real non-Russian candidate; mark uncertain or unsafe candidates rejected/retryable with stable codes.
- Score identity strength before source preference or text length; prefer direct Russian over an otherwise equal verified translation.
- Do not fabricate a fallback when every candidate fails.

Checkpoint: service tests cover mixed provider outcomes, outages, cancellation, concurrency, direct-Russian fast path, translated acceptance, and no-result behavior.

### 6. Background integration

- Invoke validation after successful ingestion for the same bounded artist batch.
- Reuse the existing background scheduler, lifecycle, candidate cache, AI generator, and shutdown context.
- Skip artists whose selected automatic result and candidates are still fresh; retain retryable state for failures.
- Add no provider or AI call to the synchronous Subsonic request path.

Checkpoint: integration tests prove ingestion-to-selection flow, cache skips, bounded retries, clean shutdown, and zero validation calls from `getArtistInfo2`.

## Implementation Order

1. Pin and verify Go 1.26.11.
2. Implement deterministic language/text-quality analysis.
3. Implement strict AI contracts with fake generators.
4. Add minimal transactional store operations.
5. Implement validator scoring and orchestration.
6. Wire validation into the existing background batch.
7. Run security, race, regression, and leakage audits.

The deterministic analyzer comes before AI so the common Russian path remains fast and testable. Store operations follow the translation contract so persistence represents the final validated states rather than intermediate model output. Runtime composition remains last.

## Reuse Decisions

- Reuse `artistbio.Store`, candidate/status types, provider reliability data, and existing background processor.
- Reuse `ai.TextGenerator` and the current Groq/Gemini fallback ordering.
- Reuse existing concurrency, cancellation, body-size, redaction, and evidence-bounding conventions.
- Keep language signals as small static tables in code; add no NLP, translation, embedding, cache, queue, or worker dependency.
- Keep translation in the original candidate row; do not create a parallel cache or generated-biography table.

## Scoring Order

Automatic candidates are compared deterministically by:

1. stronger artist-identity confidence;
2. accepted direct Russian over an otherwise equal verified translation;
3. higher measured Russian-language confidence/share;
4. explicit stable source reliability tier;
5. useful bounded text quality/length;
6. freshness, then stable candidate ID as final tie-breakers.

Manual overrides are outside this ranking and remain higher priority at read time.

## Risks and Mitigations

| Risk | Mitigation |
|---|---|
| Ukrainian or Belarusian text mislabeled Russian | Distinctive-letter and function-word evidence; ambiguous Cyrillic requires strict fallback or rejection |
| Model adds facts while translating | Exact numbered mapping, deterministic entity preservation, strict grounding verdict, fail closed |
| Prompt injection in source biography | Biography remains JSON user data; allowlisted response schema and IDs only |
| AI/provider outage erases a working biography | Transaction retains previous accepted selection and leaves failed work retryable |
| Longer weak candidate outranks exact identity | Identity confidence precedes provider and length scoring |
| Validation monopolizes background sync | Bounded candidates, segments, concurrency, batches, and per-operation contexts |
| Sensitive prose or keys leak | Generic errors and explicit leakage tests; no body/prompt/response logging |
| Security scan varies with local toolchain | Exact Go 1.26.11 pin in module, Docker, and CI |

## Verification Checkpoints

1. Baseline: Go 1.26.11 is used by local/module, Docker, and CI configuration; official vulnerability scan is clean.
2. Language: focused pure-function tests and fuzz/bounds cases.
3. AI boundary: fake-generator contract, injection, malformed-output, and leakage tests.
4. Store: transactional and concurrent selection tests under the race detector.
5. Integration: background ingestion-to-validation without request-path work.
6. Completion: `go test -count=1 ./...`, `go test -race -count=1 ./...`, `go vet ./...`, production build, `govulncheck ./...`, formatting, and `git diff --check`.
7. Diff audit: no unrelated iOS/image changes, raw biography logging, duplicated infrastructure, new dependency, or source-free generation.

## Out of Scope

- Rewriting Subsonic JSON/XML responses (`bio-delivery`).
- Studio manual-override API or UI (`studio-overrides`).
- Summarizing, expanding, or generating biographies without a real source.
- Guaranteeing a biography when every real source is empty.
- Changing provider identity matching or adding providers.
