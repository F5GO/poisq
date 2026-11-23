# Spec: metadata-merge-policy

Module: `metadata-merge-policy`
Depends on: `metadata-core`, `local-metadata-import`

## Objective

Provide one deterministic, field-level merge engine for legacy/Navidrome, embedded audio, NFO, MusicBrainz, derived, and manual metadata.

The module must:

- preserve locked fields unconditionally;
- distinguish an absent field from a present zero/false value through `FieldStates`;
- support `missing_only`, `priority`, `newer`, and `never` refresh modes per source/provider;
- apply configured source/provider priority, enabled state, and minimum confidence;
- produce the same result regardless of candidate order;
- retain provenance, confidence, provider, and update time from the winning candidate;
- persist and validate global provider policy without arbitrary JSON or unknown sources;
- expose safe field lock/unlock operations;
- make local audio reconciliation use the common merge engine.

## Merge Contract

`Merge(current, candidates, policy)` validates every candidate and evaluates only fields explicitly present in its `FieldStates`. A current locked field always wins. Disabled, below-threshold, track-mismatched, or `never` candidates are ignored.

For `missing_only`, a candidate may fill only a field absent from the current document. For `newer`, it must be newer than the current field timestamp. For `priority`, the lowest configured priority wins; confidence, timestamp, provider name, and a stable value key break ties. Manual and locked values are protected by the default policy.

Collection fields are atomic ordered values. The engine does not concatenate provider results implicitly.

## Persistence

Add an additive migration for `metadata_provider_policy` keyed by source/provider, with priority, refresh mode, enabled flag, and minimum confidence. Empty provider means a source-wide fallback rule. Repository replacement is transactional and bounded.

## Commands

```sh
gofmt -w internal/musicmeta/merge*.go internal/db/metadata_policy*.go internal/metadata/local_importer.go
go test -count=1 ./internal/musicmeta ./internal/db ./internal/metadata
go test -race -count=1 ./internal/musicmeta ./internal/db ./internal/metadata
go test -count=1 ./...
go vet ./...
go build ./cmd/proxy
```

## Boundaries

- Never overwrite a locked field.
- Never infer presence from a zero value when no field state exists.
- Never use candidate input order as a tie breaker.
- Never permit unknown sources, fields, refresh modes, NaN confidence, or unbounded policy rows.
- Do not mutate input documents.

## Success Criteria

1. Locks, missing-only, priority, newer, disabled, and confidence rules have focused tests.
2. Candidate permutation produces the same result.
3. False/zero values can win when explicitly present.
4. Policy persists transactionally and migrates idempotently.
5. Local import uses this engine and retains previous behavior.
6. Focused, race, full, vet, and build checks pass.
