# Implementation Plan: `nfo-interoperability`

Source of truth: [`SPEC-nfo-interoperability.md`](../SPEC-nfo-interoperability.md).

1. Add bounded XML DTOs, parsing, mapping, and deterministic rendering.
2. Build folder import through normalized merge and `nfo_import` proposals.
3. Build folder export as `nfo_export` proposals.
4. Extend transactional writeback to safe `.nfo` files and exact Undo/recovery.
5. Add security, round-trip, merge, apply/Undo, and regression tests.
