# Landing — Model Functional Effect design
Profile: service

Covers RV-05 (local `--no-ff` merge of the agent's branch into the main checkout, aborted on conflict, never pushed) and RV-06 (remove a worktree after confirmation, with Windows file-lock handling), plus the lock part of PL-03. Line endings (PL-03) need no design: they are a pure parser in `internal/git` (see `changesets.md`/GIT1) with boundary tests.

Both are **proposals** (`merge_local`, `remove_worktree`), so confirm/reject/hash/audit/crash-reconcile are the existing proposals lifecycle (`core/proposals`: Pending → Confirmed → Executed/Failed). This design covers only what an executor decides while it runs. Each run is a small pure state machine, so every git outcome has a tested decision and the executor cannot take a step the machine did not emit.

Core package: `internal/core/landing` (pure). Shell: the executors in `internal/proposals` (PX1) interpret its commands through `internal/git`, `internal/platform` and `internal/mainlock`.

## Change to phase-4.md decision 1 (needs your approval)

Decision 1 said: remove with `git worktree remove` without `--force`. Reading how Windows fails changes the mechanics, not the scope:

- If a file is locked, `git worktree remove` can delete part of the tree and then fail. The tree then shows deleted tracked files, so a **retry without `--force` is refused as "dirty"** and the user is stuck (I believe this is what git 2.44 does; GIT1 verifies it with a held-handle test on Windows CI before the design is relied on).
- Fix: **rename first, delete second.** Renaming the worktree folder into a staging folder (`<worktree root>/.removing/<name>-<id>`) is atomic and fails at once, with nothing changed, if any process holds a handle inside (cwd of a terminal or dev server, an open file). That is a cheap lock probe, and it works for worktrees with submodules (which `git worktree move` refuses).
- Only after the rename succeeded do we delete, and only the staged folder, with a new `platform.RemoveStaged` helper: it accepts only a path under `<worktree root>/.removing/`, never follows symlinks or junctions (it removes the link itself), and refuses anything else. A partial failure leaves a staged folder and the row `removing`; Retry resumes the delete.
- Then `git worktree prune`, but only when our entry is the only prunable one for that repo (prune would also drop the metadata of the user's own worktrees on a missing drive).

Unchanged: only Yggdrasil-created worktrees under the root, only through a confirmed proposal, only when clean (including untracked files), never a main checkout, never the branch (branch deletion stays banned), no `--force` anywhere. `RemoveStaged` is the one function the archtest "never delete" rule allows, in one named file, with a test that fails if it is ever given a path outside the staging folder or follows a junction. AGENTS.md 6.1 gets one sentence for it. If you prefer to keep "git worktree remove, no force" and accept the dead end, say so and the removal machine loses the Moving/Staged states.

## State map

### Merge (`MergeState`, one per execution)

```go
type MergeState interface{ isMergeState() }

type MergeIdle struct{}
type MergeChecking struct{ Req MergeReq; Mode Mode }       // Mode: Fresh | Recovering; waiting for Observed
type MergePredicting struct{ Req MergeReq }                 // waiting for the conflict prediction
type MergeMerging struct{ Req MergeReq }                    // RunMerge emitted; the main checkout may be changing
type MergeAborting struct{ Req MergeReq; Files []RelPath } // Abort emitted after a conflict
type MergeSettling struct{ Req MergeReq; Cause Settle }     // looking at the tree once more before deciding
// terminal
type Merged struct{ Commit string }                         // new merge commit
type AlreadyMerged struct{ Head string }                    // the branch was already in the target; nothing done
type MergeRefused struct{ Reason RefuseReason }             // nothing was touched
type Conflicted struct{ Files []RelPath }                   // tree untouched or restored; nothing was merged
type MergeFailed struct{ Reason FailReason; Retryable bool }
type NeedsHuman struct{ Why string }                        // the tree is not clean after a failed merge/abort
```

`MergeReq` (from the confirmed action, validated at the boundary): `Repo, Worktree, Branch, ExpectBranchHead, TargetBranch, Title (for the commit message), Path (main checkout)`.
`RefuseReason`: `untracked_repo` · `merge_in_progress` · `wrong_branch` · `main_dirty` · `stale` · `branch_missing` · `worktree_dirty`.
`Settle` (sealed): `AfterConflict{Files}` · `AfterFailure{FailReason}`.

### Removal (`RemoveState`)

```go
type RemoveIdle struct{}
type RemoveChecking struct{ Req RemoveReq }
type Moving struct{ Req RemoveReq; Staged StagePath }       // MoveToStaging emitted
type Deleting struct{ Req RemoveReq; Staged StagePath }     // DeleteStaged emitted
type Pruning struct{ Req RemoveReq }
// terminal
type Removed struct{ Note string }                          // folder gone; Note says if prune was skipped
type RemoveRefused struct{ Reason RemoveRefuse }            // nothing was touched
type Locked struct{ Class LockClass; Holders []string }      // rename failed; nothing changed; retryable
type Stuck struct{ Staged StagePath; Class LockClass; Holders []string } // rename ok, delete incomplete; retryable
type RemoveFailed struct{ Reason string; Retryable bool }
```

`RemoveReq`: `Worktree, Repo, Path, Branch, Root (worktree root), Name`. `RemoveRefuse`: `outside_root` · `main_checkout` · `task_running` · `in_use` (a build or verification is running there) · `dirty` · `unreadable`. `LockClass` (string enum): `sharing` · `access_denied` · `path_too_long` · `other`.

Worktree row status gains `removing` (migration 00005): `active → merged → removing → removed`, monotone.

"Retrying" is a new run from `Idle`; the observation decides where it resumes. There is no "waiting for the lock": the user closes the program and presses Retry.

## Inputs

All carry `Now`.

**Merge**

| Input | From | Fields |
|---|---|---|
| `MergeStart` | executor | `Req, Mode, Now` (Mode `Recovering` when the proposal was found `Confirmed`/interrupted at startup) |
| `Observed` | `Observe` | `Tracked, Clean, DetachedHead, CurrentBranch, Head, BranchHead, MergeInProgress, MergeHead, AlreadyMerged (branch head is an ancestor of Head), WorktreeClean, Now` |
| `Predicted` | `Predict` | `Result`: `PredictClean` · `PredictConflicts{Files}` · `PredictUnsupported` (git < 2.38) · `PredictError{Msg}` |
| `MergeDone` | `RunMerge` | `Commit, Now` |
| `MergeConflicted` | `RunMerge` | `Files, Now` |
| `MergeErrored` | `RunMerge` | `Reason (identity_missing | exec), Msg, Now` |
| `AbortDone` / `AbortErrored` | `Abort` | `Now` / `Msg, Now` |

**Removal**

| Input | From | Fields |
|---|---|---|
| `RemoveStart` | executor | `Req, Now` |
| `RemoveObserved` | `ObserveWorktree` | `UnderRoot, IsMain, RowStatus, PathExists, StagedExists (and its path), Clean, StatusReadable, TaskLive, BuildLive, OtherPrunable, Now` |
| `Moved` / `MoveFailed` | `MoveToStaging` | `Now` / `Class, Holders, Now` |
| `Deleted` / `DeleteFailed` | `DeleteStaged` | `Now` / `Class, Holders, Remaining int, Now` |
| `Pruned` / `PruneFailed` | `PruneWorktrees` | `Now` / `Msg, Now` |

## Transition table

Every terminal transition emits exactly one `WriteAudit(landing.<action>)`; other changes emit none (the proposals lifecycle already audits confirm/execute). `Publish` is the executor's, via the proposals topic.

### Merge

| From | Input | Guard (checked in this order) | To | Commands |
|---|---|---|---|---|
| MergeIdle | MergeStart | — | MergeChecking{Mode} | `Observe` |
| MergeChecking | Observed | `!Tracked` | MergeRefused{untracked_repo} | audit |
| MergeChecking | Observed | `AlreadyMerged` | AlreadyMerged{Head} | audit |
| MergeChecking | Observed | `Mode==Recovering ∧ MergeInProgress ∧ MergeHead==BranchHead` (our own interrupted merge) | MergeSettling{AfterFailure{interrupted}} after `Abort` → see Aborting row | `Abort` |
| MergeChecking | Observed | `MergeInProgress` | MergeRefused{merge_in_progress} | audit |
| MergeChecking | Observed | `DetachedHead ∨ CurrentBranch≠TargetBranch` | MergeRefused{wrong_branch} | audit |
| MergeChecking | Observed | `!Clean` | MergeRefused{main_dirty} | audit |
| MergeChecking | Observed | `BranchHead==""` | MergeRefused{branch_missing} | audit |
| MergeChecking | Observed | `BranchHead≠ExpectBranchHead` | MergeRefused{stale} | audit |
| MergeChecking | Observed | `!WorktreeClean` | MergeRefused{worktree_dirty} | audit |
| MergeChecking | Observed | all above pass | MergePredicting | `Predict` |
| MergePredicting | Predicted | `PredictClean` or `PredictUnsupported` | MergeMerging | `RunMerge{--no-ff, message}` |
| MergePredicting | Predicted | `PredictConflicts{f}` | Conflicted{f} (nothing was touched) | audit |
| MergePredicting | Predicted | `PredictError` | MergeFailed{exec, retryable} | audit |
| MergeMerging | MergeDone | — | Merged{commit} | `MarkWorktreeMerged`, audit |
| MergeMerging | MergeConflicted | — | MergeAborting{f} | `Abort` |
| MergeMerging | MergeErrored | — | MergeSettling{AfterFailure} | `Observe` |
| MergeAborting | AbortDone | — | MergeSettling{AfterConflict{f}} | `Observe` |
| MergeAborting | AbortErrored | — | NeedsHuman{"…run `git merge --abort` in <path>"} | audit |
| MergeSettling | Observed | `Clean ∧ !MergeInProgress` | Conflicted{f} (AfterConflict) or MergeFailed{reason, retryable} (AfterFailure) | audit |
| MergeSettling | Observed | otherwise | NeedsHuman | audit |

Recovering mode: when `Abort` completes for our interrupted merge, the run ends `MergeFailed{interrupted, retryable}` through Settling; the user retries (a fresh run).

**Any other (state, input) pair: no change, no commands.** Terminal states never change. The merge commit message is built by the core (`Merge branch '<branch>' (<title>)`, capped); nothing is pushed by any transition.

### Removal

| From | Input | Guard (in order) | To | Commands |
|---|---|---|---|---|
| RemoveIdle | RemoveStart | — | RemoveChecking | `ObserveWorktree` |
| RemoveChecking | RemoveObserved | `!UnderRoot` | RemoveRefused{outside_root} | audit |
| RemoveChecking | RemoveObserved | `IsMain` | RemoveRefused{main_checkout} | audit |
| RemoveChecking | RemoveObserved | `RowStatus==removed ∧ !PathExists ∧ !StagedExists` | Removed{"already removed"} | audit |
| RemoveChecking | RemoveObserved | `StagedExists` (earlier run moved it; resume) | Deleting{Staged} | `DeleteStaged` |
| RemoveChecking | RemoveObserved | `!StatusReadable` | RemoveRefused{unreadable} | audit |
| RemoveChecking | RemoveObserved | `TaskLive` | RemoveRefused{task_running} | audit |
| RemoveChecking | RemoveObserved | `BuildLive` | RemoveRefused{in_use} | audit |
| RemoveChecking | RemoveObserved | `!Clean` | RemoveRefused{dirty} | audit |
| RemoveChecking | RemoveObserved | all pass | Moving{Staged = StagingPath(Root, Name, id)} | `MoveToStaging` |
| Moving | Moved | — | Deleting | `MarkRemoving`, `DeleteStaged` |
| Moving | MoveFailed | — | Locked{Class, Holders} | audit |
| Deleting | Deleted | `OtherPrunable` was false at observation | Pruning | `PruneWorktrees` |
| Deleting | Deleted | `OtherPrunable` | Removed{"git will clean up its record later"} | `MarkRemoved`, audit |
| Deleting | DeleteFailed | — | Stuck{Staged, Class, Holders} | audit |
| Pruning | Pruned | — | Removed | `MarkRemoved`, audit |
| Pruning | PruneFailed | — | Removed{"git will clean up its record later"} | `MarkRemoved`, audit |

**Any other pair: no change.**

## Invariants

| # | Invariant | Property test |
|---|---|---|
| L1 | `RunMerge` is emitted only after an `Observed` with `Tracked ∧ Clean ∧ !MergeInProgress ∧ !Detached ∧ CurrentBranch==TargetBranch ∧ BranchHead==ExpectBranchHead ∧ WorktreeClean ∧ !AlreadyMerged`, and a `Predicted` of Clean or Unsupported. | `TestPropMergeOnlyAfterAllChecks` |
| L2 | At most one `RunMerge` per run. | `TestPropOneMergeCommand` |
| L3 | The command set is closed: merge commands are only `Observe, Predict, RunMerge, Abort, MarkWorktreeMerged, WriteAudit`; there is no push, reset, branch or delete command. | `TestPropMergeCommandsClosed` |
| L4 | `Abort` is emitted only after `RunMerge`, or in Recovering mode when `MergeHead==BranchHead` (never another merge of the user's). | `TestPropAbortOnlyOurs` |
| L5 | Terminal states never change and each emits exactly one audit. | `TestPropMergeTerminalFinal`, `TestPropOneAuditPerTerminal` |
| L6 | `Refused`, `Conflicted` from prediction, and `AlreadyMerged` happen with no `RunMerge` emitted in that run. | `TestPropNoMergeNoChange` |
| L7 | `Merged` is reached only from `MergeMerging` by `MergeDone`, with exactly one `MarkWorktreeMerged`. | `TestPropMergedOnlyFromMerging` |
| L8 | `Conflicted`/`MergeFailed` after a `RunMerge` need a clean observation; otherwise `NeedsHuman`. | `TestPropSettleNeedsCleanTree` |
| L9 | A stale (`BranchHead≠Expect`) branch is never merged, whatever else holds. | `TestPropStaleNeverMerged` |
| R1 | `MoveToStaging` is emitted only after an observation with `UnderRoot ∧ !IsMain ∧ Clean ∧ StatusReadable ∧ !TaskLive ∧ !BuildLive`. | `TestPropMoveOnlyAfterChecks` |
| R2 | `DeleteStaged` is emitted only after `Moved`, or when the observation found a staged folder from an earlier run. | `TestPropDeleteOnlyAfterMove` |
| R3 | The path of `DeleteStaged` is always `StagingPath(Root, Name, id)` or the observed staged path under the staging folder; never `Req.Path`. | `TestPropDeletePathIsStaged` |
| R4 | A `Refused` or `Locked` run emits no `DeleteStaged` and no `MarkRemoving`. | `TestPropRefusedDeletesNothing` |
| R5 | `PruneWorktrees` is never emitted when `OtherPrunable` was true. | `TestPropNoPruneWithOthers` |
| R6 | The remove command set is closed: `ObserveWorktree, MoveToStaging, MarkRemoving, DeleteStaged, PruneWorktrees, MarkRemoved, WriteAudit`; no branch or force command exists. | `TestPropRemoveCommandsClosed` |
| R7 | `Locked`/`Stuck` always carry a class and a non-empty user message; holders are de-duplicated and capped. | `TestPropLockMessage` |
| R8 | Row status is monotone `active → merged → removing → removed`: `MarkRemoving` only before `MarkRemoved`, once each. | `TestPropRowStatusMonotone` |
| R9 | `StagingPath` never contains a separator from `Name`, is under `Root/.removing`, and is unique per id. | `TestPropStagingPathInside`, `FuzzStagingPath` |

## Commands

| Command | Cardinality | Idempotent | Produces |
|---|---|---|---|
| `Observe` | one | yes (read only) | `Observed` |
| `Predict` | one | yes (`merge-tree --write-tree` only writes objects) | `Predicted` |
| `RunMerge` | one | no; protected by the L-invariants and, after a crash, by `Recovering` + `AlreadyMerged` | `MergeDone` / `MergeConflicted` / `MergeErrored` |
| `Abort` | one | yes (no merge in progress is a no-op) | `AbortDone` / `AbortErrored` |
| `MarkWorktreeMerged`, `MarkRemoving`, `MarkRemoved` | one | yes (monotone, expected-status update) | — |
| `ObserveWorktree` | one | yes | `RemoveObserved` |
| `MoveToStaging` | one | yes: moving an already moved folder is `StagedExists` at the next observation | `Moved` / `MoveFailed` |
| `DeleteStaged` | one | yes with key the staged path (a missing folder is success) | `Deleted` / `DeleteFailed` |
| `PruneWorktrees` | one | yes | `Pruned` / `PruneFailed` |
| `WriteAudit` | one | same transaction as the proposal's state write | — |

## Call graph

```go
// Production — merge_local
proposals worker: Confirmed merge_local
  → mainlock.Acquire(repo, owner "git_action:proposal:<id>")   // denied → Failed{Precondition, retryable, holder}
  → landing.MergeStart{Req, Fresh | Recovering}
  → Observe → git.Status(main), git.Head, git.BranchHead(branch), git.MergeHead, git.IsAncestor, git.Status(worktree)
  → Predict → git.MergeTreeWriteTree(HEAD, branchHead)        // git ≥ 2.38; older → PredictUnsupported
  → RunMerge → git.MergeNoFF(dir, branchHead, message)        // hooks not run, same as commit
  → on conflict: git.MergeAbort(dir) → Observe again
  → MarkWorktreeMerged → db (expected-status update + audit)
  → result → proposals Executed{commit} | Failed{kind, message, retryable}
  → mainlock.Release

// Production — remove_worktree
proposals worker: Confirmed remove_worktree
  → landing.RemoveStart
  → ObserveWorktree → platform.ResolveRoot(path vs root), git.Status(worktree), tasks.Live(task), builds/verify.Live(worktree),
                       fs.Stat(staged), git.WorktreeList(repo) (prunable count)
  → MoveToStaging → platform.MoveDir(path, staged)            // atomic rename; error → platform.ClassifyLock(err), platform.LockHolders(path)
  → DeleteStaged → platform.RemoveStaged(root, staged)        // under .removing only, never follows links
  → PruneWorktrees → git.WorktreePrune(repo)                  // only if no other prunable entry
  → MarkRemoving / MarkRemoved → db
  → result → Executed | Failed{Exec, retryable, message with holders}

// Tests
core: run(state, inputs)  — no shell
shell: executors with fake Git, fake Platform (scripted locks), fake Store, fixed Clock
```

No main-checkout lock for removal: the worktree is under the data folder, and the rename itself fails if any process uses it, which also covers a run that starts between observation and move.

## Failures

| Node | Failure | Class | Handling |
|---|---|---|---|
| Observe (git status/head) | git error, repo gone | retry once, then domain outcome | `MergeFailed{exec, retryable}` / `RemoveRefused{unreadable}` |
| `mainlock` | held by a build/pull/task | domain outcome | proposal `Failed{Precondition, retryable}` naming the holder |
| Predict | git too old | domain outcome | `PredictUnsupported` → merge, abort if it conflicts |
| Predict | other error | domain outcome | `MergeFailed{exec}` |
| RunMerge | conflict | domain outcome | Abort → `Conflicted{files}` |
| RunMerge | no git identity | domain outcome | `MergeFailed{identity_missing}`; message "Set git user.name and user.email" |
| RunMerge | any other error | domain outcome | Settling: clean → `MergeFailed`; dirty → `NeedsHuman` |
| Abort | error | escape hatch | `NeedsHuman` with the exact command for the user |
| `MoveToStaging` | sharing violation / access denied / path too long | domain outcome | `Locked{class, holders}`; nothing changed; message "Something may be using files in this folder (for example an editor, a terminal or a dev server). Close it and retry." Holders from Restart Manager when it answers; never killed |
| `DeleteStaged` | partial failure (a file locked after the move, antivirus) | domain outcome | `Stuck`; Retry resumes; the row stays `removing` and the UI shows "Removal is not finished" |
| `PruneWorktrees` | error | escape hatch | `Removed` with a note |
| Crash mid-merge | process died after `RunMerge` | retry | startup reconcile marks the proposal interrupted; a Retry starts `Recovering`: `AlreadyMerged` → success; own `MERGE_HEAD` → Abort → retryable failure |
| Crash mid-removal | | retry | Retry observes the staged folder and resumes `Deleting` |
| Panic in an executor step | | die | recovered by the proposals worker; proposal `Failed{Interrupted}` |

Errors are translated at each layer: executors map `landing` outcomes to `proposals.FailKind` (`Precondition`, `Stale`, `Exec`, `Interrupted`) and plain messages; callers never see git or Win32 error types.

## Requirements

| Capability | Live | Test | Contract |
|---|---|---|---|
| `LandingGit` (`Status`, `Head`, `BranchHead`, `MergeHead`, `IsAncestor`, `MergeTreeWriteTree`, `MergeNoFF`, `MergeAbort`, `WorktreeList`, `WorktreePrune`) | `internal/git` (GIT1) | `gittest` fake | `GitWriterContract` merge clauses: clean merge makes a two-parent commit and never pushes; conflict leaves the tree restorable; `merge-tree` reports files; abort with nothing in progress is a no-op; identity missing is a distinct error; CRLF files merge unchanged; paths with spaces. Fake and real git (local repos). |
| `LandingPlatform` (`MoveDir`, `RemoveStaged`, `ClassifyLock`, `LockHolders`, `ResolveRoot`) | `internal/platform` (P1) | fake with scripted lock errors | `LandingPlatformContract`: rename fails while a handle is held in the folder (Windows) and nothing changed; `RemoveStaged` refuses a path outside `.removing`; it removes a junction without touching its target (canary folder survives); long paths; classification of sharing/access/path-too-long. |
| `WorktreeStore` (status monotone update + audit) | `internal/db` | in-memory | monotone `active→merged→removing→removed`; wrong expected status is a no-op |
| `LiveProbe` (`TaskLive`, `BuildLive` for a worktree) | tasks + builds/verify services | map | clause per source |
| `MainLock` | `mainlock.Registry` | existing fake | existing `MainLockContract` |
| `Clock`, `IDs` | system | fixed | — |

## Boundary

| Source | Parser | Produces | Rejects |
|---|---|---|---|
| `merge_local` / `remove_worktree` action body (API, MCP is not offered these) | `ParseAction` additions in `core/proposals` (foundations) | `MergeReq` / `RemoveReq` fields | unknown fields, trailing data, branch name rules (existing), `ExpectBranchHead` not 40/64 hex, worktree ID invalid, path not given by the client (derived from the row, never from the body) |
| `git merge-tree --write-tree --name-only` output | `ParseMergeTree` | `Predicted` | output without a tree OID line, file names with control characters, exit code mapping (0 clean, 1 conflicts, other = error) |
| `git worktree list --porcelain` | `ParseWorktreeList` (existing, extended with `prunable`) | prunable count | unknown records are ignored, malformed lines rejected |
| `git status --porcelain=v2 -z` | existing status parser | `Clean` | — |
| Win32 error from rename/delete | `ClassifyLock` | `LockClass` | unknown error → `other` (never an empty class) |
| Restart Manager result | `LockHolders` | process names (≤ 5) | names with control characters dropped; empty on any API failure |
| `worktrees` row | `StateFromRow` | `RemoveReq` | path outside the root, status unknown, `removing` with no staged folder name |

Each parser gets: accepted ⇒ valid, one test per rejection rule, a fuzz target (`FuzzParseMergeTree`, `FuzzStagingPath`, `FuzzParseMergeReq`).

## Files

Core: `internal/core/landing/{types,merge_state,merge_input,merge_command,merge_step,remove_state,remove_input,remove_command,remove_step,staging,message}.go` and tests (L1–L9, R1–R9, fuzz).
Shell: `internal/proposals/{exec_merge,exec_remove,landing_facts}.go` (PX1), `internal/git/{merge,mergetree,worktree_prune}.go` (GIT1), `internal/platform/{movedir,removestaged,lockinfo}*.go` (P1), `internal/db` worktree status update and queries, `internal/archtest` rule change (one allowed delete function, named).
Contract suites: `internal/git/gittest` merge clauses; `internal/platform/platformtest` landing clauses.

Changes to the plan: **P1 also owns `MoveDir` and `RemoveStaged`** (platform), **GIT1 also owns `IsAncestor`, `MergeHead` and `WorktreePrune`**, and foundations add `WorktreeStatus` `removing` and the two proposal kinds to the SQL `CHECK`.

## Open questions

1. **Rename-then-delete instead of `git worktree remove`** (section "Change to decision 1"). Recommend yes; it needs your OK because it adds a deletion helper to `internal/platform`.
2. **Worktree must be clean to merge** (`worktree_dirty` refuses). Recommend yes: the merge then equals what was reviewed and a later removal is possible.
3. **Merge target is the main checkout's current branch at proposal time**, not necessarily the default branch; a different current branch at run time refuses (`wrong_branch`). Recommend yes.
4. **`merge-tree --write-tree` needs git ≥ 2.38**; older git falls back to merge-then-abort. `ygg doctor` warns. Recommend yes.
5. **Holder names are best effort.** The message must stay honest ("may be using"). No process is ever killed.
