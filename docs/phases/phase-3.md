# Phase 3 — Agents and safety → `v0.4.0`

> Status: **plan approved 2026-10-25; designs in progress**
> Source: docs/PLAN.md Phase 3; docs/PRD.md AG-01–AG-15, AG-20–AG-22, SF-05–SF-09, RV-02, GH-03, WE-04, MCP-01–MCP-06, UI-03; decisions D6, D7, D10, D15, D17; docs/spikes.md B4; docs/AGENTS.md 5.5, 5.6, 5.11, 6.

**Exit criterion (PLAN.md):** a Code-mode task on the demo project runs in a worktree with either reference adapter, cannot write outside it or run unlisted commands, and produces confirmable proposals. Rewrite can replace the personal tool for agent work.

---

## Starting state

- `main` is clean at `29eb693`. Tags: `v0.1.0`, `v0.2.0`. **Phase 2 is closed in code and docs but `v0.3.0` is not tagged.** Phase 3 builds on it. I won't tag. Your call whether to tag before we start (see decision 1).
- Already on `main` (Phase 1/2), reused as is:
  - `internal/agents`: `Adapter`, `Run`, `StartOptions`, `Capabilities`, `Event` with `ParentID`, `Execute`, `BuildTree`; `mock` adapter (4 fixtures, one with sub-agents); `claudecode` adapter (Ask mode only, hook with tool allowlist and path check, sub-agent caps, `ownAgentConfig` refusal for repos that ship agent config).
  - `internal/api/tasks.go`: Ask-only create, in-memory goroutine per task, `transition` + `Store.Commit`, `sink` with redaction. No queue, no recovery on restart.
  - `internal/core` types for `Worktree`, `Task`, `ChangeSet`, `Proposal`; tables `worktrees`, `task_repos`, `task_dependencies`, `change_sets`, `proposals`, `task_events`; `repos.trusted`; `Store.Commit`/`Tx.AppendAudit`; version-checked updates.
  - `internal/git`: `WorktreeAdd`, `WorktreeList`, fetch, ff-only pull, checkout, status, diff, log. **No commit, no push yet.**
  - `internal/platform`: `Start`, `KillTask`, `LongPath`, `CanonicalPath`, `ShortDirName`, `CheckPath`, PTY; `platformtest` fake.
  - `internal/safety`: clean env, redactor, log handler. `internal/proposals`, `internal/mcp`, `internal/scheduler` hold only `doc.go`.
  - Web: `Composer`, `TaskPage` (basic transcript), `WorkingNow` widget, widget registry.
- **Not installed here:** the `opencode` CLI. Ollama and the `claude` CLI are.
- Next migration number: **00004** (00001–00003 exist; 00002 and 00003 are frozen after the `v0.3.0` tag).

## Decisions I need from you (each has a recommendation)

1. **Tag `v0.3.0` first?** *Recommend:* run the Phase 2 Windows checklist and tag before Phase 3 fan-out, so a regression in Phase 3 is bisectable against a tag. The 50-repo perf margin (8.0 s of 10 s) was only measured on the dev machine. If you'd rather not wait, Phase 3 starts on untagged `main`.
2. **OpenCode needs a real-CLI spike (AG-03, D6).** I need its output format, its permission/hook model and how it takes a prompt, recorded as fixtures. CLAUDE.md allows a real CLI only in a spike with your agreement in the session. *Recommend:* you install `opencode`, and agree to one short spike with an Ollama model (I'd run it in this session, a handful of tiny prompts, nothing in tests). The result decides OpenCode's `Enforcement`: if it has a pre-tool hook we use it (`hook`); if not it is `limited` and restricted to worktree-only Code mode (SF-06). Either way the exit criterion holds.
3. **Mode switching (AG-10) changes the task model.** *Recommend:* a task keeps one worktree and one session, and gets **turns**; each turn has its own mode. New table `task_turns` (migration 00004). An Ask turn in a Code task's worktree runs read-only under the Ask-mode integrity check (SF-07). `Task.Mode` becomes "mode of the current turn". Alternative (a new task per mode) breaks session resume.
4. **Repo trust (SF-09) is bound to a content hash,** not a boolean. *Recommend:* trust records the hash of the detected agent config files (`.claude/settings*.json`, `.claude/agents/**`, `.mcp.json`, OpenCode's equivalents). If the files change, the repo is untrusted again and the user sees the diff. `repos.trusted` stays as the flag; a new `repos.trust_hash` column holds the hash.
5. **MCP transport (MCP-01–06).** *Recommend:* streamable HTTP on the existing `127.0.0.1` listener at `/mcp`, with a **per-task bearer token** (random, valid only while the task runs, written to the agent's MCP config file in the task's temp dir, redacted from logs and events). The token identifies the task, so tool visibility is limited to that task's project and tracked repos (SF-02). The alternative, a stdio shim `ygg mcp --task <id>`, needs a second channel back to the server and gains nothing. The design doc settles the details.
6. **MCP tools that have no backend yet.** Dev servers are Phase 5, `propose_publish` is Phase 6, `propose_subtasks` is Phase 7. *Recommend:* register `list_dev_servers`/`dev_server_log` now against a `DevServers` interface that reports none running; register `propose_publish` and `propose_subtasks` **not at all** until their executors exist (a confirmable proposal that cannot run is worse than a missing tool). Record both as deviations from PLAN.md. `propose_install` runs through the builds service and is in scope.
7. **Whole-project mode (AG-09) before plan mode (AG-17).** PLAN.md puts AG-17 in Phase 7, and D10 says plan mode is on by default for whole-project tasks. *Recommend:* ship AG-09 in Phase 3 behind the same explicit opt-in warning as current-branch mode, Claude Code only (hook enforcement), and say so in the changelog. Alternative: move whole-project mode to Phase 7.
8. **Current-branch mode (AG-08)** lets the agent edit the main checkout, which sits next to invariant 2 (every mutation of a main checkout is a proposal). PRD AG-08 allows it as an opt-in with a warning, so the guard treats the main checkout as the task's writable root, only one editor per main checkout (AG-11), and no build/pull/checkout on it while a task edits it. I'll state this in the PR description and the threat model. Say if you want it deferred instead.

## Functional-effect designs (written and approved before the code that needs them)

Run `/functional-effect:design <feature>` for each; each is shown to you for approval. Linked from here once written.

| Design | Covers | Why |
|---|---|---|
| `docs/design/guard.md` | AG-06–AG-09, AG-10 rules, SF-06 path guard and command allowlist, SF-07 integrity decision, SF-09 trust decision | decisions with many inputs; safety critical; the hook process uses the same pure code |
| `docs/design/tasks.md` | AG-07, AG-10, AG-11, AG-22, AG-15, WE-04: task lifecycle with turns, `maxAgents` admission, one editor per main checkout, sub-agent caps, worktree lifecycle (create, name, active), restart recovery, usage totals | states, queue, retries, process lifecycle |
| `docs/design/proposals.md` | RV-02, GH-03, MCP-05: lifecycle, confirm/reject, execution of commit/push/MR/agent_file/delete_files/install, audit, crash mid-execution | external effects that are not idempotent (push, MR); decide outbox vs reconcile here |
| `docs/design/mcp.md` | MCP-01–MCP-06: per-task identity, tool → service mapping, visibility, proposal creation | trust boundary between agent and server |

No design needed (state it in the brief):
- OpenCode adapter: implements the existing `Adapter`; its parser gets the boundary test set.
- Draft buttons (AG-20): one-shot Ask-mode call, no state of its own.
- Composer backend (saved prompts, `@` search, model lists): reads and one settings table.
- Frontend tasks.

## Foundations (done first, committed to `main` before any fan-out)

One `go-backend` agent, serial, after the four designs are approved. Everything others import:

- `internal/core/guard`, `internal/core/tasks`, `internal/core/proposals`: state, input, command types and `Step` signatures from the designs; `Validate`; no logic yet beyond that.
- `internal/agents`:
  - `StartOptions` gets a `Policy` (path roots, allowlist, mode) and a `ConfigPaths()` method on the adapter (agent config files for the trust scan); `Capabilities.Enforcement` already exists.
  - A `RepoConfig` description so trust scans don't depend on one adapter's file names.
- Migration `00004`: `task_turns`; `repos.trust_hash`; worktree columns the design needs (short name, base commit); `proposals` columns the design needs; `settings` keys for `maxAgents`, saved prompts, command allowlist. Go migration only if SQL can't do it.
- `internal/api`: stub handlers and DTO files per area (`tasks.go` stays; new `worktrees.go`, `proposals.go`, `trust.go`, `board.go`, `composer.go`, `mcp.go`), topics (`proposals`, `board`), service interfaces and `Clock`/`IDs` in `Deps`.
- `task generate` so the frontend gets every DTO at once.

Gate: `task generate`, `lint`, `typecheck`, `test`, `test:web` green on `main`.

## Batches

At most 3 implementation agents at once. A review gate (`safety-reviewer` + `functional-effect:fe-reviewer` on the batch diff) follows every batch that merges; Blocking and Must-fix are fixed and re-reviewed before the next batch starts. Every Go brief starts with `git merge --ff-only main` (worktrees branch from the default branch) and says "follow the functional-effect:model skill (docs/AGENTS.md 5.11)".

### Batch 1: pure cores and path resolution

| ID | Task | Agent | Owns | PRD |
|---|---|---|---|---|
| G1 | `core/guard`: mode rules, path decision, command allowlist decision, Ask integrity decision, trust decision, `Step`; property tests | go-backend | `internal/core/guard/**` | AG-06–AG-09, SF-06, SF-07, SF-09 |
| K1 | `core/tasks`: turn lifecycle, queue and `maxAgents` admission, one editor per main checkout, sub-agent caps, `Recovered{Now}`, usage totals, machine-room hint; property tests | go-backend | `internal/core/tasks/**` | AG-10, AG-11, AG-15, AG-22, D7 |
| P1 | Path resolution for the guard: resolve a path to its long, symlink/junction-free form; refuse ADS (`file:stream`), device names, UNC/WSL, `\\?\` tricks; move claudecode's `canonical`/`within` logic into `internal/platform`; fuzz "accepted ⇒ inside root" | platform-windows | `internal/platform/**` (new `resolve_root*.go` + tests) | SF-06, invariants 5, 6, PL-03, PL-04 |

Must not touch (all three): each other's packages, `internal/api`, `internal/agents`.

Invariants to cover with property tests (the designs list them in full):
- G1: a write outside the roots is never allowed in any mode; Ask never allows a write or a command; an unlisted command is never allowed; an untrusted repo only gets Ask; `..`/junction/short-name forms never widen a root; the same decision for the same input.
- K1: running tasks ≤ `maxAgents`; at most one editing task per main checkout; a terminal task never restarts; a stale attempt changes nothing; sub-agent depth/concurrency never exceed the caps; usage only grows.

### Batch 2: proposals core, transcript UI, adapter contract suite

| ID | Task | Agent | Owns | PRD |
|---|---|---|---|---|
| X1 | `core/proposals`: lifecycle `Step` (pending → confirmed/rejected → executed/failed), execution commands, change-set linkage; property tests | go-backend | `internal/core/proposals/**` | RV-02 |
| FE1 | Transcript: inline tool calls, file edits as diffs, turn durations, nested sub-agent tree with per-sub-agent usage, resume button state from capabilities | frontend | `web/src/features/transcript/**`, `web/src/routes/tasks/**` | AG-14, AG-21, AG-02 UI |
| A1 | `agents.Adapter` contract suite (`agentstest`), run against `mock` now; later against `claudecode` and `opencode` fixtures. Clauses: event order, `ParentID` links, usage totals, cancel kills the tree, no raw stream line leaks, Check messages are plain language | test-engineer | `internal/agents/agentstest/**`, `internal/agents/mock/*_test.go` | AG-01, AG-02, AG-21 |

### Gate 1 (G1, K1, P1, X1, FE1, A1)

Reviewers on `<phase-start>..main`. Then a pause to confirm the OpenCode spike (decision 2) has happened, because batch 3's C1 and batch 4's O1 depend on its result.

### Batch 3: safety shell, worktrees, Claude Code Code mode

| ID | Task | Agent | Owns | PRD |
|---|---|---|---|---|
| S2 | Safety shell: path guard using P1, command allowlist parse/storage, Ask-mode integrity check (`git status`+HEAD+diff hash before/after each Ask turn via `internal/git`, no auto-revert), trust scan (detect adapter config files, hash them, produce what-would-run summary) | go-backend | `internal/safety/**` (not redact/env), `internal/git/snapshot*.go` | SF-06, SF-07, SF-09, invariants 5, 9, 10 |
| W1 | Worktree shell `internal/worktrees`: create with auto-generated short names under the data dir, `core.longpaths`, long-form cwd, record in `worktrees`, remove-nothing (removal is Phase 4, RV-06) | go-backend | `internal/worktrees/**`, `internal/git/worktree*.go` | AG-07, WE-04 |
| C1 | Claude Code Code mode and project mode: policy delivered to the hook as a short id plus a file (no paths in args, per B4), Edit/Write/Bash decisions through `core/guard`, allowlisted commands only, `isolation: worktree` still blocked, trust gating replaces the blanket `ownAgentConfig` refusal, mode per turn, resume | go-backend | `internal/agents/claudecode/**` | AG-07, AG-09, AG-10, AG-22, SF-06, SF-09 |

S2 and W1 touch `internal/git` in different new files; C1 owns only its adapter package. If any needs an edit in a file another task owns, it stops and reports.

### Gate 2 (S2, W1, C1)

### Batch 4: tasks service, OpenCode, board

| ID | Task | Agent | Owns | PRD |
|---|---|---|---|---|
| S1 | Tasks service `internal/tasks`: runs `core/tasks.Step`, interprets commands (create worktree, start run via `agents.Execute`, kill, finalize), persists state + audit in one `Commit`, startup recovery, queue wake, usage totals, turns/mode switch, board data; `api/tasks.go` becomes a thin handler; `cmd/ygg` wiring | go-backend | `internal/tasks/**`, `internal/api/{tasks,board,worktrees}.go`, `cmd/ygg/tasks*.go` | AG-07–AG-11, AG-12, AG-15, AG-22 |
| O1 | OpenCode reference adapter per the spike result, fixtures, parser boundary tests, contract suite call, `Check` (installed, Ollama reachable), enforcement per spike | go-backend | `internal/agents/opencode/**` | AG-03, AG-05, D6 |
| FE2 | Agent board (Working / Needs you / Review / Done), task cards, widgets: Working now (update), Recent chats, Agent usage | frontend | `web/src/features/board/**`, `web/src/routes/board*`, `web/src/widgets/{working-now,recent-chats,agent-usage}/**` | AG-12, AG-15, UI-03 |

S1 starts after Gate 2 (needs S2, W1, C1 merged). O1 starts after Gate 1 and the spike (so it can overlap with batch 3 if a slot is free; I'll keep the cap at 3).

### Gate 3 (S1, O1, FE2)

### Batch 5: proposals shell, composer backend, composer UI

| ID | Task | Agent | Owns | PRD |
|---|---|---|---|---|
| PR1 | Proposals shell `internal/proposals`: load, `Step`, persist + audit in one `Commit`, execute commit/push/agent_file/delete_files (moves into `_to_delete/`, never deletes)/install via `internal/git`, builds service; **no force in any path**; GH-03 MR creation via GitLab push options as a proposal; refuses untracked repos (carry-in from Phase 2) | go-backend | `internal/proposals/**`, `internal/git/{commit,push}*.go`, `internal/api/proposals.go` | RV-02, GH-03, invariants 1, 2, 4, 8 |
| D1 | Composer backend: saved prompts, `@` file/folder fuzzy search inside tracked repos (reuses `git ls-files`, no path outside repos), model lists per adapter, draft endpoints (commit message, MR title/description from a diff via a one-shot Ask run on the mock-testable adapter path) | go-backend | `internal/composer/**`, `internal/api/composer.go` | AG-13, AG-20 |
| FE3 | Composer: scope, agent, model, saved prompts, `@` mentions, mode switch per message, current-branch/whole-project warnings, draft buttons | frontend | `web/src/components/Composer*`, `web/src/features/composer/**` | AG-10, AG-13, AG-20 |

### Gate 4 (PR1, D1, FE3)

### Batch 6: MCP, proposals/trust UI, safety tests

| ID | Task | Agent | Owns | PRD |
|---|---|---|---|---|
| M1 | MCP server per `docs/design/mcp.md`: read, build, dev-server (stub), safe-git, proposal tools; `propose_agent_file`; per-task token; tracked-repos-only visibility; adapters get the MCP config | go-backend | `internal/mcp/**`, `internal/api/mcp.go` | MCP-01–MCP-06, SF-02 |
| FE4 | Proposal confirmation UI (what will run, diff, confirm/reject, change-set grouping), trust dialog with the what-would-run list, Ask-integrity failure view, settings: `maxAgents` + room hint, usage display | frontend | `web/src/features/proposals/**`, `web/src/features/trust/**`, `web/src/routes/settings*` | RV-02, SF-07, SF-09, AG-11, AG-15 |
| T1 | Safety invariant tests (matrix below) with a mutation check per test; test maps from the four designs (property, parser round-trip/fuzz, contract suites); Code-mode end-to-end on `fixtures/demo-project` with the mock adapter | test-engineer | test files only, plus `fixtures/demo-gen` additions | all |

### Gate 5 and phase close

- Final review of `<phase-start>..main` by both reviewers; `/functional-effect:review` over `internal/core/{guard,tasks,proposals}` and the new shells.
- Lead writes `docs/threat-model.md` (SF-08), including the Phase 2 carry-ins: per-line log redaction can miss a token split by the 64 KiB cut, MCP token handling, hook command line visible to the model, account data in the stream, current-branch mode.
- Bookkeeping: tick PLAN, CHANGELOG, record deviations here.

## Safety invariant test matrix (T1; docs/AGENTS.md section 6)

| # | Invariant | Test shape |
|---|---|---|
| 1 | No force push, `reset --hard`, branch delete, file delete | archtest/AST scan of `internal/git` and `internal/proposals` for the forbidden argv; executor test: `delete_files` moves to `_to_delete/` and the original is gone only by move |
| 2 | Every main-checkout/remote mutation is a proposal | no executor reachable without a `confirmed` proposal; the API and MCP have no direct commit/push path |
| 3 | Tokens never reach child processes | extend the existing child-env test to agent runs and executors |
| 4 | Tokens not in URLs, config, logs, audit | seeded sentinel token through events, audit, MCP, push options; assert absent |
| 5 | Enforcement in Yggdrasil | hook/guard denies write outside root, `..`, junction, 8.3, ADS, absolute glob; unlisted command; with permission flags deliberately permissive |
| 6 | Only tracked repos visible | MCP and `@` search over an untracked sibling folder return nothing |
| 7 | Bind `127.0.0.1`, Host/Origin | existing tests, re-asserted for `/mcp` |
| 8 | State changes audited | every new state change in tasks and proposals commits an audit entry in the same transaction |
| 9 | Ask never changes a tree; integrity check present | agent edits during an Ask turn → task stops and is flagged, nothing reverted |
| 10 | No agent outside Ask in an untrusted repo | untrusted repo + Code mode refused; changing a config file resets trust |
| 11 | Sub-agents/sub-tasks never widen access | sub-agent cap and `isolation` denial; a sub-agent call outside the root denied by the same guard |

Each test is shown to fail by removing the guard it protects.

## Exit criteria → tasks

| Exit criterion | Contributing tasks | How verified |
|---|---|---|
| Code-mode task runs in a worktree | K1, W1, C1, O1, S1 | T1 e2e with mock; manual with both real agents |
| Either reference adapter | C1, O1, A1 (contract suite on both) | contract suite in CI; manual |
| Cannot write outside the worktree | G1, P1, S2, C1, O1 | invariant 5 tests; manual attempt |
| Cannot run unlisted commands | G1, S2, C1, O1 | invariant 5 tests; manual attempt |
| Produces confirmable proposals | X1, PR1, M1, FE4 | proposal lifecycle tests; manual confirm |
| Can replace the personal tool for agent work | S1, FE1–FE4, D1 | your manual daily-use check |
| Every PLAN checkbox | see the PRD column above | I tick them at close |

## What only you can verify on Windows (manual checklist, run at phase close)

Real agent CLIs run here only with your agreement in that session.

1. Claude Code (Haiku) Code-mode task on `fixtures/demo-project`: edits land in the worktree under the data dir, `git status` of the main checkout stays clean.
2. Same task asked to write `..\outside.txt`, to use a junction or an 8.3 short path, and to run `curl`/`rm`: all refused with the plain-language reason, audit entries present.
3. OpenCode with an Ollama model: same two checks (this is where its `Enforcement` level shows).
4. Ask turn that tries to edit: task stops, flagged, changed files listed, nothing reverted.
5. Repo with `.claude/settings.json` hooks: trust dialog lists what would run; trusting once allows Code mode; editing the file asks again.
6. Proposals: confirm a commit, a push (to a local bare repo) and a GitLab push-option MR; reject one; the audit log shows each.
7. `maxAgents` 2 with 3 tasks: third queues; two tasks cannot edit the same main checkout (current-branch mode).
8. Kill `ygg` from Task Manager mid-run: no `claude`/`opencode`/`git` process left; on restart the task is marked interrupted, not running.
9. Worktree path length and spaces in the project path; antivirus on.
10. MCP: an agent calls `list_repos` and `repo_status`; an untracked folder never appears.
11. Board, composer `@` mentions, mode switch per message, usage widget, draft buttons look and work as in the personal tool.

## Risks

- **Size.** About 19 tasks; the tasks service (S1) and the Claude Code hook (C1) are the largest. If a usage limit hits mid-batch I write each in-flight task's state here so work can resume.
- **OpenCode may have no pre-tool hook.** Then `limited` enforcement and a smaller feature set for it; the exit criterion still holds.
- **Hook correctness is the safety story.** C1 and S2 get the longest review; gate 2 runs `safety-reviewer` with the invariant 5 cases written out.
- **vitest worker flake** under parallel agents (known; `maxWorkers: 2` set). Reruns pass.

## Progress

Approved 2026-10-25. (1) Phase 2 checklist done, v0.3.0 tagged by the human. (2) OpenCode: no real-CLI spike now; O1 waits and its enforcement level stays open; batch 4 starts with S1 and FE2 only. (3-8) Recommendations accepted as written. Next: designs guard, tasks, proposals, mcp, one at a time.

### Designs (all approved 2026-10-25)

- `docs/design/guard.md`, `docs/design/tasks.md`, `docs/design/proposals.md`, `docs/design/mcp.md`.
- Decision recorded: MCP `pull` stays a direct tool (ff-only, clean tree, no build/editor on the checkout, audited). Read as the one explicit exception to invariant 2; goes into the threat model.
- Next: foundations (one go-backend agent), then batch 1.

### Progress log

- **Foundations** merged (`89f763a`, `6f9a669`): contracts for guard, tasks, proposals, mcp; adapter `Policy`/`RepoConfigProvider`/`MCPConfigurer`; migration 00004; API stubs (501) and DTOs; `proposals`/`board` topics. archtest now allows `crypto/sha256` and `encoding/hex` in core. Core domains cannot import each other, so `tasks.Message` carries a shell-filled `Admission` (see tasks.md Deviation).
- **Batch 1** merged: G1 `core/guard` (`fbaf9ec`), K1 `core/tasks`, P1 `platform.ResolveRoot` + `PathResolver` contract. Notes for later tasks:
  - S1: `StateFromRows` rejects Phase 2 `queued`/`running` rows with no turn row; startup must mark them interrupted before loading. `Task.Caps` is not stored; set it on load. `Schedule` admits only on `Wake`: send one after every enqueue and release. Extra `FinishTurn` on cancel-while-waiting and `PrepareFailed`; `TaskRow.IdleAt` goes to `tasks.finished_at`. Cancel of a `Starting` task relies on the shell feeding `RunFinished{Cancelled}` after `KillRun`.
  - S2/C1: `guard.Trusted` carries a `Scan` slice (no `==`). The shell sets `Call.Globs` and the cwd so relative globs resolve inside a root. `ResolveRoot` accepts `\host\share`; the guard compares roots.
  - Manual (P1): 8.3 short-name subtest with a short `TEMP`; junction into `\wsl$` returns `ErrWSLPath`; symlink tests with Developer Mode.
- **A1** (`agentstest.AdapterContract`) and **FE1** (`web/src/features/transcript`, moved from `components/transcript`) merged. Carry-ins: S1 must expose the adapter's `Capabilities` on the task response (or `GET /api/agents`) so the transcript's greying goes live, and a resume endpoint for the `ResumeButton`. The transcript ignores unknown event kinds silently. claudecode needs a missing-CLI scenario for the `check` clause.
- **Review gate 1 failed** (safety: 3 Blocking, 5 Must-fix; fe: 2 Must-fix). Fix batch FG (guard/proposals), FT (tasks/mcp), FP (platform). FP and FT merged. Shell carry-ins from FT: set `Turn` on `UsageReported`/`SubagentStarted`/`SubagentStopped`; persist `TaskRow.CurrentTurn` for idle tasks; load the previous turn's `Caps` for idle tasks (zero caps are rejected; send >= 1); the turn row stays `answered` after a late integrity verdict (re-record via the audit command if needed); hold `RunFinished` until the After snapshot is checked. From FP: callers must handle `ErrPathDotDot` (Unix); use `HasShortNameSegment`; set `YGG_REQUIRE_PATH_FIXTURES=1` in Windows CI.
- Deferred to batch 3 (S2/C1): snapshot must hash untracked content (SnapshotContract); claudecode must run `AdapterContract` incl. a missing-CLI scenario; shell redacts `Failed.Message`/`ExecFailed.Message` before persisting; resolve argv[0] via platform without searching cwd and pass it as `Call.Program`.

### PAUSED 2026-10-26 (human asked to pause)

State of `main`: `6d37098` + CI env var commit; all checks green. Round-2 review of gate 1: all round-1 Blocking and Must-fix verified closed; new Must-fix M-A (UserTrusts over an incomplete scan), M-B (late IntegrityViolated dropped for Cancelled/Failed/Dismissed idle), M-C (`,` and `@` in the command allowlist; PowerShell) plus Should-fix list (see round-2 reports in this log's earlier entries).

Round-3 fix agents were stopped mid-work; their work is NOT merged. Worktrees (under `.claude/worktrees/`, branches `worktree-agent-<id>`):
- RG guard (`a10b35f3932b26575`): one commit `dded815` (trust re-arm only after a complete scan, no `,`/`@`, integrity supersede). Missing: items 4-6 of its brief (TurnEnds in Arming, abort FlagTask, shell carry-ins in guard.md, `ConfigNameProvider` + `.claude/rules`/`output-styles` scan paths), and its last test/comment fixes.
- RT tasks/proposals (`ae3b13ad77205ed4a`): uncommitted edits in `internal/core/tasks` (stepIntegrity replacement had not applied). Items 1-6 not done or unverified.
- RP platform (`a47d93144d3080a2c`): uncommitted edits in `internal/platform` (CONIN$ stem logic; tests were passing).

To resume: re-spawn or SendMessage-resume each agent with its brief (the briefs are in the conversation; the items are: RG 1-6, RT 1-6, RP device names), merge one at a time, run `task generate/lint/typecheck/test/test:web`, then a short round-3 verification by both reviewers; then batch 3 (S2 safety shell, W1 worktrees, C1 Claude Code Code mode).
- **Review gate 1 closed (2026-10-26).** Round 3: safety "gate can close", fe "gate can close". Round-3 fixes merged (RG, RT, RP) plus the tail-word `=` rule. Constraints carried into batch 3 (also in tasks.md/guard.md): S1 keeps the live Task per task or feeds `RunFinished{Cancelled}` after a never-started kill; S2 trust scanner must walk `ConfigNames` at any depth and `ClearTrust` on `ScanFailed`; widen `TestPropViolatedTurnNeverAnswered` to every non-integrity outcome when touched.
- **S2 merged** (`internal/safety`: scan, snapshot, allowlist, policy file, ResolveProgram; `internal/git/snapshot.go`). Notes for S1/C1: snapshot folds untracked content into `Snapshot.Status`; a repo with no commits is an error; a scan fails closed on a linked config file/folder, >1 MiB, >5000 files; real-git snapshot suite takes 40-65 s on Windows (parallel subtests). Still owed by the tasks service: ClearTrust on ScanFailed, HasShortNameSegment on proposal paths, default allowlist seeding from toolchains, needs_you mapping, lost-snapshot timeout, hold RunFinished, per-repo Request.Trust.

### PAUSED 2026-10-26 (usage limit reached)

Batch 3: S2 merged. W1 (worktrees, agent `aadc2d220142d4526`) and C1 (Claude Code Code mode, agent `a76bfc7d40a4badd6`) were stopped mid-work; their branches are `worktree-agent-<id>` under `.claude/worktrees/`, not merged, state unknown (check `git log main..<branch>` and `git status` in each worktree). The full check run after the S2 merge and the archtest rule change (`internal/safety` may import `core` and `platform`) was still running when the limit hit: re-run `task generate/lint/typecheck/test/test:web` on `main` first. Then resume W1 and C1 with their briefs, merge, run review gate 2, then batch 4.
- archtest rule changed (lead decision, 2026-10-26): `internal/safety` may import `core` and `platform` (was: nothing). Reason: the layout puts the path guard, allowlist, Ask integrity check and trust scan in `internal/safety`, which need `core/guard` and `platform.ResolveRoot`. `platform` still may not import `safety`, so there is no cycle.
- **W1 merged** (`internal/worktrees`, `db` worktree queries + `Tx.RecordWorktree`, `git.ResolveCommit`). Notes: git fails with "'$GIT_DIR' too big" for worktree paths of ~220+ chars even with longpaths: keep the data dir short (tests use ~150-170); a failed `git worktree add` may leave a folder, so collisions are checked before the add (folder and branch); if Record fails after the add the worktree exists on disk without a row (error says so, nothing deleted); `core.Worktree` has no BaseCommit field (stored via RecordWorktree, read via `GetWorktreeBaseCommit`).
- **C1 merged** (`3f8024e`). **Review gate 2 round 1: FAIL** (safety: 2 Blocking B1 repo `disableAllHooks`, B2 hook fails open on CLI < 2.1.295; Must-fix M1 policy wider than folders, M2 allowlist accepts shells/git, M3 deny records lost with run folder, M4 MCP token file location, M5 Ask refusal drift; fe: Must-fix scan fail-open for a linked/junctioned `.claude`). **B5 spike run** (docs/spikes.md): `--setting-sources ""`, `disableAllHooks:false` and `--strict-mcp-config` make repo config inert; decision: adapter no longer refuses untrusted repos, Admit keeps the SF-09 policy. Fix batch G2: FA (claudecode adapter), FS (safety, worktrees, git snapshot).
- Known deviations/records from C1: `ConfigTrusted` is one bool (to be replaced by the per-repo hash check by the tasks service); `ownAgentConfig` is deliberately a subset of `ConfigPaths`; MCP tools are `ToolOther` and denied until M1; `agentstest` event_order counts file edit/command/sub-agent start as calls.
- Extra S1 carry-ins from gate 2: serialize `Ensure` per task (no unique (task, repo) index; concurrent Ensure creates duplicates), no automatic retry of Ensure/Record (list orphans), production goroutine-safe `Rand` for worktree names, `narrowCaps` previous-caps load, redact `Failed.Message`/`ExecFailed.Message`, threat model must state: allowlist limits what the agent starts not what a repo's own scripts do (an allowlisted `npm run test` after a `package.json` edit can run arbitrary code and rewrite policy/hook files in the run folder), hook resolves programs with the hook's PATH (git-bash/PowerShell can differ), snapshot covers tree and HEAD only (no refs, no ignored files).
