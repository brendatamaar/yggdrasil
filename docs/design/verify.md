# Verify — Model Functional Effect design
Profile: service

Covers VF-01 (verify each changed repo when a turn ends), VF-02 (pass/fail per repo on the card and in Review), VF-03 (send the failure to the agent), WE-05 (toolchain bootstrap before the first verification), BL-08 (per-repo verify chain, full-tests opt-in) and BL-09 (builds in a worktree). Decisions 2, 5 and 6 of `docs/phases/phase-4.md` apply: automatic bootstrap in the task's own worktree, runs go through the builds queue, verification follows every turn that ended in review with a change.

Core package: `internal/core/verify` (pure, no JSON tags). Shell: `internal/verify` (service, interpreter, reconcile loop), `internal/db` (rows ↔ state), `internal/api` (DTOs, handlers). The message composer for "Send failure to agent" is pure and lives in the same core package (`ComposeFailure`), but is not part of `Step`.

Commands run through the existing builds service as `InWorktree` builds, so BL-03 (one at a time, stop at the first failure), logs, history and cancel are not re-implemented here.

## State map

One aggregate per (task, repo): `Verification{ID, Task, Repo, Worktree, BootHash, State}`. `BootHash` is the dependency hash the last successful bootstrap saw ("" = never). `State` is sealed:

```go
type State interface{ isState() }

type None struct{}                         // no turn has ended in review yet
type Skipped struct {                      // nothing to verify, by rule
    Turn   TurnID
    Reason SkipReason                      // no_changes | disabled
}
type Unverifiable struct {                 // wanted to verify, could not
    Turn   TurnID
    Reason UnverifiableReason              // no_command | not_trusted | worktree_missing | could_not_queue
}
type Bootstrapping struct {
    Turn    TurnID
    Attempt Attempt
    Build   core.BuildID                   // the bootstrap build
    Plan    Plan                           // kept so the verify build starts without a new read
    NextBuild core.BuildID                 // the verify build's ID, chosen up front
    DepsHash string                        // hash this bootstrap is for
    Since   time.Time
}
type Verifying struct {
    Turn    TurnID
    Attempt Attempt
    Build   core.BuildID                   // the verify build
    Plan    Plan
    Since   time.Time
}
type Passed struct {
    Turn    TurnID
    Attempt Attempt
    Build   core.BuildID
    At      time.Time
}
type Failed struct {
    Turn    TurnID
    Attempt Attempt
    Stage   Stage                          // bootstrap | verify
    Build   core.BuildID
    Step    int                            // index in the stage's chain
    Reason  FailReason                     // exit_code{ExitCode} | start_failed{StartFailure}
    At      time.Time
}
type Interrupted struct {                  // the process was lost (ygg stopped)
    Turn    TurnID
    Attempt Attempt
    Stage   Stage
    At      time.Time
}
type Cancelled struct {
    Turn  TurnID
    Cause CancelCause                      // user | external (cancelled in the Builds page)
    At    time.Time
}
```

`Plan` is what the shell resolved from the toolchain defaults, the repo's override and its full-tests opt-in (BL-08): `Bootstrap []StepSpec`, `Verify []StepSpec` (full tests already appended when opted in), `DepsHash string`. The core never sees toolchains or settings.

"Superseded" is not a state: a newer turn replaces the aggregate's state directly and cancels the old build (see the transition table). "Queued behind another build" is not a state either: the builds service owns it, and the DTO joins the build's status. "Retrying" is a new `Attempt`, not a state.

Task-level summary (board, task card) is derived, never stored: worst of the repo states in the order Failed > Interrupted > Unverifiable > Cancelled > Bootstrapping/Verifying > Passed > Skipped > None.

## Inputs

All carry `Now`. IDs (build IDs) arrive as input fields; the shell generates them.

| Input | From | Fields |
|---|---|---|
| `TurnEnded` | reconcile loop: task idle, outcome ReadyForReview, turn newer than the aggregate's | `Turn, Facts, Plan, BootBuild, VerifyBuild, Now` |
| `Retry` | user ("Verify again") | `Facts, Plan, BootBuild, VerifyBuild, Now` |
| `Resume` | startup reconcile, for an `Interrupted` aggregate | `Facts, Plan, BootBuild, VerifyBuild, Now` |
| `Cancel` | user | `Now` |
| `BuildFinished` | builds service subscription or reconcile poll | `Build, Result, Now` |
| `EnqueueFailed` | interpreter: `EnqueueBuild` was rejected or gave up | `Build, Reason, Now` |

`Facts` (shell-read, parsed at the boundary): `Enabled bool` (project switch, decision 2), `Trusted bool` (SF-09), `WorktreeActive bool`, `HasChanges bool` (non-empty diff against the worktree base commit, including untracked).
`BuildResult` (sealed): `BuildPassed` · `BuildFailedAt{Step, ExitCode}` · `BuildStartFailedAt{Step, Reason}` · `BuildCancelled` · `BuildInterrupted`.
`BootBuild` is used only if a bootstrap is needed; the core ignores it otherwise.

## Transition table

`MaxAutoAttempts = 2`: the first run plus one automatic resume after a restart. Every changing transition also emits `WriteAudit(verify.<action>)` and `Publish(VerificationChanged)`; only the extra commands are listed.

`Enter(t, facts, plan, ids, attempt)` is the shared entry rule used by `TurnEnded`, `Retry` and `Resume`:

| Condition (checked in order) | Result | Extra commands |
|---|---|---|
| `!facts.Enabled` | Skipped{disabled} | — |
| `!facts.WorktreeActive` | Unverifiable{worktree_missing} | — |
| `!facts.Trusted` | Unverifiable{not_trusted} | — |
| `!facts.HasChanges` | Skipped{no_changes} | — |
| `len(plan.Verify)==0` | Unverifiable{no_command} | — |
| `len(plan.Bootstrap)>0 ∧ plan.DepsHash ≠ BootHash` | Bootstrapping{Build=BootBuild} | `EnqueueBuild(BootBuild, bootstrap)` |
| otherwise | Verifying{Build=VerifyBuild} | `EnqueueBuild(VerifyBuild, verify)` |

| From | Input | Guard | To | Extra commands |
|---|---|---|---|---|
| any | TurnEnded | `Turn > current turn` (None = 0) | `Enter(attempt 1)` | `CancelBuild(old)` when the old state was Bootstrapping or Verifying |
| Skipped, Unverifiable, Passed, Failed, Interrupted, Cancelled | Retry | — | `Enter(Attempt+1)` | — |
| Bootstrapping, Verifying | Retry | — | no change (a run is live; the user cancels first) | — |
| Interrupted | Resume | `Attempt < MaxAutoAttempts` | `Enter(Attempt+1)` | — |
| Bootstrapping | BuildFinished | `Build==current ∧ BuildPassed` | Verifying{Build=plan's verify build}, BootHash=DepsHash | `EnqueueBuild(VerifyBuild, verify)` |
| Bootstrapping | BuildFinished | `Build==current ∧ BuildFailedAt{s,c}` | Failed{bootstrap, s, exit_code c}, BootHash unchanged | — |
| Bootstrapping | BuildFinished | `Build==current ∧ BuildStartFailedAt{s,r}` | Failed{bootstrap, s, start_failed r} | — |
| Verifying | BuildFinished | `Build==current ∧ BuildPassed` | Passed | — |
| Verifying | BuildFinished | `Build==current ∧ BuildFailedAt{s,c}` | Failed{verify, s, exit_code c} | — |
| Verifying | BuildFinished | `Build==current ∧ BuildStartFailedAt{s,r}` | Failed{verify, s, start_failed r} | — |
| Bootstrapping, Verifying | BuildFinished | `Build==current ∧ BuildCancelled` | Cancelled{external} | — |
| Bootstrapping, Verifying | BuildFinished | `Build==current ∧ BuildInterrupted` | Interrupted{stage} | — |
| Bootstrapping, Verifying | Cancel | — | Cancelled{user} | `CancelBuild(current)` |
| Bootstrapping, Verifying | EnqueueFailed | `Build==current` | Unverifiable{could_not_queue} | — |

The verify build ID for the Bootstrapping → Verifying hop is `Bootstrapping.NextBuild`, set by `Enter` from the input's `VerifyBuild`, so the hop needs no new ID from the shell.

**Any other (state, input) pair: no change, no commands.** This includes: `BuildFinished` for a build that is not the current one (a superseded or duplicated delivery), `TurnEnded` with a turn not newer than the current one, `Resume` on anything but a first-time `Interrupted`, `Cancel` on a state with no live build, and a second `BuildFinished` after a terminal state.

## Invariants

Checked after every step of a random input sequence (`run(None-aggregate, inputs)`, rapid generators mixing legal, stale and duplicate inputs).

| # | Invariant | Property test |
|---|---|---|
| V1 | At most one live build per aggregate: every transition into Bootstrapping or Verifying emits exactly one `EnqueueBuild` with the state's `Build`; no other transition emits one. | `TestPropOneEnqueuePerLiveEntry` |
| V2 | A `BuildFinished` whose `Build` is not the current one changes nothing. | `TestPropStaleBuildIgnored` |
| V3 | `Passed` is only reached from `Verifying`, only by `BuildPassed` of the verify build; never from `Bootstrapping`, never from `Enter`. | `TestPropPassedOnlyFromVerify` |
| V4 | The turn never decreases, and a `TurnEnded` for a turn not newer changes nothing. | `TestPropTurnMonotonic` |
| V5 | The attempt never decreases; `Resume` increments only from `Interrupted` with `Attempt < MaxAutoAttempts`; `Retry` is the only way past that. | `TestPropAttemptRules` |
| V6 | `BootHash` changes only on bootstrap success, to that bootstrap's `DepsHash`; a failed, cancelled or interrupted bootstrap leaves it as it was. | `TestPropBootHashOnlyOnSuccess` |
| V7 | A bootstrap is started only when the plan has bootstrap steps and its `DepsHash` differs from `BootHash`; otherwise the first live state is Verifying. | `TestPropBootstrapOnlyWhenHashChanged` |
| V8 | Terminal states (Passed, Failed, Skipped, Unverifiable, Interrupted, Cancelled) change only through `TurnEnded` (newer), `Retry`, or `Resume` (Interrupted). | `TestPropTerminalOnlyLeavesByRestartInputs` |
| V9 | A live state never has an empty chain: Verifying implies `len(Plan.Verify)>0`; Bootstrapping implies `len(Plan.Bootstrap)>0`. | `TestPropLiveStatesHaveChains` |
| V10 | `Skipped` and `Unverifiable` emit no build command. | `TestPropNoBuildForSkipped` |
| V11 | Every step that changes the state emits exactly one `WriteAudit` and one `Publish`; a no-change step emits no command. | `TestPropAuditPerChange`, `TestPropNoChangeNoCommands` |
| V12 | `Task`, `Repo` and `Worktree` never change. | `TestPropIdentityStable` |
| V13 | The failing step index is inside the chain of its stage (`Step < len(chain)`) or the input is ignored. | `TestPropFailedStepInRange` |

## Commands

| Command | Cardinality | Idempotent | Produces |
|---|---|---|---|
| `EnqueueBuild{Build, Stage, Spec}` | one | yes, key `Build` (the builds service returns the existing build for a known ID) | `BuildFinished` later (stream via the subscription), or `EnqueueFailed` |
| `CancelBuild{Build}` | one | yes (cancelling a finished or unknown build is a no-op) | `BuildFinished{BuildCancelled}` for the user-cancel case, ignored for supersede |
| `WriteAudit` | one | no, but it runs in the same transaction as the state write | — |
| `Publish{VerificationChanged}` | one | yes (a topic message) | — |

`Spec` is `Target: InWorktree{Worktree}`, the stage's steps with `Env` (`CI=true` for Node), actor `system` (`verify`).

## Call graph

```go
// Production — trigger
hub topics "tasks"/"board" or 30 s ticker or startup
  → verify.Service.reconcile
      → db.ListTasksInReview                       // stored state is the truth, not the event
      → per (task, repo with an active worktree): db.LoadVerification
      → if aggregate.turn < task.turn:
          → readFacts: settings(enabled), trust(repo), worktrees(active), git.DiffAgainstBase(hasChanges)
          → resolvePlan: builds toolchain defaults + repo_build_settings + deps hash (fs read of manifests)
          → ids.New x2
          → parse (boundary) → TurnEnded
          → commit: Step + audit in one Store.Commit (expected version)
          → interpret commands
              → EnqueueBuild → builds.Service.EnqueueWithID(id, spec, actor system)
      → per live aggregate: builds.Get(Build)
          → Finished: BuildFinished → commit, interpret
          → missing: re-issue EnqueueBuild (same ID: idempotent)
          → Queued/Running: wait for the subscription

// Production — build result
builds.Service state change → Subscribe(fn) → verify.Service.onBuild(id)
  → find aggregate by Build → map builds.Finished → BuildResult → BuildFinished → commit, interpret

// Production — user
POST /api/tasks/{id}/verify/retry      → Retry     (facts/plan as above)
POST /api/verifications/{id}/cancel    → Cancel
POST /api/verifications/{id}/send-failure
  → load Failed, read log tail (capped, redacted) → core.ComposeFailure (pure)
  → tasks.Service.Message(task, text, same mode)   // no Step: tasks owns the new turn and its audit

// Tests
core: run(aggregate, inputs)                       // no shell
shell: Service with fake BuildRunner, fake Store, fake Git, fixed Clock/IDs
```

Cardinality of the inputs: `BuildFinished` is a stream per live build with at-least-once delivery (subscription plus the reconcile poll, deduplicated by the `Build == current` guard and the terminal-state rule). `TurnEnded` is derived state, not an event: a missed wake is picked up by the next reconcile.

## Failures

| Node | Failure | Class | Handling |
|---|---|---|---|
| Read facts (git diff, trust, settings) | git error, db error | retry | the reconcile loop retries on its next tick; nothing is committed; after 3 consecutive failures per aggregate the failure is logged and shown as `Unverifiable{could_not_queue}`-style note only through the next successful read (no state written) |
| Plan resolution | toolchain not detected, no verify chain | domain outcome | `Plan.Verify` empty → `Unverifiable{no_command}` |
| Plan resolution | malformed override JSON in settings | domain outcome | settings parser rejects it; user sees "The verify command for this repo is not valid"; `Unverifiable{no_command}` |
| `EnqueueBuild` | builds rejects the spec (unknown worktree, bad argv) | domain outcome | `EnqueueFailed` → `Unverifiable{could_not_queue}` |
| `EnqueueBuild` | transient db/lock error | retry | idempotent by ID, up to 3 attempts with backoff, then `EnqueueFailed` |
| Build runs | step exits non-zero | domain outcome | `Failed` with step and exit code |
| Build runs | program not found | domain outcome | `Failed{start_failed}`; plain message ("`npm` was not found") |
| Build runs | `ygg` stopped | domain outcome | builds marks it Interrupted → `Interrupted`; startup `Resume` once |
| `CancelBuild` | any error | escape hatch | log; the verification state already moved on; a build left running ends by itself and its result is ignored (V2) |
| Commit | version conflict | retry | reload and re-evaluate (same pattern as tasks) |
| Commit | other db error | die | logged; the loop continues; nothing half-written (state + audit in one tx) |
| Any panic in an interpreter step | | die | recovered per aggregate, logged, aggregate left as stored |

Errors are translated at each layer: callers see `core/verify` types, `apperr` classes at the API, never builds or git error types.

## Requirements

| Capability | Live | Test | Contract |
|---|---|---|---|
| `BuildRunner` (`EnqueueWithID(id, spec, actor)`, `Cancel`, `Get`, `Subscribe`) | `builds.Service` through a small adapter in `cmd/ygg` | in-memory fake with scripted outcomes | `BuildRunnerContract`: enqueue twice with one ID gives one build; Get of an unknown ID is NotFound; cancel of a finished build is a no-op; a finished build is delivered at least once and its result maps to exactly one `BuildResult`. Runs against the fake and the real service (real runs use the fake `platform.StartFunc`). |
| `VerifyStore` (load, commit with expected version + audit, list in review) | `internal/db` | in-memory | `VerifyStoreContract`: version conflict, audit written in the same transaction, list returns only active worktrees. Fake and SQLite. |
| `DiffProbe` (`HasChanges(worktree)`) | `git.DiffAgainstBase` (GIT1) | fake | GIT1's clauses: committed only, uncommitted only, untracked only, EOL-only (counts as a change, flagged), none. |
| `SettingsReader` (enabled, override, full-tests, bootstrap) | `repo_build_settings` + project setting | map | clause per setting, parser rejects |
| `TaskMessenger` (`Message(task, text)`) | `tasks.Service` | fake | busy task → plain refusal |
| `Clock`, `IDs` | system | fixed | — |

Node fakes needed in the biggest test: five. Enough to split: reconcile loop and interpreter are separate files with their own fakes.

## Boundary

| Source | Parser | Produces | Rejects |
|---|---|---|---|
| `repo_build_settings` row / API body (override, full-tests, bootstrap on/off) | `ParseBuildSettings` (T1) | `Plan` overrides | unknown fields, empty argv, NUL, more than `MaxSteps`, `.cmd` argument rules (`platform.BatchArgsOK`), trailing data |
| Dependency manifests (package.json, lockfile, pom.xml, build.gradle) | `HashDeps(fs)` | `DepsHash` | files over the size cap (hash covers the cap marker), unreadable → hash of "unreadable", never an error |
| `builds.State` (Finished) | `ToBuildResult` | `BuildResult` | a state that is not terminal; a failed step index outside the spec |
| `verifications` row | `StateFromRow` | `Verification` | unknown state code, live state without a build ID, terminal state with a build ID missing where required, attempt 0 |
| Log tail for `ComposeFailure` | `TailLog(bytes, cap)` | text | invalid UTF-8 (replaced), over the cap (cut at a line boundary), secrets (redactor applied before) |

Each parser gets: round trip where a formatter exists, accepted ⇒ valid, one test per rejection rule, and a fuzz target.

## Files

Core (`internal/core/verify`): `types.go` (IDs, enums, `Plan`, `Facts`), `state.go`, `input.go`, `command.go`, `step.go`, `enter.go`, `summary.go` (derived task summary), `compose.go` (`ComposeFailure`, pure), `step_prop_test.go` (V1–V13), `compose_test.go`.
Shell (`internal/verify`): `service.go`, `reconcile.go`, `facts.go`, `plan.go`, `interpret.go`, `parse.go`, `store.go`, `fakes_test.go`, `*_test.go`; `internal/db/store_verify.go`, `queries/verifications.sql`; `cmd/ygg/verify.go`; API DTOs and handlers in `internal/api` (AP1).
Contract suites: `internal/verify/verifytest` (`BuildRunnerContract`, `VerifyStoreContract`).

Foundations also include, because other tasks depend on it: `builds.Service.EnqueueWithID` (idempotent by ID) and `BuildStep.Env`.

## Open questions

1. **Idempotent enqueue by ID needs a change in `internal/builds`** (`Enqueue` currently generates the ID). Proposed in foundations. Alternative: store the verify build ID only after enqueue and accept a rare duplicate build after a crash. Recommend the change.
2. **Automatic resume after a restart: once** (`MaxAutoAttempts = 2`), then the user presses Verify again. Recommend.
3. **Re-bootstrap when the dependency hash changes** (agent edited `package.json`/lockfile), not only for the first verification. Recommend yes; WE-05 says "before the first verification", and a stale `node_modules` gives misleading failures.
4. **Failed bootstrap can be sent to the agent** (the agent may have broken the lockfile). The composer words it differently. Recommend yes.
5. **Sub-task children** (AG-26) verify like any task, each on its own; the parent's integration view only reads their summaries. No special case in this design.
6. **Verification does not take the main-checkout lock**: it only reads and builds in a worktree. `mainlock` is untouched. Confirm this matches how `InWorktree` builds behave today (the builds design says worktree builds do not take the lock); the foundations agent checks and reports.
