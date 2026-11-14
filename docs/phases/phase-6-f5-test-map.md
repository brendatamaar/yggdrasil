# F5 review repair test map

Owner: one test-engineer, tests/helpers only in internal/git and internal/platform.
PRD AU-02/AC-04, NR-04 support; safety invariants 3/4/5/6. Feature agents stopped.
No lead production edits while test ownership is active.

| Contract / defect | Evidence required |
|---|---|
| Large-output fixture works on Windows | Put large commit messages in a temp file, invoke git commit -F path; preserve >1 MiB output and partial-record assertions without a long command line. |
| Overflow does not hide natural failure | Deterministic injected-start contract: overflow stdout, natural nonzero exit + nil Wait error, expect git.Error with code/redacted stderr. Include genuine Wait error, caller cancel, successful exit and unknown termination reporter cases. |
| Cutoff-induced kill is distinct | Real platform processes and real Git overflow verify WasTerminated only for actual Kill termination; natural failure/success plus late Kill while output drains remains false. Exercise Unix and Windows process-job semantics without OS branching outside platform. |
| Runner limits/error privacy | Direct bounded/redacted stderr, stdout overflow success/failure, platform pipe-cutoff metadata, cancellation and optional-lock/clean-env tests; retain existing bounded-read behavior. |
| Independent accepted-implies-valid | Test-only primitive identity/parent/date/framing predicate does not call validOID or production parser as oracle. Add SHA-1/SHA-256 full-field round trips, empty/mismatched short-ID and bad-parent rejection cases. |
| Assertions detect faults | Restore controlled mutations for ignored nonzero code, lost termination evidence/false evidence, weakened identity/parent rules and upper-bound/completeness conditions. Compiler failures are not assertion evidence. |

Retain existing mutation, range/order/old-history/date-source tests. Use temp
fictional repos and process fixtures; no real host/model/agent calls. Run scoped
normal/race/fuzz and Windows cross-compile checks, all five actual Task commands.
Report Windows execution honestly if Linux-only. Do not modify production,
queries, generated files, Taskfile, dependencies or any other package. Temporary
controlled source mutations must be restored exactly before final validation.
