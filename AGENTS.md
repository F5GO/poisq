# Project Engineering Rules

These rules apply to every change in this repository.

1. Reuse existing code and abstractions whenever they fit the requirement. Add new code only when the current implementation cannot be safely extended.
2. Optimize latency, throughput, allocations, I/O, and external calls on critical paths without weakening security, correctness, maintainability, or existing functionality. Measure or benchmark when an optimization is not self-evident.
3. Keep code simple, precise, idiomatic, and consistent with the surrounding project. Prefer small focused units, explicit contracts, bounded resource use, and clear error handling over speculative abstractions.
4. Design every change with secure defaults. Validate and bound all external input, preserve authorization boundaries, avoid secret or personal-data leakage, use parameterized database operations, and review new code for injection, SSRF, path traversal, request smuggling, unsafe redirects, decompression/resource exhaustion, denial of service, races, and dependency risks as applicable.
5. Add or update tests for normal behavior, failure behavior, boundary conditions, and relevant abuse cases. Run the smallest focused test set during development and the complete applicable suite before handoff.
