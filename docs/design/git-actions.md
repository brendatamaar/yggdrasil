# Git actions — Model Functional Effect design

Covers:

- WS-06: fetch, fetch all, fast-forward-only pull (per repo or bulk), checkout.
- WS-07: cross-repo branch switch for a project. It skips repos with uncommitted changes and reports which were skipped.
- WS-08: open in editor and show in folder. These are stateless and covered under "Call graph" only.

Approved decision (`docs/phases/phase-2.md`): user-initiated actions run directly after the click, with an audit entry. They are not proposals. A pull or checkout is refused while a build runs in that repo's main checkout. Force, reset, clean and branch deletion don't exist (invariant 1); `internal/git` can't run them.

- Core: `internal/core/repos/actions.go`.
- Shell: `internal/repos/actions.go`.
- API: `internal/api/repos_actions.go`.

Every action is a **batch**, even a single-repo one: a batch of one item. Batches live in memory while they run. The audit log is the lasting record.

## State map

```go
type Actions struct {
    Batches map[BatchID]Batch
    Running map[core.RepoID]BatchID   // per-repo lock: at most one git action per repo
    Max     int                       // concurrent items across all batches (default 4)
}
type Batch struct {
    ID      BatchID
    Kind    ActionKind                // fetch | pull | checkout | switch
    Branch  string                    // checkout / switch target ("" for fetch, pull)
    Actor   core.Actor
    Started time.Time
    Items   []Item                    // stable order: project, then repo name
}
type Item struct {
    Repo    core.RepoID
    Attempt int
    State   ItemState                 // sealed, below
}
```

`ItemState` (sealed):

- `Pending{}`: waiting for its turn (the per-repo lock or the `Max` slots).
- `Checking{Attempt}`: switch only. Reading fresh status before deciding whether to check out.
- `Running{Attempt}`: the git command is running.
- `Done{Outcome, At}`: finished, whether it worked or was refused by git.
- `Skipped{Reason, At}`: not run. Reasons:
  - `dirty` (switch with uncommitted changes);
  - `no_such_branch`;
  - `build_running` (a build in the main checkout);
  - `untracked` (repo untracked meanwhile);
  - `cancelled` (the user cancelled the batch).

`Outcome` (sealed):

- `Fetched{}`
- `Pulled{From, To string}`
- `UpToDate{}`
- `PullRefused{Reason git.FFReason}` (detached | no_upstream | diverged | dirty | other)
- `CheckedOut{Branch}`
- `CheckoutRefused{Reason}` (dirty_conflict | other)
- `Failed{Kind}` (network | auth | timeout | git)

A batch is **finished** when every item is `Done` or `Skipped`. A finished batch is kept for 10 minutes for the UI, then dropped from memory.

## Inputs

| Input | From | Fields |
|---|---|---|
| `StartBatch` | user (repo page, projects page bulk bar, branch-switch dialog) | `ID, Kind, Branch, Repos []core.RepoID, Busy map[RepoID]BusyReason, Actor, Now` |
| `CancelBatch` | user | `ID, Actor, Now` |
| `StatusChecked` | interpreter (switch only) | `Batch, Repo, Attempt, Dirty bool, HasBranch bool, Now` |
| `ActionDone` | interpreter | `Batch, Repo, Attempt, Outcome, Now` |
| `BuildStarted` | builds service (repos whose main checkout now has a running build) | `Repos []RepoID, Now` |
| `Sweep` | shell ticker (every 1 min) | `Now` |

`Busy` is a snapshot from the builds service, taken by the shell when the batch starts. `BuildStarted` keeps it current for items that haven't started yet.

## Transition table

`start(item)` runs for every `Pending` item whose repo is not in `Running`, while fewer than `Max` items are `Checking` or `Running`, in batch then item order:

- a switch item → `Checking{a+1}` and `CheckStatus`;
- any other item → `Running{a+1}` and `RunGit`.

Both set `Running[repo] = batch`. `start` runs after every input.

| From | Input | Guard | To | Commands |
|---|---|---|---|---|
| — | StartBatch | `len(Repos) ≥ 1`; Kind valid; Branch non-empty iff checkout or switch; checkout has exactly 1 repo | new Batch. Items for repos in `Busy` with kind pull, checkout or switch → `Skipped{build_running}`; others → `Pending` | `WriteAudit(git.<kind>.start)`, `Publish(BatchChanged)` + starts |
| Pending | BuildStarted | repo in list, kind ≠ fetch | `Skipped{build_running}` | `Publish` |
| Checking{a} | StatusChecked | attempt `a`; `Dirty` | `Skipped{dirty}`; release the repo lock | `Publish` + starts |
| Checking{a} | StatusChecked | attempt `a`; `!HasBranch` | `Skipped{no_such_branch}`; release | `Publish` + starts |
| Checking{a} | StatusChecked | attempt `a`; clean and has the branch | `Running{a}` (same attempt, the lock stays) | `RunGit(checkout Branch)` |
| Running{a} | ActionDone | attempt `a` | `Done{Outcome, Now}`; release the repo lock | `WriteAudit(git.<kind>)` (one per repo, outcome in the detail), `RefreshStatus(repo)`, `Publish` + starts |
| Pending | CancelBatch | — | `Skipped{cancelled}` | `Publish` |
| Checking / Running | CancelBatch | — | unchanged (a running git command is never killed half way) | — |
| batch finished | Sweep | `Now − last item time ≥ 10 min` | batch removed | — |
| any | StatusChecked / ActionDone | stale attempt or unknown item | no change | none |

`WriteAudit(git.<kind>.start)` names the actor, the kind, the branch and the repo IDs. The per-repo audit entries record the outcome, for example `Pulled{From, To}`. Fetch is audited too, because it moves remote-tracking refs (invariant 8).

**Any other (state, input) pair: no change, no commands.**

## Invariants

| # | Invariant | Property test |
|---|---|---|
| A1 | At most one item per repo is `Checking` or `Running` across all batches (`Running` map consistent with item states). | `TestPropOneActionPerRepo` |
| A2 | At most `Max` items are `Checking` or `Running`. | `TestPropActionsBounded` |
| A3 | No `RunGit` for pull, checkout or switch is ever emitted for a repo that was in `Busy` or `BuildStarted` before its item started. | `TestPropNoGitWhileBuildRuns` |
| A4 | Switch: `RunGit(checkout)` is emitted for a repo only after a `StatusChecked` with `Dirty == false` and `HasBranch == true` for the same attempt. | `TestPropSwitchOnlyClean` |
| A5 | Every item ends `Done` or `Skipped` once every emitted command has been answered (liveness). `CancelBatch` never leaves an item `Pending`. | `TestPropBatchesFinish` |
| A6 | Every `Done` item has exactly one per-repo `WriteAudit`, and every batch has exactly one start `WriteAudit`. | `TestPropAuditPerAction` |
| A7 | Work-conserving: no startable item stays `Pending` while slots are free. | `TestPropNoIdleWhilePending` |
| A8 | Commands are only `CheckStatus`, `RunGit{fetch, pull, checkout}`, `WriteAudit`, `RefreshStatus` and `Publish`. No other git operation exists in the command set. | `TestPropOnlySafeGit` |

## Commands

| Command | Cardinality | Idempotent | Produces |
|---|---|---|---|
| `CheckStatus{Batch, Repo, Attempt, Branch}` | one | yes (read-only: `git status` + `git rev-parse --verify` local or `origin/<branch>`) | `StatusChecked` |
| `RunGit{Batch, Repo, Attempt, Op}` | one | fetch: yes; pull `--ff-only`: yes in effect (a second run is `UpToDate`); checkout: yes (same branch → no-op). Never retried automatically | `ActionDone` |
| `WriteAudit` | one | committed in its own `Store.Commit`; the action itself is not a database state, so there is nothing else to commit with it | — |
| `RefreshStatus{Repo}` | one | yes | repo-status `RefreshRequested{git_action}` |
| `Publish(BatchChanged)` | one | yes | — |

## Call graph

```go
// Production
POST /api/git/batches {kind, repos[], branch?}
  → repos.ParseBatchRequest (boundary) → kind, ids (active only), branch name
  → GitActionsService.Start(ctx, req)
      → busy := buildService.BusyMainCheckouts(ids)          // snapshot
      → actionsOwner.send(StartBatch{ID: ids.New("gab"), …, Busy: busy, Now})
  → 202 {batchId}

actionsOwner (one goroutine owns Actions; inputs on a channel; 1 min Sweep ticker)
  input → repos.Act(state, input) [core] → cmds
  WriteAudit → Store.Commit(tx.AppendAudit(audit.Prepare(…)))   // before the git command runs for the start entry
  CheckStatus → worker: git.Status(ctx 30 s, path) + git.BranchExists(ctx, path, branch) → StatusChecked
  RunGit → worker (errgroup):
      fetch    → git.Fetch(ctx 120 s, path, "")
      pull     → git.PullFFOnly(ctx 120 s, path)     → *git.FFError → PullRefused{Reason}
      checkout → git.Checkout(ctx 60 s, path, branch) → refusal → CheckoutRefused
      → git error classes → Failed{network|auth|timeout|git} (internal/git.classify)
      → ActionDone
  RefreshStatus → statusService.Request(ids, git_action)
  Publish → hub.publishNext(RepoTopic{id} / ReposTopic{}, BatchDTO)
POST /api/git/batches/{id}/cancel → CancelBatch
GET  /api/git/batches/{id}        → snapshot from the owner

// WS-08 (stateless)
POST /api/repos/{id}/open {editor?, file?}   → repos.ParseOpen → platform.OpenEditor(ctx, editor, path[, file]) (outside the job, PL-05)
POST /api/repos/{id}/reveal {file?}          → platform.OpenFolder(ctx, path[, file])
  editor default: the settings table (key "editor": vscode | cursor | idea), or detected on PATH
```

```go
// Tests
repos.RunActions(initial, inputs) (core) → A1–A8
service tests: fake GitRunner (scripted outcomes, delays) + fake BuildBusy
git tests (exist) cover the real fetch/pull/checkout on temp repos with a local bare remote
```

## Failures

| Node | Failure | Class | Handling |
|---|---|---|---|
| ParseBatchRequest | unknown kind; invalid branch name (`checkName` rules); untracked or unknown repo; more than 500 repos | domain outcome | 400 or 404 |
| StartBatch | repo busy with a build | domain outcome | `Skipped{build_running}` for that item; the rest run |
| CheckStatus | dirty, no such branch | domain outcome | `Skipped{dirty / no_such_branch}`, listed in the batch report (WS-07) |
| CheckStatus | git error or timeout | domain outcome | `Done{Failed{…}}` (treated like a failed action) |
| fetch / pull | network unreachable, DNS | domain outcome | `Failed{network}`; "Retry failed" in the UI starts a new batch for the failed repos |
| fetch / pull | authentication (credential helper refused) | domain outcome | `Failed{auth}`, with "fix your git credentials in a terminal". ygg never prompts and never holds git credentials |
| pull | cannot fast-forward | domain outcome | `PullRefused{reason}` + `FFError.UserMessage()` |
| checkout | local changes would be overwritten | domain outcome | `CheckoutRefused{dirty_conflict}` |
| any git | timeout | domain outcome | `Failed{timeout}`; the process tree is killed by ctx. git leaves the repo consistent: fetch and pull are atomic per ref, and checkout either finishes or refuses |
| WriteAudit | DB error | die for that batch | start entry fails → batch not started, 503. A per-repo entry fails → logged loudly, and the batch continues (the git change already happened) |
| core | invariant broken | die | recovered by the owner, logged |

There is no automatic retry. Every retry is a new batch the user starts, and it is audited.

## Requirements

| Capability | Live | Test | Contract |
|---|---|---|---|
| `GitRunner` (`Fetch`, `PullFFOnly`, `Checkout`, `Status`, `BranchExists`) | `internal/git` | scripted fake | `GitRunnerContract`: fetch from a local bare remote; pull fast-forward; pull up to date; pull diverged → FFError diverged; pull dirty overwrite → dirty; detached → detached; checkout clean; checkout with conflicting changes → refused; BranchExists local and remote-only; ctx timeout. Runs against the fake and against real git on `t.TempDir` repos |
| `BuildBusy` (`BusyMainCheckouts(ids) map[RepoID]BusyReason`, plus a `BuildStarted` subscription) | builds service | static map | — |
| `Store.Commit` (audit) | `internal/db` | SQLite temp | existing |
| `StatusRefresher`, `Publisher`, `Clock`, `IDs` | services, hub, shell | recorder / fixed | — |
| `EditorOpener` (WS-08) | `platform.OpenEditor` / `OpenFolder` | recorder | platform tests exist (outside the job object) |

## Persistence and delivery

- **Batches:** in memory only. A crash mid-batch loses the batch view, but each git command either finished or not; the per-repo audit entries show what ran. On restart there is nothing to reconcile: git state is the truth, and repo-status rereads it.
- **Audit:** the batch start entry is committed **before** any git command runs. Each per-repo entry is committed right after its command returns.
- **Known race (accepted for Phase 2):**
  - a build can start in a main checkout between the `Busy` snapshot and a checkout. `BuildStarted` closes most of that window. The builds interpreter doesn't check for git actions;
  - Phase 3 brings a shared main-checkout lock (AG-11: one agent per main checkout), and builds and git actions will both take it.
- **Shell invariants (chaos):**
  - S1: no git command other than fetch, `pull --ff-only` and checkout is ever run by this feature (the `internal/git` invariant test already scans for destructive commands);
  - S2: never two git commands on the same repo at once;
  - S3: no git process outlives the service.

## Boundary

| Source | Parser | Produces | Rejects |
|---|---|---|---|
| HTTP `POST /api/git/batches` | `repos.ParseBatchRequest` | `StartBatch` fields | unknown kind, missing or extra branch, branch failing `git check-ref-format` rules (reuse `internal/git.checkName`), duplicate repo IDs, inactive repos, checkout with ≠ 1 repo, more than 500 repos |
| HTTP open/reveal | `repos.ParseOpen` | editor (closed set), relative file path inside the repo | unknown editor, absolute file, `..` escaping the repo, NUL |
| git results | `internal/git` (FFError, Error classes) | `Outcome` | — (total mapping, tested) |

Each parser gets round-trip, accepted ⇒ valid, one test per rejection rule, and fuzz: `FuzzParseBatchRequest`, `FuzzParseOpen`.

## Files

- **Core:** `internal/core/repos/actions.go` (types, `Act`, `RunActions`) and `actions_prop_test.go` (A1–A8), written first.
- **Shell, `internal/repos`:**
  - `actions.go` (owner goroutine, interpreter);
  - `actions_parse.go`;
  - `repostest/gitrunner_contract.go`.
- **`internal/git`:** `BranchExists` (read-only).
- **`internal/api`:** `repos_actions.go` (handlers, `GitActionsService`) and the batch DTOs in a new `types_actions.go`.
- **Builds service** (B2 owner): it exposes `BusyMainCheckouts` and emits `BuildStarted`. Note this in the B2 brief.

## Open questions

Approved by the human on 2026-10-18 with every recommendation below.

1. **Concurrency:** `Max = 4` across batches. Fetch is network-bound, and 4 keeps self-hosted GitLab happy. *Recommend 4.*
2. **Checkout of a remote-only branch:** `git checkout <name>` creates a local tracking branch from `origin/<name>`. git does this by default (DWIM), and it is still a non-destructive checkout. *Recommend allowing it*, and saying "created local branch tracking origin/<name>" in the outcome.
3. **"Fetch all" scope:** every active repo in the project, or every active repo when started from the home page. *Recommend as stated.*
