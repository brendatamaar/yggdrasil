# Phase 6 Standup — Model Functional Effect design
Profile: service

AU-02/UI-03. `internal/core/standup` formats deterministic facts;
`internal/standup` reads bounded sources. No scheduler or autonomous model call.

## Shapes and state map

`Range{ProjectID,From,To}` is UTC, half-open, at most 31 days; default is current
local day boundaries supplied by UI and validated server-side. Summary has typed
commit/MR/build sections, source counts and unavailable/truncated annotations.
At most 50 repos, 200 records per source, 64 KiB final text; stable order is
repo name, event timestamp, source identity. Never invent a missing source's text.

Five states: `Empty`, `Loading{Attempt,Range}`, `Ready{Summary}`,
`Partial{Summary,Unavailable}`, `Failed{Reason}`. Polish is an explicit action
submission using the existing task RequestID path; no separate agent state machine.

## Inputs and transition table

Inputs: `Requested{Range,Now}`, `SourcesRead{Attempt,Commits,MRs,Builds,Unavailable}`,
`ReadFailed{Attempt,Reason}`, `Cancelled{Attempt}`, `PolishRequested{RequestID}`.

| From | Input / guard | To | Commands |
|---|---|---|---|
| any | Requested valid range | Loading(a+1) | ReadSources |
| Loading(a) | SourcesRead(a), at least one source readable | Ready/Partial (empty summary allowed) | ReturnSummary |
| Loading(a) | ReadFailed(a), no source readable | Failed | Explain |
| Loading(a) | Cancelled(a) | Empty | CancelRead |
| Ready/Partial | PolishRequested, text nonempty | unchanged | SubmitAskDraft |

Other pairs unchanged/no commands. At most one latest read response wins; a new
request cancels prior IO. Empty data is not failed loading. Copy is a user-gesture
clipboard command on the displayed text, not a server-side effect.

## Commands and call graph

| Command | Cardinality | Idempotent | Produces |
|---|---|---|---|
| ReadSources | one bounded aggregate, 20s total | yes | SourcesRead/ReadFailed |
| ReturnSummary/Explain | one | yes | API response |
| CancelRead | one | yes | none |
| SubmitAskDraft | one explicit task submission | RequestID-bound | ordinary task ID |

```go
// Production
GET standup → ParseRange (boundary) → tracked project/repos
  → ReadSources(repo history, cached hosts, build history)
  → parse facts → standup.Step/Format (core) → DTO text/sections
user polish → action request dedup → tasks.Message(Ask, source-labelled text)
// Tests
Step/Format → generated fact sequences
service → fake sources + SQLite builds + temporary git histories + recorded host cache
```

Polish is a normal visible Ask task started by explicit user submission (the
command name SubmitAskDraft denotes its prepared brief; dispatch still uses the
ordinary action/task path). A refresh never starts an agent. Input text is bounded
so the polish request fits the task prompt size limit. Project scope contains only
tracked ready repos; status changes during collection are revalidated before task
submission. The summary itself is not a notification payload.

## Failures and requirements

| Node | Failure | Class | Handling |
|---|---|---|---|
| Range/scope | inverted/oversized range, untracked project | domain outcome | plain refusal |
| Commit/MR/build source | some unavailable | escape hatch | Partial with source labels |
| Source | all unreadable/timeout | domain outcome | Failed, explicit retry |
| Polish | admission/trust/adapter unavailable | domain outcome | show ordinary task error, summary retained |
| Formatter | impossible domain variant | die | no fabricated output |

`StandupSource` methods are consumer-local: commits from repos/git, MR facts from
cached hosts, builds from store; each uses the corresponding live/fake contract,
including bounds, time filtering, missing source and cancellation. `TaskSubmitter`
is the existing RequestID-aware capability; tests prove no call on refresh.

## Boundary, invariants and files

Parsers: API Range→UTC validated range/ProjectID; source rows→bounded typed events
(reject malformed timestamp/foreign repo/invalid status); DB provenance→valid
identity; polish request→validated RequestID/agent/model, no arbitrary widened scope.

`TestPropStandupDeterministic`, `TestPropStandupWithinBounds`,
`TestPropStandupNoMissingSourceFabrication`, `TestStandupRefreshNeverCallsAgent`,
`TestStandupPolishExplicitAsk`, `TestStandupDateRangeAndEmpty`.
Files: `core/standup/*.go`, `standup/*.go`; F3 standup contracts, U independent
`web/src/widgets/standup` plus shared feature hooks; widget cannot import its peers.

## Open questions

Proposed defaults above are explicit; polishing does not rewrite the saved raw
summary and there is no automatic cost-bearing work.

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
