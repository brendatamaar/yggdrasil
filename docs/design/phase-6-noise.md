# Phase 6 noise proposals — Model Functional Effect design
Profile: service

WS-09. Analysis belongs to `internal/core/repos`, collection to `internal/repos`,
confirmed writing to `internal/proposals`. Do not repurpose protected AgentFile.

## Shapes and state map

`NoiseFacts{UntrackedCount,Sample,Truncated}` derives from existing bounded status.
A candidate has `Directory`, `SampleCount`, `Pattern`, `Evidence` and `Incomplete`.
Choose a conservative default: total untracked count at least 100; at least 50
sampled files under one allowlisted generated directory (`node_modules`, `.venv`,
`dist`, `build`, `target`, `bin`, `obj`, `__pycache__`), at least half the sample
under that directory. Never suggest a broad `*`, source directory or individual
unknown file. The UI labels sampled evidence; no recursive filesystem scan occurs
on dashboard refresh. A nested pattern is anchored to its exact repo-relative path;
escape gitignore metacharacters, reject newline/control/unsupported path forms.

Three analysis states: `NoSuggestion`, `Suggested{Candidates,Evidence}`,
`AwaitingConfirmation{ProposalID}`. Proposal lifecycle remains the existing one.
`IgnoreEntries{Where,Entries,ExpectedHash,ExpectedAbsent}` is a narrowly scoped
`.gitignore` append action; it cannot name a different file. Absence is explicit,
not overloaded with empty-file hash. ExpectedHash and Entries are hash-covered.

## Inputs and transition table

Inputs: `Observed{Facts}`, `Selected{Entries,CurrentHash,Absent,ProposalID}`,
`ProposalDecided{ID,Status}`, `ScopeRemoved`.

| From | Input / guard | To | Commands |
|---|---|---|---|
| NoSuggestion/Suggested | Observed | classify to Suggested or NoSuggestion | none |
| Suggested | Selected, nonempty subset of current candidates | AwaitingConfirmation | CreateIgnoreProposal |
| AwaitingConfirmation | ProposalDecided matching ID | NoSuggestion | RefreshStatus |
| any | ScopeRemoved | NoSuggestion | none |

Other pairs unchanged/no commands. Observations never write. New observations after
selection cannot edit an immutable proposal; create a new proposal when stale.

## Commands and call graph

| Command | Cardinality | Idempotent | Produces |
|---|---|---|---|
| CreateIgnoreProposal | one, RequestID-bound | yes with action request key | proposal ID |
| RefreshStatus | one | yes | Observed |
| AppendIgnore (confirmed proposal only) | one local write | with content precondition | executed/stale/unknown |

```go
// Production
status reader → NoiseFacts parser → NoiseStep (core) → candidate DTO
user selects → proposal create/hash → UI confirmation
  → mainlock → tracked/root recheck → no-follow file handle → compare bytes/hash
  → append exactly missing entries, preserve prefix and newline style → audit/result
// Tests
NoiseStep generated samples → properties
proposal worker → temporary repo/real filesystem + fake locking/fault injection
```

Use the existing guarded file-write pattern only after checking it preserves a
no-follow, same-file precondition through the write. Missing `.gitignore` uses
exclusive creation. Existing file uses a safely opened handle and verified identity;
concurrent external changes must be detected, never overwritten. Platform capability
owns Windows handle/reparse behavior. If safe append cannot be guaranteed by the
current abstraction, add that capability in foundations, not an OS check in executor.
Do not implement rename-overwrite that deletes an existing file as a side effect.

## Failures

| Node | Failure | Class | Handling |
|---|---|---|---|
| Classifier | insufficient/truncated evidence | domain outcome | no suggestion or explicitly incomplete candidate |
| Writer | stale bytes, symlink/reparse, untracked, locked | domain outcome | refuse; offer refresh/retry only with unchanged binding |
| Writer | lost result after write | domain outcome | compare original prefix and exact resulting bytes on recovery |
| Collector | read failure | escape hatch | keep ordinary status view, labelled unavailable noise |
| Core/parser | invalid domain value | die | stop operation; no write |

## Requirements and boundary

`NoiseSource`: real bounded git status / sample fake; contract counts vs truncated
samples and no expensive scan. `GuardedIgnoreWriter`: platform handle-safe append /
scripted fake; contract absent/empty/CRLF/unicode/metacharacters/stale/locked/reparse,
identical resulting bytes on repeat. `ProposalStore`: existing SQLite/fake contract,
state/audit atomic and wrong hash refuses. `MainLock`: existing shared registry.

Parsers: status sample→valid relative paths; selected Entries→nonempty bounded
subset (max 32 entries, 4 KiB total), reject traversal/newline/absolute/ADS;
file bytes/hash→expected snapshot with max 1 MiB file, preserve raw bytes;
DB action→validated IgnoreEntries, reject unsupported target or altered hash.

## Invariants and files

`TestPropNoiseRequiresThreshold`, `TestPropNoiseNeverSuggestsSourceOrWildcard`,
`TestPropTruncatedEvidenceNeverComplete`, `TestPropIgnorePreservesPrefix`,
`TestInvariant_2_IgnoreOnlyAfterConfirm`, `TestIgnoreStaleReparseAndCRLF`.
Recovery never appends twice; an unknown/partial file write is reported for manual
inspection, never repaired by deleting or replacing user content.
Files: `core/repos/noise*.go`, `repos/noise*.go`, `core/proposals` IgnoreEntries
variant/hash/Describe, `proposals/exec_ignore.go`, shared platform writer capability.

## Open questions

Threshold and allowlist above are concrete F0 defaults; product tuning may change
them later through ordinary reviewed config. No safety rule is weakened.

## Implementation and verification gate

Design status: approved by the human; see `docs/phases/phase-6.md`.
No implementation or migrations are part of this document. Shared types, schema,
queries, capability declarations, DTOs and generated files land together on main
before any implementation fan-out. Consumer-local interfaces take `context.Context`;
core `Step(state, input) (State, []Command)` has no effects. All sum types are sealed.
Time, IDs and resolved facts arrive as input. Unhandled state/input combinations
return the original state and no commands; result inputs carry an attempt/revision.

The lead derives a test map after implementation merges; test-engineer implements
it in the scheduled package handoff, including evidence that each test can fail.
Every parser listed below needs round-trip (for representable domain values),
accepted-implies-valid, one test for each rejection rule and Go fuzz coverage.
Every fake capability shares a contract suite with the live SQLite, filesystem,
platform or recorded-HTTP implementation; scripted failure and duplicate cases
are clauses of the suite. Automated tests use fictional temporary repos, fake
hosts and the mock agent only. All five required `task` checks must pass; Windows
and real-host/real-agent manual checks remain explicitly separate.
