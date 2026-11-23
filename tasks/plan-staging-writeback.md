# Implementation Plan: `staging-writeback`

Source of truth: [`SPEC-staging-writeback.md`](../SPEC-staging-writeback.md).

## Components

1. Add durable writeback transaction migration and repository.
2. Add safe backup/hash/restore primitives to the atomic writer.
3. Add bounded ffprobe post-write tag verification.
4. Journal tag and cover applies, commit status atomically, and reconcile DB.
5. Replace logical Undo with exact byte restore and add startup recovery.
6. Run focused, race, full, vet, build, and diff checks.
