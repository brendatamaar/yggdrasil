# Phase 5 — Dev servers, terminal, worktree environment and more toolchains → `v0.6.0`

> Status: **plan approved, decisions final (2026-11-08). Designs approved with all recommendations. Foundations F1–F4 on `main`. Batch 1 (T1, T2, T3) merged; gate 1 passed (no Blocking/Must-fix); waiting for the human.**
> Source: docs/PLAN.md Phase 5; docs/PRD.md BL-12, BL-13, BL-14, WE-01, WE-02, WE-03, WE-05, DV-01–DV-05, UI-03, PL-02, PL-05 (D9, D12 reused); docs/AGENTS.md 5.2, 5.3, 5.7, 5.10, 5.11, 6 (invariants 2, 3, 4, 5, 6, 7, 8, 10); docs/spikes.md B1 (ConPTY gotchas), B2 (job objects); docs/phases/phase-4.md (carry-ins).

**Exit criterion (PLAN.md):** two worktrees of the same frontend+backend pair run side by side with no port conflicts; killing Yggdrasil leaves no processes behind; a Python (uv) repo and a .NET repo in the demo project pass verification in fresh worktrees.

---

## Decisions (final, from the human)

1. **Docker compose (WE-03):** implement, opt-in per repo. Only `docker compose -p ygg-<worktree>` `up -d` / `stop` / `down`. Never `down -v`. Runs through `platform.Start` with the clean env. Built last (task E2) so it can be dropped. Real docker is manual verification only; CI uses a fake runner.
2. **Ports (WE-02, DV-01):** block of 20 consecutive ports per worktree from a configurable range (default 20000–29999), stored in SQLite. Dev servers get `PORT`, `YGG_PORT_BASE`, `YGG_PORT_0..n` in env. Argv `{port}`/`{port:1}` placeholders only for frameworks with no env var (Vite `--port`, `ASPNETCORE_URLS`, uvicorn `--port`). DV-02 detection reports actual ports and warns when one is outside the block.
3. **Bootstrap profile (WE-01):** explicit per-repo list of relative paths; no globs outside the repo, no `.git`, no symlinks followed, never overwrite; copied only for worktrees the user starts or confirms; **off by default for agent tasks**, with a per-repo "also copy for agent tasks" toggle and a visible warning.
4. **Terminal (DV-04):** tracked repo/worktree folders only (DV-05), not reachable from MCP or agents, open/close audited (not keystrokes), single-use in-memory token with 30 s TTL. Default shell PowerShell on Windows, `$SHELL` elsewhere, clean env. Do not call `SetConsoleCtrlHandler` in `ygg` itself; clear the ignore flag in the child only. **If that fails with ConPTY, stop and ask the human.**
5. **Python bootstrap:** uv → `uv sync` (`--frozen` when `uv.lock` exists); Poetry → `poetry install` with `POETRY_VIRTUALENVS_IN_PROJECT=true`; pip → `python -m venv .venv` then the `.venv` python `-m pip install -r requirements.txt`. A missing uv/poetry/dotnet is a plain-language verification failure, not a crash.
6. **MCP-03** is out of scope (Phase 7).
7. **Config:** `internal/verify`, `internal/devservers`, `internal/terminal`, `internal/worktreeenv` added to the shell globs in `.functional-effect.json` (the one approved config edit).
8. **Stale worktrees (~148):** do NOT prune or delete anything. If a `go-backend` spawn fails because of them, use general-purpose/sonnet agents in hand-made worktrees (as in Phase 4) and tell the human.
9. **Process:** never push, tag, delete files or branches. Stop at each gate and at phase close. Every subagent brief begins with `git merge --ff-only main`. `v0.5.0` (Phase 4) is not tagged; do not tag it.

---

## Starting state

- `main` clean. Tags: `v0.1.0`, `v0.2.0`, `v0.4.0`. **`v0.5.0` (Phase 4) is not tagged yet**; the lead doesn't tag. Phase 5 branches from `main` regardless.
- Already on `main`, reused as is:
  - `internal/platform`: `Start` (job objects, nested per-task job, `KillTask`), `PTY`/`StartPTY` (own ConPTY wrapper, tested), `ListeningPorts(pid)` (Windows TCP table, process tree; Unix version exists), `Resolve` (wrappers), `BatchArgsOK`, `OpenFolder/Editor` outside the job, clean env in `internal/safety` (already drops `VIRTUAL_ENV`, `CONDA_PREFIX`, `PYTHONPATH`).
  - `internal/builds`: `Toolchain` interface with `Commands{Install, Bootstrap, Verify, FullTests, Build, DevServer}`; modules node, maven, gradle, custom (node/maven/gradle already fill `Bootstrap` and `DevServer`); `Step.Env`; per-repo `RepoBuildSettings` (override, full tests, bootstrap on/off).
  - `internal/verify` + `core/verify`: bootstrap-then-verify with `DepsHash`; **bootstrap for a new toolchain only needs `Commands.Bootstrap` filled** (WE-05 plumbing exists).
  - `internal/worktrees` (`Ensurer`, create only), `internal/api` (WS hub with closed topic set, `parseTopic`), widget registry (`web/src/widgets`, 10 widgets), `@xterm/xterm` + `addon-fit` already in `web/package.json`.
  - `fixtures/demo-gen`: already has a Go repo (`shop-payments`) and a Python uv repo (`shop-inventory`: `pyproject.toml` + `uv.lock`).
- **Gaps found while reading** (these shape the plan):
  1. `internal/devservers` is an empty `doc.go`. No dev-server service, store, logs, API or UI. No terminal service, no socket token, no terminal route.
  2. `toolchains.All()` lists only node, maven, gradle. No Go, .NET or Python module.
  3. `platform.Resolve` has no venv resolution (`.venv\Scripts\python.exe`), and nothing creates a venv.
  4. Dev-server `Step`s carry no port information. Frameworks differ on how a port is set (`PORT` env, `--port`, `ASPNETCORE_URLS`, `--server.port`).
  5. No store for port blocks, bootstrap profiles or compose opt-in; `Ensurer` has no hook for copying files into a new worktree.
  6. Demo project has **no .NET repo**, and no small servers that honour a port variable, so the side-by-side exit test has nothing to run.
  7. Migrations on disk: `00001`, `00002`, `00004` (no `00003`). Phase 4 added a Go migration `00005` (verify/review/landing), so the Phase 5 migration is `00006_devservers_env.sql`.
  8. DV-03 orphan listing (non-Windows) has no platform support. Windows relies on the job object; a "kill ygg leaves nothing" test exists only for builds, not for long-lived servers.
  9. `.functional-effect.json` shell globs did not list the four new packages (decision 7 fixes this).
- **Environment risk carried from Phase 4:** `go-backend` subagent spawns failed with ~135 stale agent worktrees; `git worktree list` now shows 148. See decision 8.

## Contracts decided now (committed in foundations)

- **Argv convention for the project's Python.** Toolchain `Step.Argv[0]` is `.venv/python` (repo-relative, forward slashes), exactly as `mvnw` is named today. `platform.Resolve` maps it to `.venv\Scripts\python.exe` / `.venv/bin/python` (no `runtime.GOOS` outside platform). `uv run`/`poetry run` are plain `uv`/`poetry` names.
- **Dev-server step.** No new `DevServerStep` type; `Step.Argv` placeholders plus `Step.Env` carry everything, and the shell (devservers) substitutes `{port}`. Keeps `Toolchain` unchanged. (Foundation F2 may adjust; any change is a contract commit by the lead.)
- **Closed topic set** gets `devserversTopic`, `devserverTopic{ID}` (state + log lines) and `worktreeEnvTopic`. Terminal output is **not** a topic; it is its own token-gated socket.

---

## Foundations (lead, committed to `main` before any fan-out)

Each is a pure-core design first (`/functional-effect:design`), approved by the human, then types only (no shell code).

| ID | What | Output |
|---|---|---|
| F1 | Design docs (service profile): devservers (lifecycle Starting → Running{ports} → Stopping → Stopped / Failed / Orphaned; port detection as input events; no automatic restart; cleanup at shutdown), terminal (token: Issued → Redeemed / Expired; session: Opening → Open → Closed), worktree-env (port-block allocation, bootstrap profile copy plan, compose lifecycle). Each lists failure classes, boundary parsers and capabilities with fakes. | 3 design docs, linked below |
| F2 | Core types and `step` signatures: `internal/core/devservers`, `internal/core/terminal`, `internal/core/worktreeenv` (state, input, command, `step`; stub returning state unchanged), plus ID types in `internal/core/ids.go`. | pure packages, archtest-clean |
| F3 | Migration `00006_devservers_env.sql` (`dev_servers`, `worktree_ports`, `repo_env_settings`: profile paths, agent-copy toggle, compose opt-in; terminal sessions audit rows only), sqlc queries, `task generate`. | migration + generated code |
| F4 | API contracts: DTOs in `internal/api/types_devservers.go`, `types_terminal.go`, `types_worktreeenv.go`, closed topics above (`topic.go`), tygo output; `platform` interface additions (signatures + stubs in `platformtest`): `Resolve` venv rule, `PythonBase()`, `ListOrphans`/`StopOrphan`. | types + generated TS |

Design docs:
- [docs/design/devservers.md](../design/devservers.md)
- [docs/design/terminal.md](../design/terminal.md)
- [docs/design/worktree-env.md](../design/worktree-env.md)

Go/.NET/Python modules and the Windows platform pieces have no states or retries beyond what `verify` already models, so they need **no design doc**, only the spec in this file.

---

## Batches

At most 3 implementation agents at once; no two parallel tasks own the same package or file. After each batch: merge one branch at a time, run `task generate`, `task lint`, `task typecheck`, `task test`, `task test:web`, then `safety-reviewer` and `functional-effect:fe-reviewer` on `git diff <batch-start>..main`; fix Blocking/Must-fix; short update to the human.

### Batch 1 — toolchains, platform, one extraction (parallel, disjoint)

| Task | Agent | PRD | Owns | Uses | Definition of done |
|---|---|---|---|---|---|
| **T1** Go, .NET, Python modules | go-backend | BL-12, BL-13, BL-14, WE-05, D9, DV-01 (commands) | `internal/builds/toolchains/golang/`, `dotnet/`, `python/`, `toolchains/all.go`, their `testdata` fixture repos | `builds.Toolchain`, `Commands`, `Step`; `.venv/python` Argv convention | Detect reads files only (go.mod; *`.sln`/*`.csproj`/`global.json`; `uv.lock`/`[tool.uv]`, `poetry.lock`/`[tool.poetry]`, `requirements.txt`); verify chains exactly per PRD 6.3 (Go `vet`→`build`, full `test`; .NET `build --no-restore`, full `test`; Python ruff → mypy/pyright → pytest only if configured, else `compileall -q`, Django `manage.py check`); `Bootstrap`: `go mod download`, `dotnet restore`, Python per decision 5; `DevServer` commands per DV-01; detection tests per fixture; modules don't import each other; `task test`, `task lint` pass |
| **T2** Platform: venv, orphans, terminal hardening | platform-windows | PL-02, DV-02, DV-03, DV-05, DV-04 (server side), PL-05 | `internal/platform/**` only | F4 platform signatures | `Resolve(".venv/python")` per OS; `PythonBase()` (py launcher/PATH, never `VIRTUAL_ENV`); Unix `ListOrphans/StopOrphan` (pid files under the data dir; Windows returns none, job object handles it); port detection tests with real listeners incl. grandchild listeners and IPv6; Ctrl+C behaviour per decision 4 with a ConPTY test; **test: a started dev-server tree dies when the host process is killed**; contract suites run against fake and real; `go vet` for darwin/linux |
| **T3** Extract `Ensurer` decisions | go-backend | WE-04, groundwork for WE-01/02 | `internal/worktrees/**`, `internal/core/worktrees/**` | `/functional-effect:extract`, existing `ensure_test.go` as characterization | Behaviour unchanged (name collision retry, base commit, audit in one tx) behind characterization tests first; name/branch choice becomes a pure step with a property test; shell keeps git/store; `task test` passes. Its own task, per CLAUDE.md. |

Dependencies: F1–F4 merged first. T1/T2/T3 are independent of each other.

### Batch 2 — services (parallel, disjoint; after batch 1 merged)

| Task | Agent | PRD | Owns | Uses | Definition of done |
|---|---|---|---|---|---|
| **D** Dev-server service | go-backend | DV-01, DV-02, DV-03, DV-05 | `internal/devservers/**` | `core/devservers`, `builds.Toolchain.Commands.DevServer`, `platform.Start/ListeningPorts/ListOrphans`, `safety` clean env, F3 queries, design `devservers.md` | Interprets core commands; one server per (repo/worktree, command); start only in tracked repo/worktree (invariant 6, DV-05); started via `platform.Start` with the task job; port detection poll fed back as input; logs to file + ring buffer + topic lines (redacted); stop on `Stop`, on context cancel and on app shutdown; reconcile at startup (stale rows → Stopped or Orphaned); every start/stop audited; property tests per design invariants, parsers round-trip/fuzz, store contract suite (fake + SQLite) |
| **E1** Worktree environment: ports and profiles | go-backend | WE-01, WE-02 | `internal/worktreeenv/**` (new), and the **hook call only** in `internal/worktrees/ensure.go` (T3 merged by then) | `core/worktreeenv`, `worktrees.Ensurer`, F3 queries | Port block allocator (no overlap across live worktrees, range exhaustion is a plain error, freed on worktree removal); profile copy with path guard (relative only, inside repo, no `.git`, no symlink/reparse follow, never overwrite, size cap, CRLF preserved); agent-task toggle per decision 3; audited; invariant-6 and path-traversal tests; allocator contract suite; no deletion anywhere |
| **P** Terminal service | go-backend | DV-04, DV-05 | `internal/terminal/**` (new) | `core/terminal`, `platform.StartPTY`, `safety` clean env, design `terminal.md` | Single-use token (random, 30 s TTL, constant-time compare, consumed on first use even on failure to bind); sessions limited; folder must be a tracked repo/worktree; PTY read loop owned by a context; close order per spikes B1; audit open/close; tests: token reuse, expiry, wrong folder, concurrent redeem; no real shell in CI (platformtest fake PTY); real ConPTY test is Windows-tagged |

`E2` (compose, WE-03) comes in batch 4. D, E1, P are independent (they meet in `internal/api` and `cmd/ygg`, which none of them touch).

### Batch 3 — API, UI, fixtures (parallel; after batch 2 merged)

| Task | Agent | PRD | Owns | Definition of done |
|---|---|---|---|---|
| **A** API handlers | go-backend | DV-01–DV-05, WE-01–WE-03, UI-03 | `internal/api/**` handlers and adapters for the new DTOs (DTO files are from F4: changes need the lead); topic relays | Endpoints: list/start/stop/restart dev server, server log tail, ports; repo env settings get/set (parsers reject bad input: absolute paths, `..`, `.git`, NUL, too many entries); terminal `POST /api/terminal` (issues token; Host/Origin checked) and WS `/ws/terminal?token=`; each mapped to apperr statuses; invariant 7 test (Host/Origin on the terminal socket); invariant 2: starting a server is not a main-checkout mutation, but any `install` stays a proposal |
| **UI** Frontend | frontend | UI-03, DV-01–DV-04, WE-01, WE-02, WE-03 | `web/**` | Widget "Running services" in `web/src/widgets/running-services/` (no cross-widget imports); dev-server panel on repo and task pages (start/stop, state, detected ports, live log); terminal dock (xterm, ``Ctrl+` ``, fit, reconnect needs a new token); repo env settings (profile paths, agent-copy toggle with warning, compose opt-in); keyboard-accessible (Radix); `task typecheck`, `task test:web` pass; uses generated types only |
| **FX** Demo fixtures and fixture servers | test-engineer | exit criterion, BL-13 | `fixtures/demo-gen/**` | New .NET repo (`shop-ledger`: `.sln` + `.csproj`, offline-safe tests), fictional frontend+backend servers that honour `PORT` (tiny Node/Python/Go servers, no deps), a second uv repo state for venv tests; generator tests; `task fixtures` clean; all names fictional (AGENTS section 10) |

Also in this batch window (lead, between merges): wiring in `cmd/ygg/app.go` for devservers, terminal, worktreeenv services (the Phase 4 WIRE1 pattern; stop before store close). Not delegated.

### Batch 4 — compose, tests, end to end (after batch 3 merged)

| Task | Agent | PRD | Owns | Definition of done |
|---|---|---|---|---|
| **E2** Compose per worktree (last, droppable) | go-backend | WE-03 | `internal/worktreeenv/compose*.go` (E1 merged) | Per decision 1; project name `ygg-<worktree>`; ports offset into the block; `up -d`/`stop`/`down`, never `-v`; runner behind an interface with a fake; missing docker = plain message; audited |
| **T-maps** Test maps and gaps | test-engineer | all above | `_test.go`, `testdata`, helpers in `internal/devservers`, `internal/terminal`, `internal/worktreeenv`, `internal/core/*` new packages | From each design doc: invariants → property tests, boundary parsers → round-trip / accepted ⇒ valid / rejects-each-rule / fuzz, fakes → contract suites; every test shown able to fail; safety tests for invariants 3, 4, 6, 7 on the new surface (token never in env, URL or log) |
| **E2E** `TestPhase5EndToEnd` and smoke | test-engineer (after T-maps) | exit criterion | Phase 4 E2E location, `scripts/smoke.ps1` | See "Exit criteria" below |

E2 and T-maps run together; E2E starts after both.

### Review gates and close

- Gate 1 after batch 1, gate 2 after batch 2, gate 3 after batch 3, final after batch 4: `safety-reviewer` + `functional-effect:fe-reviewer`; baseline `/functional-effect:review` over the new packages before close.
- Bookkeeping: tick PLAN.md, CHANGELOG, `docs/threat-model.md` (terminal token, profile copy, compose), `docs/writing-a-toolchain.md` (Go/.NET/Python examples and the `.venv/python` rule), decisions recorded here.
- Close: exit criteria with evidence, manual checklist, proposed tag `v0.6.0`. No tag, push, or Phase 6 start without the human.

---

## Exit criteria → tasks → evidence

| Exit criterion | Tasks | Evidence |
|---|---|---|
| Two worktrees of the same frontend+backend pair run side by side with no port conflicts | E1, D, A, UI, FX, E2E | `TestPhase5EndToEnd/side_by_side`: two worktrees of the fixture pair, both started, distinct detected ports inside their blocks, both respond, logs separate |
| Killing Yggdrasil leaves no processes behind | T2, D, E2E | platform test (host killed, tree gone) + `TestPhase5EndToEnd/no_leftovers` + `scripts/smoke.ps1` extended; Windows-only, auto in CI |
| A Python (uv) repo and a .NET repo pass verification in fresh worktrees | T1, FX, E2E | `TestPhase5EndToEnd/uv_and_dotnet_verify` (skips when `uv`/`dotnet` missing; the CI runner installs them) |

Other PLAN.md checkboxes: Python bootstrap (T1, T2), dev servers (D, A, UI), terminal dock (P, A, UI), bootstrap profiles (E1), port ranges (E1), compose (E2), Running services widget (UI).

## Manual verification on Windows (for the human, at close)

1. Open a task worktree, start the frontend and backend dev servers in two worktrees; ports differ, "Running services" shows both with live logs.
2. Close the browser tab, then stop `ygg` from Task Manager: no `node.exe`, `dotnet.exe`, `python.exe`, `uvicorn`, `conhost.exe`/`OpenConsole.exe` remain.
3. Terminal dock: ``Ctrl+` ``, run an interactive command, resize the window, Ctrl+C stops the command and not the shell, close the tab: no `powershell.exe` left. Reload the page: a new token is needed; reusing the old WebSocket URL is refused.
4. A fresh uv worktree and a fresh .NET worktree verify to green; a worktree with a profile copies `.env` and does not overwrite an existing file.
5. Locked-folder and antivirus behaviour: start a dev server, try to remove the worktree: Phase 4's lock message appears and the server stop frees it.
6. If compose is kept: with Docker Desktop, two worktrees each bring up their own database on offset ports; `down` removes containers and keeps volumes.
7. Editor/Explorer opened from the UI still survives killing `ygg` (PL-05 regression).

## Risks

- Dev servers that ignore `PORT` and have no flag: detection still works, but they can collide; the UI warns, the `custom` override takes `{port}`.
- Antivirus/Defender slowness on `node_modules`, `.venv` and `bin/obj` during bootstrap (documented tip, PL-03).
- Test time: proposals tests already take 135–210 s; new Windows process tests must stay fast and be tagged.
- Subagent spawn failures from stale worktrees (decision 8).
- Usage limits: if hit mid-batch, record each in-flight task's state in this file under "In-flight state".

## Log

- 2026-11-08: the three designs approved with all recommendations (compose file hash-confirm, containers outlive `ygg`, same trust gate for dev servers, no block for main checkouts, token bound to folder + Host/Origin, port probe on allocation, terminal limits 4/8).
- F2 `98aff37` core types and `Step` stubs (`core/devservers`, `core/terminal`, `core/worktreeenv`; `core.PortBlock`, `DevServerID`, `TerminalSessionID`). F3 `ea2b8e3` migration (first numbered 00005, renumbered 00006 after merging Phase 4, which owns 00005) + queries + sqlc. F4 `1d3c64a` DTOs, topics (`devservers`, `devserver:<id>`, `worktreeenv`), tygo output, `platform/orphans.go` stubs (`RecordProcess`, `ForgetProcess`, `ListOrphans`, `StopOrphan`, `PythonBase`; return `ErrNotImplemented` until T2).
- `TestMigration00003RerunIsSafe` now expects version 6.
- The local checkout lacked Phase 4 (it was only on origin): fetched and merged `origin/main` (`0edb96c`) into local `main` with a merge commit, conflicts in `topic.go` (topics merged), generated files (regenerated) and the migration test (Phase 4's `migrate00005_test.go` kept, ours renamed `migrate00006_test.go`). T1 had stopped for lack of `Step.Env`/`Commands.Bootstrap`; it is re-briefed after the merge.
- Checks run on this Linux box: `go vet ./...`, golangci-lint on core/api/platform/db (0 issues), tests for api, archtest, db, platform, core. Pre-existing and unrelated: `api.TestJoinInside` fails on Linux (a `C:/x` path case); `db.TestListActiveTaskStates` is order-flaky (equal timestamps, random IDs). `pnpm` is not installed here, so `task typecheck` and `task test:web` could not run; the generated TS is committed unchecked and the frontend task must run them.

### Batch 1 (merged 2026-11-08)

- T3 `543ee50`, T2 `ab12463`, T1 `182c25a`. Gate 1: safety-reviewer PASS, fe-reviewer no Blocking/Must-fix.
- T3 deviation: a repo listed twice in one `Ensure` request now returns the first worktree (invariant "one worktree per task+repo").
- T2: child-only Ctrl+C is done by clearing `CONSOLE_IGNORE_CTRL_C` in the suspended child's PEB (`clearChildIgnoreCtrlC`); ygg's own flag and `SetConsoleCtrlHandler` are never touched. **Unverified on Windows** (decision 4: if `TestPTYCtrlCInheritedIgnoreFlag` fails there, stop and ask the human). Windows-only tests never run here: `TestPTYCtrlC`, `TestPTYCtrlCInheritedIgnoreFlag`, `TestDevServerTreeDiesWhenHostIsKilled`, `TestOrphanFunctionsAreNoOps`, `TestDefaultShellIsPowerShell`; the Windows `PythonBase` `py -3` path has no test.
- Unix orphan handling needs the caller (`internal/devservers`) to call `RecordProcess` after start, `ForgetProcess` on exit, `ListOrphans`/`StopOrphan` at startup, and to **audit** orphan stops (platform cannot audit).
- Should-fix carried: (1) pip bootstrap used a bare `python` that `Resolve` maps to the (not yet existing) venv: fixed by a `python-base` marker resolved through `PythonBase` (merged); (2) Go dev server has no port injection (`go run .`): relies on detection, UI warns; (3) `ensure.go` nil-input fallthrough, FS error misclassified as git, load-cancel mapping, orphaned worktree after a failed record not named in the error; (4) no `docs/design/worktrees.md`, property test is one `TestPropInvariants` and lacks well-sequenced-but-wrong-state inputs; (5) `.venv/python` is relative to `Detection.Dir`, so verify must pass that dir as the working dir to `Resolve`.
- Foundation tidy: `devservers.Failed.Message` added, `worktreeenv.ComposeRejectReason` made a closed type. fe-reviewer "Consider" items for the stub types (closed `KillAttempt`, audit action types, step-does-not-mutate-input property, `Now` alias) go to the owning tasks D, E1, P, E2.
- Checks on this box: build, vet (linux/windows/darwin), tests for platform, worktrees, core, builds, verify, archtest pass; `golangci-lint` clean except the Phase 4 gosec G302 in `internal/platform/landing_unix.go`. `cmd/ygg` tests `TestCodeModeEndToEnd_ProposalCommitConfirmedThroughTheAPI` and `TestPhase4ServicesAreWiredAndMergeLocalWaitsForConfirm` fail here (no git identity for the clean env) and `TestLoadConfig` expects a Windows path; not caused by this batch.
