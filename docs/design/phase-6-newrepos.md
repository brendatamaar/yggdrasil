# Phase 6 new repo and publish — Model Functional Effect design
Profile: distributed

NR-01–04; AG-08, SF-02/04/05/06/09. Core: `internal/core/newrepos`.
Shell: `internal/newrepos`; confirmed effects reuse `internal/proposals` workers.
This feature explicitly raises the default service profile for these durable,
non-idempotent effects; it does not change the global profile for read-only flows.

## Shapes and state map

Repo initialization uses an ordinary reserved `repos` row linked to its tracked
project, `readiness=reserved|ready`. The user explicitly opts into tracking this
future repo in the new-repo dialog. A reserved repo is excluded from **all** agent,
MCP, build, terminal, dev-server and ordinary status/tracking listings and direct
lookup paths; only the initialization resolver may inspect it. `GetRepo` and
`AddTaskRepo` must enforce readiness in SQL, not just UI filtering. Rescan cannot
promote it before initialization succeeds. Existing repos migrate to ready.

An ordinary draft setup task and its task-repo relation own the initialization
proposal, preserving Project→Repo→Task→Change set. The setup draft is internal to
the initialization flow until ready and cannot start an agent. The initialization
resolver is narrowly scoped by proposal/task/reservation ID; ordinary proposals
and task submission still require ready repos. Reserving names/rows is audited but
does not create a disk directory or automatically trust a repo.

`InitSpec{Project,Repo,Leaf,Branch,README,Ignore,CommitMessage}` is immutable,
hash-bound and previewed. Leaf is exactly one child name, never a browser-supplied
absolute path. Initial branch `main`, README/ignore at most 64 KiB each. Missing
user identity refuses before disk effects. No automatic model call for first prompt.

Eight states: `Reserved{Spec,Hash}`, `AwaitingConfirm{Proposal}`,
`Initializing{Attempt,Stage}`, `Ready{Repo,InitialHead}`,
`Publishing{Attempt,Stage,Spec}`, `Published{RemoteID,URL,Head}`,
`Uncertain{Attempt,Stage,Reason}`, `Stopped{Stage,Reason}`.
InitStage is CreateDirectory, InitGit, WriteREADME, WriteIgnore, FirstCommit,
FinalizeTracking. PublishStage is CreateRemote, AddOrigin, PushHead.
A stage enum is not a bundle of completion booleans; stage receipt data is a
sealed variant holding exactly the identity needed by the next stage.

`PublishSpec{Repo,HostInstance,Owner,Name,Visibility,Branch,ExpectedHead,Marker}`
requires absent origin and a ready tracked repo. Private is the explicit default;
public selection appears in confirmation. Owner/account type is server-validated.

## Inputs and transition table

Inputs: `Reserve{Spec,IDs,Now}`, `ProposalCreated{ID,Hash}`, `Confirmed{Hash,Attempt}`,
`StageSucceeded{Attempt,Stage,Receipt}`, `StageFailed{Attempt,Stage,Outcome}`,
`PublishConfirmed{Spec,Hash,Attempt}`, `Recovered`,
`Reconciled{Attempt,Stage,Outcome}`, `RetryConfirmed`, `Reject`, `Untracked`.

| From | Input / guard | To | Commands |
|---|---|---|---|
| none | Reserve, project tracked/leaf available | Reserved | PersistReservationAndSetupDraft, Audit |
| Reserved | ProposalCreated | AwaitingConfirm | PublishPreview |
| AwaitingConfirm | Confirmed matching immutable hash | Initializing(CreateDirectory) | PersistIntent, RunInitStage |
| Initializing(a,s) | StageSucceeded(a,s) | next stage or Ready | PersistReceipt, Audit, next RunInitStage |
| Ready | PublishConfirmed, matching HEAD/origin absent | Publishing(CreateRemote) | PersistIntent, RunPublishStage |
| Publishing(a,s) | StageSucceeded(a,s) | next stage or Published | PersistReceipt, Audit, next RunPublishStage |
| Initializing/Publishing | definite failure | Stopped | PersistFailure, Explain |
| Initializing/Publishing | unknown result / Recovered | Uncertain | ReconcileStage (read-only) |
| Uncertain | Reconciled exact success | next stage or ready/published | PersistReceipt; next stage only while confirmation remains valid |
| Uncertain | Reconciled absent/conflicting/unknown | Stopped or Uncertain | Explain; no write |
| Stopped | RetryConfirmed, original immutable preconditions valid | pending stage | PersistIntent, RunStage |
| Reserved/AwaitingConfirm | Reject | Stopped | Audit, hide pending proposal; no disk deletion |
| any nonterminal | Untracked | Stopped | CancelWorker, Audit |

Any other pair: unchanged/no commands. Cancellation during an effect becomes
Uncertain, not a claim that rollback occurred. A stage that may have partly run
is never automatically repeated. Untracking never removes reserved folders/files.

## Commands and call graph

| Command | Cardinality | Idempotent | Produces |
|---|---|---|---|
| PersistReservationAndSetupDraft / PersistIntent / Receipt / Audit | one transaction | yes, operation+stage+version | state |
| RunInitStage | one local effect | not generally; exclusive create/content/HEAD fences | StageSucceeded/Failed |
| RunPublishStage CreateRemote | one HTTP POST | no | remote ID or unknown |
| RunPublishStage AddOrigin | one local config write | only if absent/identical owned origin | receipt/failure |
| RunPublishStage PushHead | one in-process smart HTTP write | expected absent/same remote ref, no force | remote head/unknown |
| ReconcileStage | one bounded read | yes | Reconciled |
| CancelWorker / PublishPreview / Explain | one | yes | result/event |

```go
// Production
new-repo preview/reserve API → parse leaf/project (boundary)
  → NewRepoStep (core) → Store.Commit(reservation + setup draft + proposal + audit)
UI confirm → existing proposals.Step/hash → durable worker
  → NewRepoStep → platform safe child creation / git init / exact file creation / first commit
  → Store.Commit(ready + receipts + audit)
UI publish confirm → worker → host HTTP create → safe origin write
  → internal/git in-process PublishTransport → remote branch
// Tests
NewRepoStep sequences → properties
worker → SQLite + temporary repos + fake path/host/transport capabilities
fault injector → restart at each stage boundary and lost-response point
```

Filesystem capability uses handle-relative/exclusive operations and rejects
junction/reparse ancestors or identity swaps, including the project root changing
between preview and execution. Long-name canonicalization and no-follow semantics
are platform-owned. Existing folders are refused, even when empty. Resume requires
owned directory identity and exact receipts; files supplied by another process
are never overwritten. File creates do not run repo hooks; first commit is through
safe existing git wrapper with clean env and hooks disabled. No recursive cleanup.

## Publish transport decision

Use an in-process HTTP Git transport inside `internal/git`, with explicit immutable
refspec `refs/heads/<branch>:refs/heads/<branch>`, no force/ref deletion, remote URL
constructed from configured host+validated owner/name, no URL credentials. Proposed
library: go-git, whose project documents pure-Go implementation and Apache-2.0
license ([repository](https://github.com/go-git/go-git),
[license](https://github.com/go-git/go-git/blob/main/LICENSE)). Pin a stable supported
major/version during F1 and run dependency/license/cgo checks; do not infer compatibility
from the license alone. The transport is injected at the consuming boundary.

Only the host service/transport interpreter sees the token in process memory;
HTTP authorization is an internal header. No askpass, token-bearing Git subprocess,
credential helper, env token, token URL, persistent credential config or signed URL
in logs. Disable cross-origin authenticated redirects. Do not invoke external
hooks, subprocess filters, submodule recursion or global credential helpers.
An unsupported Git object/storage format is a clear refusal, not a fallback to
an unsafe credential path. Existing non-publish git CLI operations still use
`platform.Start` with clean env and job objects.

## Persistence and delivery

Migration 00007 adds repo readiness, unique per-project reservation leaf, setup
request links and operation/stage receipts with expected-version updates.
Reservation/initial proposal/audit commit atomically before filesystem effects.
Outbox rows are scoped to proposal+stage+attempt and claimed by one worker; stale
claims recover through reconciliation, never unconditional resend. Store producer
request IDs/digests for the lifetime of the setup/publish history. There is no
automatic destructive expiry or timeout cleanup.

CreateRemote includes a non-secret, random operation marker in the previewed repo
description. A reply gives immutable remote repository ID, recorded before origin
write/push. Lost create reply: lookup owner/name, require exact operation marker,
expected visibility/owner and empty ref state; missing/mismatched/inaccessible data
remains unknown/conflict. Never adopt an unrelated existing repo. Lost push reply:
read remote branch; exact ExpectedHead means done, absence means explicit retry
may be offered, any different value means conflict. Force is unrepresentable.
Unknown first commit: verify exact tree/message/author/parent receipts; merely
finding any HEAD is not proof. Unknown directory creation without a durable
identity receipt may require user inspection rather than unsafe automatic adoption.

## Failures, requirements and boundary

| Node | Failure | Class | Handling |
|---|---|---|---|
| Path/reservation | invalid leaf, existing dir, project untracked | domain outcome | refuse before write |
| Git/init | missing author, locked file, stale/unowned receipt | domain outcome | explain stage; no rollback deletion |
| Host create/push | auth/org permission/name collision | domain outcome | stop, no cleanup or credential fallback |
| Mutating stage | cancelled/timeout/lost response | domain outcome | Uncertain, read-only reconciliation |
| Read-only reconciliation | transient connection | retry | bounded 2 retries; then remain Uncertain |
| Publish capability | unsupported repo format/transport | escape hatch | keep local ready repo, explain manual publishing option |
| Core/row decode | impossible stage/receipt/version | die | stop worker; no new effects |

Capabilities/contracts: `ReservationStore` (SQLite/fake: uniqueness, rollback,
readiness invisibility, version conflict); `SafeInitFS` (real platform/temp/fake:
exclusive creates, identity swaps, reparse, CRLF, long path, locked files);
`InitGit` (existing wrapper/temp repos/fake: exact first commit, hooks disabled,
clean env); `RemoteCreator` (GitHub recorded HTTP/fake: private/user/org,
collision, lost reply, marker lookup); `PublishTransport` (pure-Go transport/local
HTTP git simulator/fake: exact single branch, cancellation, no auth redirect,
missing/different/exact head, no subprocess). Shared MainLock covers each local
mutation stage and is never held while waiting for another lock-taking service.

Parsers: InitSpec and PublishSpec reject path traversal/ADS/device/WSL/8.3 and
unsupported branches/owner/scheme/content size; DB receipt parser rejects impossible
readiness/stage/result combinations; host create/lookup parser validates immutable
IDs, owner, marker, visibility and origin; transport response parser validates refs.

## Invariants and files

`TestPropInitOnlyAfterConfirm`, `TestPropReservedRepoInvisible`,
`TestPropInitNeverOverwrites`, `TestPropStageAttemptFenced`,
`TestPropPublishPrivateDefault`, `TestPropNoUnrelatedRemoteAdoption`,
`TestPropUnknownNeverBlindRetry`, `TestInvariant_3_PublishNoTokenChild`,
`TestInvariant_4_PublishCredentialFree`, `TestNewRepoCrashAtEveryStage`.

Files: `core/newrepos/*.go`, `newrepos/*.go`, safe path capability in platform,
git init/origin/publish helpers; proposal variants/executors; F2 queries/readiness
filters across every consumer path; DTOs/generation in foundations.

## Direct editing and open questions

After Ready, first prompt is explicit submission using `current_branch` with
opt-in warning, write modes enabled, shared mainlock, fresh trust/admission scan
and hook-enforcing adapter. Limited enforcement refuses direct editing. Publishing
does not silently change a running task's scope/mode. Ordinary Code stays worktree
mode. This uses existing AG-08 policy; no new exception to safety is proposed.
Approval covers reservation visibility and in-process transport choice; if safe
handle-relative creation is unavailable, implementation must report that blocker,
not approximate the guarantee with only a preview-time path check.

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
