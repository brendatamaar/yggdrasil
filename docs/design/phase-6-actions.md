# Phase 6 workspace actions — Model Functional Effect design
Profile: distributed

AC-01–06; GH-05/06; supports WS-09 and AU-02. The catalogue is pure in
`internal/core/actions`; `internal/actions` interprets it. Reuse existing tasks,
proposals, git batches, builds and verification; no new task queue or agent loop.
This feature needs distributed handling for duplicate submissions and MR posting;
read-only host context stays service-profile as specified separately.

## Shapes and state map

`ActionKind` is DiagnosePipeline, FixPipeline, ReviewMR, ProposeCommits,
PullAndCheck, ImportIssue, DiagnoseBuild, DiagnoseRepo, or ProposeIgnore.
`ActionRequest{RequestID,RepoID,Kind,SourceID,Agent,Model}` is bounded, scoped and
server-validated; its digest binds the idempotency key. A reused key with a
changed digest is rejected. `ActionFacts` has repo readability/status, CI/build
identity and capabilities. `Catalogue(facts)` returns typed offerings plus reasons.

Seven dispatch states: `Idle`, `Preparing{Attempt}`, `Ready{Context,ExpectedHead}`,
`Submitting{Attempt,Key}`, `Completed{TaskID,ProposalIDs,BatchID}`,
`Refused{Reason}`, `Cancelled`. A completed import refers to a **draft** task.
DraftTask→Queued is an explicit existing task submission, not an import transition.

MR comment actions extend the existing proposal lifecycle with
`ReviewComment{Repo,MRNumber,ExpectedHead,Body,Marker}`. Marker is generated in the
shell before proposal creation, immutable and hash-covered; render the exact body
and destination in Describe. Comment posting uses a durable per-proposal effect
record; it does not add an alternative confirmation path.

## Inputs

`Requested{Request,IDs,Now}`, `Prepared{Attempt,Facts,Context}`,
`PrepareFailed{Attempt,Reason}`, `SubmitRequested`,
`Submitted{Attempt,Result}`, `SubmitFailed{Attempt,Outcome}`, `Cancel`,
`Recovered`, `Reconciled{Attempt,Outcome}`. Confirmation/rejection of resulting
proposals belongs to existing `core/proposals.Step`, not this dispatcher.

## Transition table

| From | Input / guard | To | Commands |
|---|---|---|---|
| Idle | Requested, valid tracked scope | Preparing | LoadFactsAndContext |
| Preparing(a) | Prepared(a), offering still valid | Ready | PresentPreview |
| Preparing(a) | failure / no valid offering | Refused | Explain |
| Ready | SubmitRequested, same version/scope | Submitting | PersistIntent, Dispatch |
| Submitting(a) | Submitted(a) | Completed | PersistResult, Audit, Publish |
| Submitting(a) | definite refusal | Refused | PersistResult, Audit |
| Submitting | unknown result or Recovered | unchanged | ReconcileDispatch |
| Submitting(a) | Reconciled(a, found) | Completed | PersistResult, Publish |
| Submitting(a) | Reconciled(a, definitely absent) | Refused | Explain retry |
| Idle/Preparing/Ready | Cancel | Cancelled | CancelPreparation |

All other pairs: unchanged, no commands. RequestID lookup returns completed
results without redispatch. Cancel after submission cannot roll back an admitted
task; user cancels that task through the existing endpoint. Retrying an uncertain
submission first reconciles its durable task/proposal link, never starts another.

## Catalogue, modes and effects

| Source | Offering | Effect after explicit submission |
|---|---|---|
| Failed CI | Diagnose with agent | Ask task + bounded redacted logs, source/head pinned |
| CI diagnosis | Fix in worktree | new Code task, never silently re-mode existing Ask task |
| Failed local build | Diagnose build | Ask + bounded build log |
| Open MR | Review with agent | Ask + pinned diff, drafts comment proposals |
| Dirty | Propose commits | Ask analysis of exact current diff, explicit-path commit proposals |
| Behind | Pull and check | confirmed `PullFF{Repo,ExpectedHead,Upstream,Verify}` proposal; existing git service only |
| Issue | Add draft task | ordinary draft task with source identity; no process/worktree |
| Conflicts/divergence/unreadable | Diagnose repo | Ask with readable tracked facts; never reset/merge/delete |
| Large untracked count | Review ignore suggestions | bounded noise context and confirmed IgnoreEntries proposal |

All Needs attention reasons have a useful Ask fallback if host details or a
filesystem read is unavailable. For an unreadable repo, the task may only receive
tracked metadata already available; task admission must never bypass a missing
working-directory requirement. If no repo can be safely read, preserve the draft
and explain what must be restored before running; the row must still prepare a
meaningful diagnosis brief, not just navigate to details. Never fabricate a
successful agent run for an unavailable filesystem.

Commit grouping does not change user files. Proposals capture each selected path,
HEAD and diff hash; acceptance of one group makes stale overlapping proposals
refuse/recompute. Pull checks dirty/diverged/untracked/locked state and returns a
reason; it never falls back to a merge. Optional verification runs only after
confirmed successful pull through existing verifier/build capability.

## Commands and call graph

| Command | Cardinality | Idempotent | Produces |
|---|---|---|---|
| LoadFactsAndContext | one bounded aggregate | yes | Prepared/failure |
| PresentPreview/Explain | one | yes | response |
| PersistIntent/Result, Audit, Publish | one | yes, RequestID+revision | response/event |
| Dispatch | one task/draft or bounded set of proposals | with RequestID in same transaction as links | Submitted |
| ReconcileDispatch | one read | yes | Reconciled |
| PostReviewComment (proposal worker only) | one remote write | no native guarantee | posted/unknown |
| FindReviewComment | bounded paginated read by immutable marker | yes | found/absent/unknown |

```go
// Production
widget/repo/palette → API parser → catalogue/Step (core)
  → action service → scoped facts + host context
  → Store.Commit(state + intent + audit + outbox)
worker → Dispatch → tasks draft/Message or proposals.Create
proposal confirmation → proposal worker → recheck MR head → host PostReviewComment
// Tests
Step sequences → properties
service → SQLite + fake tasks/proposals + recorded hosts
worker → scripted crash points before/after external write
```

## Persistence and delivery

`workspace_action_requests` stores RequestID, request digest, tracked RepoID,
state, version, task/proposal result links and audit. Outbox rows are committed
with intent and drained by a context-owned worker; compare expected version when
claiming/results return. Producer key is the UI RequestID (random generated once
per submission); results are keyed by request+attempt. Keep keys while the linked
task/proposal history exists; no timer may silently discard an unresolved intent.

For comments, persist proposal/effect ID, marker, expected MR head and immutable
body hash before POST. Mark executed only with host comment ID. On lost reply or
restart, GET matching marker/body/head: exactly one match succeeds; conflict or
incomplete pagination remains Unknown and blocks resend. Definite absence still
needs explicit human retry. No automatic POST retries. Host unavailability cannot
be treated as proof of absence. Late old-attempt results cannot run another effect.

## Failures and requirements

| Node | Failure | Class | Handling |
|---|---|---|---|
| Catalogue/admission | changed scope, unsupported mode, lock/trust/HEAD | domain outcome | refuse, retain preview/context as safe |
| Optional logs | unavailable/token missing | escape hatch | labelled metadata-only Ask context |
| Store | transient busy | retry | bounded transaction retry before effects only |
| Task/proposal submission | timeout after admission | domain outcome | reconcile durable request link, never blind repeat |
| Comment POST | timeout / lost reply | domain outcome | Unknown; read-only reconciliation |
| Core/record decoder | impossible state/version | die | stop operation, no effects |

Consumer ports: `ScopeReader`→db/repos (fake map), `ContextReader`→hosts/builds
(recorded HTTP/log fake), `TaskSubmitter`→tasks with transactional RequestID
(SQLite contract), `ProposalCreator`→existing proposals (fake/live suite),
`CommentWriter/Reader`→host clients (recorded HTTP contract). Contracts cover
success, every refusal, duplicate RequestID, changed digest, stale head,
untracking between prepare and dispatch and unknown write outcomes.

## Boundary, invariants and files

Parsers: API action request→validated IDs/kind/key (reject foreign scope, URLs,
oversized prompt/unknown action); proposal comment body→bounded plain text and
head/marker (reject control data/invalid MR/head); DB action/outbox row→state
(reject invalid variant/link/version); host comment list→validated identity.

Properties: `TestPropEveryAttentionReasonHasAction`, `TestPropDiagnoseIsAsk`,
`TestPropFixRequiresExplicitCode`, `TestPropImportNeverStarts`,
`TestPropActionScopeRevalidated`, `TestPropRequestDigestImmutable`,
`TestPropNoPostBeforeConfirm`, `TestPropUnknownCommentNeverReposts`.
Chaos tests kill after intent commit/task admission/comment POST and before result
commit; the same request yields at most one task/comment. Existing invariants
1–11 remain independently covered.

Files: `internal/core/actions/*.go`, `internal/actions/*.go`, task draft/request
ports in `internal/tasks`, proposal variant/executor in `internal/proposals`,
host write adapter, migration/query/DTO foundation files; UI uses one shared action
feature across all three surfaces, specified in the UI design.

## Open questions

F0 proposes durable RequestID/outbox and general MR-level comment posting with
pinned review context; inline line-specific posting is not required by AC-02.
Unreadable-repo drafts must be labelled as awaiting a valid cwd, not presented as
an agent that can already run. No safety exception is proposed.

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
