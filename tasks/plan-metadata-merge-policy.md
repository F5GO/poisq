# Implementation Plan: `metadata-merge-policy`

Source of truth: [`SPEC-metadata-merge-policy.md`](../SPEC-metadata-merge-policy.md).

## Approach

Build a pure merge engine in `internal/musicmeta`, add a normalized policy repository, expose lock operations on the metadata repository, then route local-file reconciliation through the engine.

## Components

1. Define refresh modes, provider rules, defaults, validation, field presence and stable comparison.
2. Implement order-independent field selection and copying for every normalized field.
3. Persist provider policy with an additive migration and transactional replacement.
4. Add field lock/unlock repository operations.
5. Replace local import overlay and cover regressions/permutations with tests.

## Verification

Run the commands in the module spec, followed by `git diff --check`.
