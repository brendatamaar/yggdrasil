# Tasks — Model Functional Effect design
Profile: service

Covers:

- AG-07: Code mode, one worktree per task (and per repo in project mode), auto-generated short names. WE-04: short paths, `core.longpaths`.
- AG-08, AG-09: current-branch and whole-project tasks (rules in `docs/design/guard.md`; admission is `guard.Admit`).
- AG-10: mode per message (a task has **turns**).
- AG-11, D7: `maxAgents` (default 2), queue, one editor per main checkout, room hint.
- AG-12: board column of a task.
- AG-15: usage totals per turn and task.
- AG-22: sub-agent depth and concurrency caps carried by the turn (moved out of `internal/api`), observed and enforced as a second layer.
- Restart recovery (Phase 2 review: "Tasks run in an in-memory goroutine with no startup reconciliation").
- Shared main-checkout lock (carried from Phase 2: "Busy-snapshot race ... deferred to Phase 3").

Core: `internal/core/tasks` (no JSON tags; DTOs in `internal/api`; imports `internal/core/guard`, which is pure).
Shell: `internal/tasks` (service, interpreter), `internal/worktrees` (create, names), `internal/mainlock` (new, shared registry), `internal/api/{tasks,board,worktrees}.go`, `cmd/ygg/tasks*.go`.

Sub-tasks (AG-23 to AG-26) are Phase 7. The `ParentTaskID` column stays; nothing here creates a child.

Two pure pieces:

| Piece | What it decides |
|---|---|
| `Step(Task, Input) (Task, []Command)` | one task: turns, attempts, stop, finish, usage, caps |
| `Schedule(Board, Input) (Board, []Command)` | across tasks: who may start now (`maxAgents`, main-checkout editors, locks) |

`Board` is derived from the database on load; it is not stored.

## State map

**Task** (sealed interface `State`; `Status()` maps to the existing `core.TaskStatus` values):

| State | Data | `core.TaskStatus` |
|---|---|---|
| `Draft` | | `draft` |
| `Waiting` | `Turn`, `BlockedBy` (none / `MaxAgents` / `MainCheckout{Repo, Holder}`) | `queued` |
| `Starting` | `Turn`, `Attempt`, `Since` (worktrees and locks being prepared) | `running` |
| `Running` | `Turn`, `Attempt`, `Since`, `Subagents` (active IDs with depth), `Usage` | `running` |
| `Stopping` | `Turn`, `Attempt`, `Cause` (UserCancel / IntegrityViolated / CapExceeded / Shutdown) | `running` |
| `Idle` | `Outcome`, `At` | see below |

`Idle.Outcome` (closed set): `Answered` → `done` (an Ask turn finished), `ReadyForReview` → `review` (a writing turn finished), `NeedsYou{Why}` → `needs_you` (`IntegrityViolated`, `CapExceeded`), `Failed{Kind}` → `failed` (`PrepareFailed`, `StartFailed`, `RunFailed`, `Interrupted`), `Cancelled` → `cancelled`, `Dismissed` → `done` (the user marked it done).

An idle task accepts a new message (a new turn that resumes the session when the adapter can). `done`, `failed` and `cancelled` are therefore not "terminal" for the task; a **turn** never restarts (invariant K5). `core.TaskStatus.Terminal()` keeps meaning "no run is live".

**Turn** (stored in `task_turns`): `ID`, `Seq`, `Mode`, `Prompt` (stored, never audited or notified), `Attempt`, `StartedAt`, `FinishedAt`, `Outcome`, `Usage`.

**Board** (scheduler state): `Max`, `Tasks map[TaskID]Entry{State kind, Mode, EditsMain []RepoID, Queued uint64 (order)}`, `Locks map[RepoID]TaskID` (granted main-checkout edit locks).

**Board column** (pure `Column(state, pendingProposals int)`): `Working` for `Waiting/Starting/Running/Stopping`, `NeedsYou` for `Idle{NeedsYou}` or (`Idle` with pending proposals), `Review` for `Idle{ReadyForReview}`, `Done` for the rest of `Idle`. A running task with pending proposals stays `Working`; the card shows a badge.

## Inputs

Task inputs (`Step`):

| Input | Fields |
|---|---|
| `Message` | `Turn`, `Mode`, `Prompt`, `Now`, `Trust guard.TrustView`, `Enforcement`, `OptIn`, `Caps` (depth, concurrency from settings) |
| `Admitted` | `Turn`, `Now` (from `Schedule`; moves `Waiting` to `Starting`) |
| `Prepared` | `Turn`, `Attempt`, `Dir`, `ExtraDirs`, `Now` (worktrees exist, locks held) |
| `PrepareFailed` | `Turn`, `Attempt`, `Reason`, `Now` |
| `LocksDenied` | `Turn`, `Attempt`, `Holder` |
| `RunFinished` | `Turn`, `Attempt`, `Status` (Completed / Failed / Cancelled), `Usage`, `SessionID`, `Now` |
| `UsageReported` | `Attempt`, `Usage` (increment or cumulative) |
| `SubagentStarted` / `SubagentStopped` | `Attempt`, `AgentID`, `Depth` |
| `CancelRequested` | `Now` |
| `IntegrityViolated` | `Turn`, `Now` (from `guard.StepIntegrity`' `StopTask`) |
| `ProcessesGone` | `Turn`, `Attempt` (after `KillTask`) |
| `Dismissed` | `Now` |
| `Recovered` | `Now` (once, at startup) |

Scheduler inputs (`Schedule`): `Enqueued{Task, Mode, EditsMain, Order}`, `Removed{Task}`, `MaxChanged{Max}`, `LockGranted/LockDenied`, `Wake` (any release), plus the task state changes above as `TaskChanged{Task, Kind}`.

Clock and IDs arrive as input fields; the core reads neither.

## Transition table

### `Step`

| From | Input | Guard | To | Commands |
|---|---|---|---|---|
| `Draft`/`Idle` | `Message` | `guard.Admit` admits `Mode` | `Waiting{Turn}` | `PersistTurn`, `Audit{task.turn}`, `Publish`, `Enqueue` |
| `Draft`/`Idle` | `Message` | `Admit` refuses | unchanged | `Reject{Reason}` (the API shows the plain message) |
| `Waiting` | `Admitted` | | `Starting{Turn, 1}` | `PrepareRun{Turn, Attempt}` (create worktrees, take locks), `Audit`, `Publish` |
| `Waiting` | `CancelRequested` | | `Idle{Cancelled}` | `Dequeue`, `Audit`, `Publish` |
| `Starting{t,a}` | `Prepared{t,a}` | | `Running{t,a}` | `StartRun{Turn, Attempt, Mode, Resume, Caps, Dir, ExtraDirs}`, `Audit`, `Publish` |
| `Starting{t,a}` | `PrepareFailed{t,a}` | | `Idle{Failed(PrepareFailed)}` | `ReleaseLocks`, `Audit`, `Publish` |
| `Starting{t,a}` | `LocksDenied{t,a}` | | `Waiting{t, BlockedBy}` | `Requeue` (position kept) |
| `Starting{t,a}` | `CancelRequested` | | `Stopping{t,a,UserCancel}` | `KillRun` (also stops preparation), `Audit` |
| `Running{t,a}` | `UsageReported{a}` | | `Running` with summed usage | none |
| `Running{t,a}` | `SubagentStarted{a, id, depth}` | `depth <= Caps.Depth` and active `< Caps.Concurrent` | `Running` with `id` added | none |
| `Running{t,a}` | `SubagentStarted{a, id, depth}` | over a cap | `Stopping{t,a,CapExceeded}` | `KillRun`, `Audit{subagent.cap}` |
| `Running{t,a}` | `SubagentStopped{a, id}` | | `Running` with `id` removed | none |
| `Running{t,a}` | `RunFinished{t,a,Completed}` | | `Idle{Answered}` if the turn is read-only, else `Idle{ReadyForReview}` | `FinishTurn{usage, session}`, `ReleaseLocks`, `KillTask`, `Audit`, `Publish` |
| `Running{t,a}` | `RunFinished{t,a,Failed}` | | `Idle{Failed(RunFailed or StartFailed)}` | same |
| `Running{t,a}` | `RunFinished{t,a,Cancelled}` | | `Idle{Cancelled}` | same |
| `Running{t,a}` | `CancelRequested` | | `Stopping{t,a,UserCancel}` | `KillRun`, `Audit` |
| `Running{t,a}` | `IntegrityViolated{t}` | | `Stopping{t,a,IntegrityViolated}` | `KillRun`, `Audit` |
| `Stopping{t,a,c}` | `RunFinished{t,a,_}` | | `Idle` per cause: `Cancelled`, `NeedsYou{IntegrityViolated}`, `NeedsYou{CapExceeded}` | `FinishTurn`, `ReleaseLocks`, `KillTask`, `Audit`, `Publish` |
| `Starting`/`Running`/`Stopping` | `Recovered` | | `Idle{Failed(Interrupted)}` | `FinishTurn{usage from stored events}`, `ReleaseLocks`, `Audit` |
| `Idle{ReadyForReview/NeedsYou}` | `Dismissed` | | `Idle{Dismissed}` | `Audit`, `Publish` |
| any | input with a stale `Attempt` or `Turn` | | unchanged | none |
| any other (state, input) pair | | | unchanged | none |

A cancel that arrives in `Idle` does nothing. `Shutdown` is not a cause in Phase 3: closing the server cancels the run contexts, the runs finish as `Cancelled`, and the next start finds nothing to recover; a crash goes through `Recovered`.

### `Schedule`

`Wake` picks tasks to admit, in `Order`, while all hold:

1. `active < Max`, where `active` counts `Starting`, `Running` and `Stopping` tasks (a stopping task still has a process).
2. If the turn edits a main checkout (`current_branch`), no other active task holds a lock on that repo (`Locks`).
3. Otherwise the task stays `Waiting` and keeps its place; the scan continues with the next task (**work-conserving**: a blocked head does not hold back tasks behind it that can run).

For each admitted task it emits `Admit{Task}` and records the lock. `MaxChanged` never stops a running task; admission waits until `active < Max`. Lowering `Max` is only a limit on new starts.

Room hint (pure): `RoomHint(cpus, freeMemMB, max, active) Hint`: suggest raising `maxAgents` by one when `active == max`, `cpus >= 4*(max+1)` and `freeMemMB >= 2048*(max+1)`; never suggests more than 8. Constants in one place; the tests pin the table.

## Invariants

| # | Invariant | Property test |
|---|---|---|
| K1 | After every `Schedule` step with no `MaxChanged` lowering, `active <= Max` | `TestPropActiveNeverExceedsMax` |
| K2 | At most one active task holds an edit lock on a given main checkout; none holds a lock it was not granted | `TestPropOneEditorPerMainCheckout` |
| K3 | A task has at most one live turn; `Waiting/Starting/Running/Stopping` carry exactly one | `TestPropOneLiveTurn` |
| K4 | An input with a stale `Attempt` or `Turn` changes nothing | `TestPropStaleInputIgnored` |
| K5 | `Attempt` never decreases; a finished turn never produces `StartRun` again | `TestPropTurnNeverRestarts` |
| K6 | `StartRun` is emitted only on `Starting → Running`, once per `(Turn, Attempt)` | `TestPropStartRunOnce` |
| K7 | `Stopping` always has a `KillRun` in the step that entered it, and leaves only to `Idle` | `TestPropStoppingEndsIdle` |
| K8 | Reported usage never decreases within a task | `TestPropUsageMonotonic` |
| K9 | Observed sub-agent depth or concurrency over the cap always yields `KillRun`; under the cap never | `TestPropSubagentCapsEnforced` |
| K10 | `Recovered` leaves no task in `Starting/Running/Stopping` and emits no `StartRun`/`Prepare` | `TestPropRecoveryStartsNothing` |
| K11 | Work-conserving: after `Wake`, if a task could be admitted, one is | `TestPropWorkConserving` |
| K12 | `Admit` refusal never changes state; an admitted mode is one `guard.Admit` allows for that input | `TestPropMessageRespectsGuard` |
| K13 | Every command batch that changes the status has an `Audit` and a `Publish` for the new status | `TestPropStatusChangeAudited` |
| K14 | `Column` is a function of (state, pending count): the same input gives the same column, and `Working` iff a run could be live | `TestColumnTable`, `TestPropColumnWorkingIffLive` |
| K15 | `RoomHint` never suggests above 8 and never when `active < max` | `TestPropRoomHintBounds` |

Run via `Run(initial, inputs)` helpers with random input sequences, checking the invariants after every step (the pattern in `internal/core/builds`).

## Commands

| Command | Cardinality | Idempotent | Produces |
|---|---|---|---|
| `PersistTurn` | one | yes with key `Turn` | none (part of the commit) |
| `Enqueue` / `Requeue` / `Dequeue` | one | yes | `Wake` later |
| `PrepareRun{Turn, Attempt}` | one | yes: skips repos that already have a worktree for the task | `Prepared` / `PrepareFailed` / `LocksDenied` |
| `StartRun{Turn, Attempt, ...}` | one (the run is a stream of events) | **no**, protected by K6 and `Attempt` | `UsageReported*`, `SubagentStarted/Stopped*`, `RunFinished` |
| `KillRun{Turn, Attempt}` | one | yes (cancel the run context; `KillTask` after) | `ProcessesGone` |
| `FinishTurn` | one | yes with key `(Turn, Attempt)` | none |
| `ReleaseLocks` | one | yes | `Wake` |
| `KillTask{TaskID}` | one | yes | `ProcessesGone` |
| `Publish{status}` | one | yes (the hub assigns the sequence) | none |
| `Audit{Entry}` | one | yes with key (subject, action, at) | none; committed with the state |
| `Reject{Reason}` | one | yes | none (HTTP response only) |

## Call graph

```go
// Production: POST /api/tasks and POST /api/tasks/{id}/messages
api handler
  → parse request (boundary)                    // prompt, mode, agent, model, scope, repos
  → tasks.Service.Message(ctx, input)
      → Store.Load(task)                        // rows → State (boundary)
      → tasks.Step(task, Message)               // core (calls guard.Admit)
      → Store.Commit { SaveTask(expectedVersion), PersistTurn, AppendAudit }   // one transaction
      → interpret commands: Enqueue → Schedule(Enqueued) → Wake

// Production: the scheduler loop (one goroutine owned by the service group)
Wake (channel)
  → Schedule(board, Wake)                       // core
  → for each Admit: Step(task, Admitted) → commit → interpret PrepareRun

interpret PrepareRun
  → mainlock.TryAcquire(repos, owner=task)      // only current_branch turns
  → worktrees.Ensure(task, repos)               // internal/worktrees → internal/git.WorktreeAdd
      → names (random, shell), platform.ShortDirName, platform.LongPath
      → Store.Commit { CreateWorktree rows, AppendAudit }
  → Step(task, Prepared | PrepareFailed | LocksDenied) → commit → interpret

interpret StartRun                              // runs in a goroutine owned by the group
  → safety.Scan + guard.StepTrust (non-Ask turns), safety policy file for the hook
  → env := safety.EnvBuilder.Build
  → agents.Execute(ctx, adapter, StartOptions, sink)
        sink.Append: redact → Store.AppendTaskEvent → hub.publishSeq
                     → Step(UsageReported | SubagentStarted/Stopped) at most once per 250 ms (batched)
  → Step(RunFinished) → commit { FinishTurn, status, audit } → platform.KillTask → ReleaseLocks → Wake

// Production: cancel, dismiss, startup
POST /api/tasks/{id}/cancel → Step(CancelRequested) → commit → interpret KillRun
startup: Store.LoadActive → usage from task_events → Step(Recovered) for each → commit → Wake
```

```go
// Tests
Run(initial, inputs)                       // core, no shell
interpret PrepareRun → worktreestest.Fake + mainlocktest.Fake + dbtest store
interpret StartRun  → mock adapter (fixtures) + platformtest.Fake
```

## Failures

| Node | Failure | Class | Handling |
|---|---|---|---|
| `Store.Commit` | version conflict | retry | reload, rerun `Step`, recommit (no command ran yet; bounded 3 times, then the error is shown) |
| `Store.Commit` | other error | die | log; the user's request fails with a plain message; nothing was published |
| `worktrees.Ensure` | name already used (branch or folder) | retry | new random name, up to 5 times (nothing was created) |
| `worktrees.Ensure` | git fails (lock, bad repo, path too long, `\\wsl$`) | domain outcome | `PrepareFailed{plain reason}`; worktrees created so far stay recorded and are reused by the next attempt; nothing is deleted (invariant 1) |
| `mainlock.TryAcquire` | held by a build, a git action or another task | domain outcome | `LocksDenied{Holder}`; task stays `Waiting{MainCheckout}` and shows who holds it |
| `adapter.Check` / `Start` | CLI missing, not signed in | domain outcome | `RunFinished{Failed, StartFailed}`; the plain `UserMessage` goes to the transcript (existing `explainFailure`) |
| `agents.Execute` | sink store failure | die for the run | run cancelled, `RunFinished{Failed}` |
| agent process | crashes / non-zero exit | domain outcome | `RunFinished{Failed, RunFailed}` |
| `platform.KillTask` | error | escape hatch | log; the job object still kills the tree when `ygg` stops; the status is stored after the kill returns or fails |
| interpreter goroutine | panic | die | recovered; `RunFinished{Failed}` fed back for the current attempt; the stack is logged (the builds `safeInterpret` pattern) |
| process crash mid-run | | domain outcome | next start: `Recovered` → `Idle{Failed(Interrupted)}`; nothing is resumed or restarted |
| hub publish | slow client | escape hatch | the hub drops for that client, never blocks |

Errors are translated at each edge: `internal/git` and `platform` errors become `PrepareFailed.Reason` (plain text, no paths); the API returns `apperr` classes.

## Requirements

| Capability | Live | Test | Contract |
|---|---|---|---|
| `TaskStore` (`Load`, `LoadActive`, `Commit` with `SaveTask`, `SaveTurn`, `CreateWorktree`) | `internal/db` | `dbtest` | existing store contract plus: row ⇄ `State` round trip for every state; inconsistent rows rejected (Running without `started_at`); version conflict returns `ErrConflict`; `LoadActive` returns exactly the live and waiting tasks |
| `Worktrees` (`Ensure(task, repos) ([]Worktree, error)`) | `internal/worktrees` over `internal/git` | `worktreestest.Fake` | `WorktreesContract`: creates one per repo under the data dir; second call reuses; collision retries; partial failure keeps the earlier ones; path is short and long-form; branch is new. Real git in `t.TempDir()` |
| `MainLock` (`TryAcquire(repos, owner) (Release, Holder, bool)`) | `internal/mainlock` | `mainlocktest.Fake` | `MainLockContract`: exclusive per repo; same owner re-acquires; release frees; concurrent acquirers get one winner; a build owner and a task owner exclude each other |
| `Runner` (`agents.Adapter` + `agents.Execute`) | claudecode / opencode | `mock` | `agentstest` (A1) |
| `Killer` (`platform.KillTask`) | `platform` | `platformtest.Fake` | existing `ProcessContract` |
| `Machine` (`CPUs()`, `FreeMemMB()`) | `platform` | fixed values | two clauses: positive values, no error on Windows/Linux |
| `Clock`, `IDs` | real | fixed / counter | none |

If `Step` needs more than these to test, split it.

## Boundary

| Source | Parser | Produces | Rejects |
|---|---|---|---|
| `POST /api/tasks`, `/messages` bodies | `api` → `tasks.ParseMessage` | `Message` | empty or over-long prompt (100 KiB); unknown mode or scope; `ask`/`code` with ≠ 1 repo; `project` scope with repos listed; unknown or unavailable agent; model not matching the model-name rule; repo not tracked or not in the project; title with control characters (cleaned, not rejected) |
| `settings.maxAgents` | `tasks.ParseMax` | `int` 1..16 | not a number, out of range |
| `tasks` + `task_turns` rows | `tasks.StateFromRows` | `State` | unknown status; a state whose required fields are missing or extra (`Running` without a turn or start time, `Idle` with a turn in flight); `Attempt < 1` in a live state |
| `worktrees` names | `worktrees.ParseName` | `Name` | not `word-word` with an optional digit; longer than 16 |
| `task_events` (recovery usage) | `tasks.SumUsage` | `Usage` | undecodable event (skipped and counted) |

Boundary properties for each: round trip (`StateFromRows(RowsOf(s)) == s`, `ParseMax(Format(n)) == n`), accepted ⇒ valid, one test per rejection rule, and fuzz targets for `ParseMessage` and `StateFromRows`.

## Persistence

- Migration `00004` (see the foundations list): `task_turns(id, task_id, seq, mode, prompt, attempt, state, started_at, finished_at, outcome, usage columns)`; `tasks.current_turn`, `tasks.idle_outcome`; `repos.trust_hash`; `worktrees.base_commit`.
- The `Board` is rebuilt from `LoadActive` at startup and kept in memory after; the database is the source of truth, and each `Step` loads its task before it runs.
- Usage during a run lives in memory and in `task_events`; the turn row is written at `FinishTurn`. After a crash, `Recovered` recomputes the turn's usage from stored events.
- Every status change commits the task row (expected version), the turn row and its audit entry in one transaction; the hub publishes after the commit.

## Files

Core (`internal/core/tasks`): `doc.go`, `types.go` (IDs, `Turn`, `Mode` use, `Usage`, `Caps`), `state.go` (sealed `State`, `Status()`), `input.go`, `command.go`, `step.go`, `schedule.go`, `column.go`, `hint.go`, `parse.go` (`ParseMessage`, `ParseMax`, `StateFromRows`, `RowsOf`), `prop_helpers_test.go`, `step_prop_test.go` (K3–K10, K12, K13), `schedule_prop_test.go` (K1, K2, K11), `column_test.go`, `hint_test.go`, `parse_test.go`, `fuzz_test.go`.

Shell:
- `internal/tasks`: `service.go`, `interpret.go`, `loop.go` (scheduler goroutine), `recover.go`, `usage.go`, `tasksfake_test.go`, shell tests (crash points, duplicate delivery, cancel races, chaos over Message/Cancel/Finish).
- `internal/worktrees`: `ensure.go`, `names.go`, `worktreestest/`.
- `internal/mainlock`: `lock.go`, `mainlocktest/`; **builds and repo git actions adopt it** (one-line acquire around main-checkout builds and pull/checkout/switch). That touches `internal/builds` and `internal/repos` services, so it is its own small task after the tasks service, with the Busy-snapshot race test as its definition of done.
- `internal/api`: thin `tasks.go`, `board.go`, `worktrees.go`; the sub-agent cap constants and `modelName` leave `api`.
- `cmd/ygg`: wiring, startup `Recovered`.

## Open questions

1. **Whole-project mode uses one worktree per repo** (not the main checkouts), with the first worktree as the agent's working directory and the others as extra directories. Only `current_branch` edits a main checkout. Recommendation: yes; it keeps invariant 5 uniform and gives "one commit per repo" for free. Alternative: main checkouts for project mode (needs locks on every repo).
2. **Idle tasks can always take a new message,** including after `failed` or `cancelled` (resume when the adapter supports it, otherwise a fresh session with the earlier prompt in the transcript only). Recommendation: yes.
3. **Who marks Done:** an Ask turn that finishes is `done` by itself; a writing turn ends in `review` until the user dismisses it or ships its proposals (Phase 4 closes it). Recommendation: as written.
4. **`maxAgents` range 1..16, and lowering it never stops a running task.** Recommendation: as written.
5. **The main-checkout lock** touches `internal/builds` and `internal/repos` (small, after the tasks service). Recommendation: yes, to close the Phase 2 race.

## Decisions (approved 2026-10-25)

All five recommendations accepted: (1) project mode uses one worktree per repo; (2) idle tasks always accept a new message; (3) Ask turn → `done`, writing turn → `review` until dismissed or shipped; (4) `maxAgents` 1..16, lowering never stops a running task; (5) `internal/mainlock` adopted by builds and repo actions in a small follow-up task.

## Deviation (foundations, 2026-10-25)

archtest `coreSubpackages` forbids core domains importing each other, so `core/tasks` does not call `guard.Admit`. `tasks.Message` carries `Admission{Admitted, Warning, Reason, Message}`, filled by the shell from `guard.Admit`; `Step` refuses (`Reject`) when `!Admitted` and never starts a turn the admission did not allow (K12 is tested on that). `guard.TurnID` and `tasks.TurnID` are separate types converted in the shell.
