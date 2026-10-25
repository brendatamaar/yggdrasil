# Proposals — Model Functional Effect design
Profile: service

Covers:

- RV-02: every commit, push, MR, install, agent-file change and file removal is a proposal that needs UI confirmation. Invariants 1, 2, 3, 4, 8.
- GH-03: MR creation through GitLab push options, as a proposal.
- MCP-05: the `propose_*` tools create proposals (the MCP side is in `docs/design/mcp.md`).
- RV-03 (the grouping only): proposals of one task share a change set. Merge order and linked MRs are Phase 4.

Core: `internal/core/proposals` (no JSON tags; DTOs in `internal/api`).
Shell: `internal/proposals` (service, executors), `internal/git` (new `commit.go`, `push.go`), `internal/api/proposals.go`.

Phase 3 executes six kinds: `commit`, `push`, `merge_request`, `agent_file`, `delete_files`, `install`. `publish` (Phase 6) and `subtasks` (Phase 7) stay in the enum but `ParseAction` refuses them, so no proposal that cannot run can be created.

## State map

A proposal is a header plus a sealed `State`:

```go
type Proposal struct {
    ID          core.ProposalID
    ChangeSet   core.ChangeSetID
    Task        core.TaskID
    Repo        core.RepoID
    Source      Source          // Agent{Task} | User
    Action      Action          // sealed; immutable after creation
    ActionHash  string          // sha256 of the canonical action; what the user confirms
    Summary     string          // agent text, shown as plain text next to Describe(Action)
    Created     time.Time
    State       State
}
```

| State | Data | `core.ProposalStatus` |
|---|---|---|
| `Pending` | | `pending` |
| `Confirmed` | `Attempt`, `At`, `By` (executing) | `confirmed` |
| `Rejected` | `At` | `rejected` |
| `Executed` | `At`, `Result` (commit ID, MR URL, moved paths, build ID, ...) | `executed` |
| `Failed` | `At`, `Kind` (`Stale` / `Precondition` / `RepoUntracked` / `Rejected` by the remote / `UnsafeRemote` / `Interrupted` / `ExecError`), `Message`, `Retryable` | `failed` |

`Action` (closed set; each is plain data):

| Action | Fields |
|---|---|
| `Commit` | `Worktree` (or main checkout), `Paths []RelPath` (never "all"), `Message`, `ExpectHead`, `ExpectDiffHash` |
| `Push` | `Worktree`, `Branch`, `Remote`, `ExpectHead` |
| `MergeRequest` | `Worktree`, `Branch`, `Remote`, `Target`, `Title`, `Description` (one line); includes the push |
| `AgentFile` | `Target` (worktree or main checkout), `Path` (one of the protected agent-config names), `Content`, `ExpectHash` (hash of the file now, or absent) |
| `DeleteFiles` | `Target`, `Paths []RelPath` (moved, never deleted) |
| `Install` | `Repo`, `Command` (the toolchain's install step, by ID) |

`Describe(Action) []Line` is pure: the exact words the confirmation dialog shows ("Commit 3 files to branch `ygg/swift-fern` of shop-api", the paths, the message). The UI shows `Describe`, never only the agent's `Summary`.

## Inputs

| Input | Fields |
|---|---|
| `Created` | `Proposal` (header + action), `Facts` (repo tracked, path checks passed, pending count for the task), `Now` |
| `Confirm` | `Hash`, `By`, `RepoTracked`, `Now` |
| `Reject` | `By`, `Now` |
| `ExecDone` | `Attempt`, `Result`, `Now` |
| `ExecFailed` | `Attempt`, `Kind`, `Message`, `Retryable`, `Now` |
| `Retry` | `By`, `RepoTracked`, `Now` (a user click; never automatic) |
| `Recovered` | `Now` (once at startup) |
| `Reconciled` | `Attempt`, `Outcome` (`Done{Result}` / `NotDone` / `Unknown`), `Now` |

## Transition table

| From | Input | Guard | To | Commands |
|---|---|---|---|---|
| none | `Created` | facts ok, pending count under the cap (50 per task), kind supported | `Pending` | `Persist`, `Audit{proposal.create}`, `Publish` |
| none | `Created` | any fact fails | none | `Refuse{Reason}` (the MCP/API caller gets a plain message) |
| `Pending` | `Confirm{h}` | `h == ActionHash` and `RepoTracked` | `Confirmed{Attempt 1}` | `Execute{ID, 1, Action}`, `Audit{proposal.confirm}`, `Publish` |
| `Pending` | `Confirm{h}` | `h != ActionHash` | `Pending` | `Refuse{HashMismatch}` (the user saw something else) |
| `Pending` | `Confirm{h}` | repo untracked | `Pending` | `Refuse{RepoUntracked}` |
| `Pending` | `Reject` | | `Rejected` | `Audit`, `Publish` |
| `Confirmed{a}` | `ExecDone{a}` | | `Executed` | `Audit{proposal.execute}`, `Publish` |
| `Confirmed{a}` | `ExecFailed{a}` | | `Failed` | `Audit{proposal.fail, kind}`, `Publish` |
| `Failed{Retryable}` | `Retry` | `RepoTracked` | `Confirmed{a+1}` | `Execute{ID, a+1, Action}`, `Audit{proposal.retry}`, `Publish` |
| `Confirmed{a}` | `Recovered` | | `Confirmed{a}` | `Reconcile{ID, a, Action}` (read-only, no `Execute`) |
| `Confirmed{a}` | `Reconciled{a, Done}` | | `Executed` | `Audit`, `Publish` |
| `Confirmed{a}` | `Reconciled{a, NotDone or Unknown}` | | `Failed{Interrupted, Retryable}` | `Audit`, `Publish` |
| any | input with a stale `Attempt` | | unchanged | none |
| any other (state, input) pair, including `Reject` or `Confirm` on a decided proposal | | | unchanged | none |

There is no input that edits an `Action`. A proposal that needs a change is rejected and a new one created.

## Invariants

| # | Invariant | Property test |
|---|---|---|
| P1 | `Execute` is emitted only by `Confirm` with the matching `ActionHash`, or by `Retry`; never from `Pending` without a confirm | `TestPropExecuteOnlyAfterConfirm` |
| P2 | `Execute` is emitted once per `(ID, Attempt)` | `TestPropExecuteOncePerAttempt` |
| P3 | `Action` and `ActionHash` never change across any sequence of inputs | `TestPropActionImmutable` |
| P4 | `Rejected` and `Executed` are terminal; `Failed` leaves only through `Retry` and only when `Retryable` | `TestPropTerminalStates` |
| P5 | A `Confirm` with a different hash changes nothing and emits no `Execute` | `TestPropWrongHashDoesNothing` |
| P6 | `Recovered` emits `Reconcile` for each `Confirmed` and no `Execute` | `TestPropRecoveryNeverExecutes` |
| P7 | Every status change is accompanied by `Audit` and `Publish` | `TestPropStatusChangeAudited` |
| P8 | `ActionHash` is a function of the action alone (equal actions, equal hashes; any field change, a different hash) | `TestPropHashCoversEveryField` |
| P9 | `Describe(a)` names every path, the branch, the remote and the message of `a` | `TestPropDescribeShowsEverything` |
| P10 | The `Action` set has no variant that can express force, reset, clean, branch deletion or file deletion; for every action the shell's git argv contains none of `--force`, `-f`, `+` refspecs, `:branch`, `--delete`, `reset`, `clean`, `branch -d/-D`, `rm` (shell test over a generator) | `TestPropGitArgvHasNoDestructiveForm` |
| P11 | All proposals of a task have the same `ChangeSet` | `TestPropOneChangeSetPerTask` |
| P12 | An untracked repo never reaches `Confirmed` | `TestPropUntrackedRepoNeverConfirmed` |

## Commands

| Command | Cardinality | Idempotent | Produces |
|---|---|---|---|
| `Persist` | one | yes with key `ID` | none |
| `Execute{ID, Attempt, Action}` | one (install: the wait is a stream of build events) | **no**, but fenced: each executor checks its precondition (`ExpectHead`, `ExpectHash`, remote head) first, so a second run after a crash does nothing harmful | `ExecDone` / `ExecFailed` |
| `Reconcile{ID, Attempt, Action}` | one | yes (read-only) | `Reconciled` |
| `Audit`, `Publish` | one | yes | none |
| `Refuse{Reason}` | one | yes | none (response only) |

## Call graph

```go
// Production: an agent proposes (MCP tool propose_commit etc.)
mcp tool handler
  → mcp.ParseAction(kind, args) (boundary)               // rejects unknown kinds, bad paths
  → proposals.Service.Create(taskID, action, summary)
      → facts: repo tracked? paths inside the task's worktree? pending count?
      → ChangeSet.Ensure(task)                           // same transaction
      → Step(none, Created)                              // core
      → Store.Commit { CreateProposal, AppendAudit }     // one transaction
      → hub.publish(proposals topic)

// Production: the user confirms (UI only; MCP has no confirm tool)
POST /api/proposals/{id}/confirm {hash}                  // Host/Origin checked
  → Service.Confirm → Store.Load → Step(Confirm)
  → Store.Commit { SaveProposal(expectedVersion), AppendAudit }
  → interpret Execute (goroutine owned by the service group, one per repo at a time)
      → exclusivity: per-worktree mutex, or mainlock for the main checkout
      → executor by kind (below)
      → Step(ExecDone | ExecFailed) → Store.Commit → hub.publish

// executors (all git through internal/git → platform.Start, clean env, GIT_TERMINAL_PROMPT=0)
Commit:   check HEAD == ExpectHead and diff hash of Paths == ExpectDiffHash
          → git -c core.hooksPath=<empty> add -- <paths>        (literal pathspecs, never -A)
          → git -c core.hooksPath=<empty> commit --no-verify -m <message>   (author from the user's git config)
          → result: new commit ID
Push:     check HEAD == ExpectHead; remote URL has no credentials
          → git push --porcelain <remote> refs/heads/<b>:refs/heads/<b>  (no force, ever)
MergeRequest (GitLab only): as Push plus -o merge_request.create -o merge_request.target=<t>
          -o merge_request.title=<t> [-o merge_request.description=<d>]
          → parse "View merge request" URL from the remote output → SafeWebURL → result
AgentFile: check ExpectHash of the target file → write temp file in the same folder → rename
DeleteFiles: for each path inside Target and not under .git: rename into <Target>/_to_delete/<relative path>
          (name clash → numeric suffix); no os.Remove anywhere
Install:  builds.Service.Enqueue(single install step, main checkout) under mainlock → wait for the build → result

// Production: startup
Store.LoadByStatus(confirmed) → Step(Recovered) → interpret Reconcile
  Commit: HEAD's parent == ExpectHead and its message equals the action's → Done
  Push/MR: git ls-remote <remote> <branch> == local HEAD → Done (MR: Unknown, since GitLab is not asked)
  AgentFile: file hash == hash(Content) → Done
  DeleteFiles: every source gone and every destination present → Done
  Install: Unknown
```

```go
// Tests
Run(initial, inputs)                         // core, no shell
interpret Execute → gitstest.Fake / real git in t.TempDir() + local bare remote
install → buildstest fake
```

## Failures

| Node | Failure | Class | Handling |
|---|---|---|---|
| `Created` facts | path outside the worktree, protected path for a non-agent-file kind, unknown kind, over the pending cap | domain outcome | `Refuse`: plain message to the agent through the MCP error and nothing persisted |
| `Store.Commit` | version conflict | retry | reload and re-run `Step` (no command ran yet) |
| Commit executor | `HEAD != ExpectHead` or diff changed | domain outcome | `Failed{Stale}`, not retryable (the proposal describes a tree that no longer exists): the user rejects it and asks the agent again |
| Commit executor | nothing staged, author unset, git error | domain outcome | `Failed{ExecError, plain message}`; author unset says how to set it; retryable except "nothing to commit" |
| Push executor | remote refuses (non-fast-forward, protected branch, auth) | domain outcome | `Failed{Rejected by remote}` with the remote's reason redacted; not retryable for non-ff (we never force); retryable for network errors |
| Push executor | remote URL carries credentials | domain outcome | `Failed{UnsafeRemote}`; nothing is sent (invariant 4) |
| Push / MR executor | timeout after the request left (outcome unknown) | escape hatch | `ExecFailed{Interrupted}`; `Retry` first runs the same `ls-remote` check that `Reconcile` uses, so a push that did land becomes `Executed` |
| MR executor | remote is not GitLab, or no MR URL in the output | domain outcome | refused at `Created` for non-GitLab; a missing URL still counts as pushed: `Executed` with "pushed; open the MR from the repo page" |
| AgentFile executor | `ExpectHash` differs | domain outcome | `Failed{Stale}` |
| DeleteFiles executor | a path is gone or locked (Windows) | domain outcome | `Failed{ExecError}` listing which; files already moved stay moved and `Reconcile` accounts for them |
| Install executor | build fails | domain outcome | `Failed{ExecError}` with the build ID |
| Repo untracked after confirm | | domain outcome | `Failed{RepoUntracked}` at the executor's own check |
| Executor goroutine | panic | die | recovered, `ExecFailed{Interrupted}`, stack logged |
| Process crash while `Confirmed` | | domain outcome | `Recovered` → `Reconcile` → `Executed` or `Failed{Interrupted, Retryable}`; never auto-executed |

No outbox: every executor is fenced by a read-only precondition and `Reconcile`, as decided in the Phase 2 builds design ("revisit the outbox if a later feature needs to resume commands"). Execution never resumes on its own; the user clicks `Retry`.

## Requirements

| Capability | Live | Test | Contract |
|---|---|---|---|
| `ProposalStore` (`Create`, `Load`, `LoadByStatus`, `Save` with version, `EnsureChangeSet`) | `internal/db` | `dbtest` | round trip for every action kind and state; payload with an unknown field or kind rejected; version conflict; change set created once per task; contract: `Failed.Message` and `ExecFailed.Message` are free text (git or host output), so the shell must redact tokens and paths before it persists or shows them |
| `GitWriter` (`Head`, `DiffHash`, `Add`, `Commit`, `Push`, `LsRemote`, `RemoteURL`) | `internal/git` | `gittest.Fake` (in-memory refs and trees) | `GitWriterContract`: commit creates one commit with the given message and parent; commit with a stale head fails; push to a bare remote moves the ref; push of a non-ff ref is refused and no force is possible; hooks in `.git/hooks` and `core.hooksPath` do not run; credentials in the URL are refused. Real git with a local bare repo |
| `Files` (`WriteAtomic`, `MoveAside`) | `internal/proposals` (os) | none: tests use `t.TempDir()` | `FilesContract`: atomic write replaces content or leaves the old; `MoveAside` keeps the tree and suffixes clashes; path outside the root refused; no remove call exists (archtest) |
| `Builds` (`Enqueue`, `Wait`) | `internal/builds` | `buildstest` fake | existing builds contract |
| `Hosts` (`Kind(remote)`) | `internal/hosts` | fixed map | `Kind` for GitLab, GitHub, Gitea and unknown remotes |
| `Lock` (`mainlock`) | `internal/mainlock` | `mainlocktest.Fake` | `MainLockContract` (tasks design) |
| `Clock`, `IDs` | real | fixed | none |

## Boundary

| Source | Parser | Produces | Rejects |
|---|---|---|---|
| MCP tool arguments | `proposals.ParseAction(kind, json)` | `Action` | unknown kind or field; `publish`, `subtasks`; a path that is absolute, has `..`, a drive, a stream (`:`), a device name, or is empty; more than 200 paths; message empty or over 4 KiB; title over 200; description with a newline (push options cannot carry one); `Content` over 256 KiB; `AgentFile` path not in the protected names; branch name failing the git ref rules; remote name not matching `[A-Za-z0-9._-]+` |
| `proposals.payload` row | `proposals.ActionFromRow` | `Action` | same rules, plus kind/payload mismatch |
| Confirm / reject / retry requests | `api` | `Confirm`, `Reject`, `Retry` | id not valid; hash not 64 hex |
| Remote output (push) | `proposals.ParsePushOutput` | `Result{MRURL}` | URL that fails `SafeWebURL` (scheme, userinfo) |
| Remote URL | `proposals.CheckRemote` | `Remote` | userinfo with a password or token; non-http(s)/ssh/scp forms |

Each parser has a round trip (`ActionFromRow(RowOf(a)) == a`), accepted ⇒ valid, one test per rejection rule, and a fuzz target (`FuzzParseAction`: accepted ⇒ every path relative and inside; `FuzzParsePushOutput`: any accepted URL passes `SafeWebURL`).

## Files

Core (`internal/core/proposals`): `doc.go`, `types.go` (Proposal, Source), `action.go` (sealed `Action`, `Hash`, `Describe`), `state.go`, `input.go`, `command.go`, `step.go`, `parse.go` (`ParseAction`, `ActionFromRow`, `RowOf`), `step_prop_test.go` (P1–P7, P11, P12), `action_prop_test.go` (P3, P8, P9), `parse_test.go`, `fuzz_test.go`.

Shell:
- `internal/proposals`: `service.go`, `interpret.go`, `exec_commit.go`, `exec_push.go`, `exec_mr.go`, `exec_agentfile.go`, `exec_delete.go`, `exec_install.go`, `reconcile.go`, `files.go`, `argv_test.go` (P10), shell tests (crash points, duplicate confirm, concurrent confirms on one repo, hash mismatch).
- `internal/git`: `commit.go`, `push.go` (no force parameters exist), `gittest/`.
- `internal/api`: `proposals.go` (list, get, confirm, reject, retry), DTOs, topic.
- `internal/db`: queries for proposals and change sets (the migration is in the foundations).
- archtest rules: `internal/proposals` and `internal/git` do not call `os.Remove`, `os.RemoveAll`, or build an argv containing the forbidden forms.

## Open questions

1. **Hooks are disabled** (`core.hooksPath` pointed at an empty folder, `--no-verify`) for proposal commits and pushes. A repo's pre-commit hook can be a file the agent wrote (for example `.husky/pre-commit`), which would run agent code when you confirm. Cost: repos that rely on hooks for formatting lose that step; verification (VF-01, Phase 4) runs the verify chain instead. Recommendation: disabled.
2. **Pushes to the repo's default branch are refused** in Phase 3 (a proposal can push a task branch or, in current-branch mode, the checked-out branch unless it is the default). Recommendation: yes.
3. **MR description is one line,** because git push options cannot carry a newline; GitLab only. Multi-line descriptions and GitHub/Gitea PRs need an API token and arrive later (Phase 6). Recommendation: accept, and say so in the confirmation dialog.
4. **After a crash a `Confirmed` proposal is never re-executed on its own;** it becomes `Executed` if the read-only check proves it landed, otherwise `Failed{Interrupted}` with a `Retry` button. Recommendation: yes.
5. **Execution order is confirmation order, with one executor per worktree/main checkout at a time;** "confirm all" confirms in creation order. Recommendation: yes. Smarter ordering (commit before push) is RV-04, Phase 4.
6. **`propose_install` runs through the builds service in the main checkout** (NR-03), under the main-checkout lock. Recommendation: yes.

## Decisions (approved 2026-10-25)

All six recommendations accepted: (1) hooks disabled for proposal commits and pushes; (2) no pushes to the default branch; (3) MR description is one line, GitLab only; (4) no auto-retry after a crash; (5) confirmation-order execution, one executor per worktree or main checkout; (6) install through the builds service under the main-checkout lock.

## Deviation (X1, 2026-10-25)

`Failed` carries the failed `Attempt` (Retry needs a+1; the table persists it). A non-retryable `Failed` proposal is terminal: it stays in the list as history and is not rejected (the design's "user rejects it" means: ignore it and ask again). `Step` computes `ActionHash` itself. Payload JSON is one strict struct per kind (keys: `worktree`|`repo`, `paths`, `message`, `expect_head`, `expect_diff_hash`, `branch`, `remote`, `target`, `title`, `description`, `path`, `content`, `expect_hash`, `step`); MCP arguments use the same names. Shell: use `proposals.IsProtectedPath` for `Facts.PathsOK` on every kind except `agent_file`; `Describe` names the worktree/repo ID, so the API adds "branch X of repo Y" from its own lookups.

## Review gate 1 fixes

- **M1 short names and depth.** `CheckRelPath` rejects any path part holding `~` followed by a digit (Windows 8.3 aliases such as `GIT~1`, `CLAUDE~1`), and compares `.git` with an ASCII-only fold. `IsProtectedPath` matches at any depth with the same lists as `core/guard` (folders `.claude`, `.opencode`, `.codex`, `.gemini`, `.cursor`, `.husky`, `.githooks`; files `.mcp.json`, `CLAUDE.md`, `CLAUDE.local.md`, `AGENTS.md`, `opencode.json`, `GEMINI.md`, `.gitmodules`). A bare folder name is protected too, because committing it stages everything below; an `agent_file` path still has to name a file. A test in `gate1_test.go` imports `core/guard` and fails if the lists differ.
- **Remotes.** `CheckRemote` refuses a leading `-` in the ssh user and in the host.
- **MR links.** `ParsePushOutput(out, remoteHost)` accepts a link only on the remote own host (case-insensitive, port ignored); any other host is `ErrUnsafeURL`.
- **Defence in depth.** `Action.Validate()` re-runs the `ParseAction` rules on each kind and `Step(Created)` refuses an invalid action with `RefuseBadFacts`.
- **Redaction contract.** `Failed.Message` and `ExecFailed.Message` are free text; the shell redacts before persisting (see the `ProposalStore` row).
