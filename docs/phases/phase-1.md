# Phase 1 — Foundations → `v0.2.0`

> Status: **plan, awaiting approval** · 2026-10-15
> Source: docs/PLAN.md Phase 1; docs/PRD.md AG-01–AG-03, AG-05, AG-23 (schema only), SF-01, SF-04, SF-05, PL-02, PL-04, PL-05, WE-04, UI-02, UI-04, IN-04, D12, D15, D17; docs/spikes.md (B1–B4); docs/AGENTS.md sections 5–9.

**Exit criteria (PLAN.md):**
- E1. A mock agent task streams events end to end into a basic transcript view.
- E2. Every spawned process goes through `platform.Start`.
- E3. Safety helpers have tests.

---

## Starting state

- `main` has a 0.1.0 changelog entry and is 2 commits ahead of `origin`; **CI pipeline is shown as failed** (Phase 0 "CI green" exit is still open; check before starting).
- `docs/phases/phase-0.md` has an uncommitted edit (yours). I won't touch it. Commit or stash it before Batch 1, so worktrees branch from a clean `main`.
- Root module has **no dependencies** yet. `web/` has React, TanStack, Zustand, Radix Tooltip, xterm; it lacks `cmdk`, `react-grid-layout` and more Radix primitives.
- Spike code to rebuild from (not copy blindly): `spikes/conpty` (PTY interface), `spikes/jobobject`, `spikes/agentstream` (draft `Event`, parser, hook, env, `testdata/live/*.jsonl`), `spikes/sqlite` (pools, pragmas, goose provider, sqlc config).

## Carried-in constraints (from docs/spikes.md)

1. Clean env is mandatory for every spawn, **including editors**. A nil env is an error in `platform.Start`.
2. Job per process via `PROC_THREAD_ATTRIBUTE_JOB_LIST` or suspended→assign→resume. One main job (`KILL_ON_JOB_CLOSE`, no `BREAKAWAY_OK`) with a nested job per task. No `NoJob` path.
3. Editors/Explorer start outside every `ygg` job (PL-05); inside a foreign outer job use Explorer COM `ShellExecute`, not parent spoofing.
4. Worktree/cwd paths are expanded to long form (`GetLongPathNameW`) before use (8.3 TEMP breaks `dontAsk`).
5. ConPTY close order: `ClosePseudoConsole` → drain output → close pipes. Decide the Ctrl+C handling (spike B1) and record it.
6. Agent adapter: `--` before the prompt; keep paths and settings out of hook args; never log or audit raw `init`, `commands_changed`, `rate_limit_event`; depth/concurrency env defaults are 3 and 20 (lower only); `Agent` tool name; `onFailure: "block"` on enforcing hooks.
7. Static assets served from an explicit allowlist (not `http.Dir`); register `.mjs` as `text/javascript`.
8. Sqlite: two pools (1 writer `_txlock=immediate`, N readers `_query_only`), pragmas per connection, goose provider, sqlc from goose files.

---

## Foundations (lead, sequential, committed to `main` before any fan-out)

| # | Task | Owns | Definition of done |
|---|---|---|---|
| F0 | **Dependencies** via `propose_dependencies` (needs your confirmation). Go: `modernc.org/sqlite`, `pressly/goose/v3`, `coder/websocket`, `golang.org/x/sys`, `golang.org/x/sync`, `creack/pty`; `tool` directives for `sqlc` and `tygo`. Web: `cmdk`, `react-grid-layout`, `@radix-ui/react-dialog`, `-dropdown-menu`, `-popover`. Versions pinned, pure Go and license-checked. | `go.mod`, `go.sum`, `web/package.json`, `web/pnpm-lock.yaml` | `task license` passes; `CGO_ENABLED=0 go build ./...` passes. |
| F1 | **Core types** in `internal/core`: IDs; `Project`, `Repo`, `Worktree`, `Task` (with `ParentTaskID *TaskID`, mode, scope, agent, status), `ChangeSet`, `Proposal` (kind, status), `Build` (+steps), `AuditEntry`; enums with JSON tags. No imports from `api`, `agents` or `web`. | `internal/core/**` | Compiles; unit test for enum validation and ID parsing. |
| F2 | **Agent contract** in `internal/agents/adapter.go`: `Adapter` (`Check`, `Start`, `Resume`, `Cancel`, `Capabilities`), `Event` (kind, `ID`, optional `ParentID`, payload per kind: message, tool call, file edit, command, usage, error, status), `Capabilities` (resume, cost, plan limits, permission hooks, enforcement level, sub-agents none/flat/nested, budget cap), `StartOptions` (worktree, env, depth/concurrency caps, budget). Based on `spikes/agentstream/event.go` and `capabilities.go`. | `internal/agents/adapter.go`, `event.go`, `capabilities.go` | Compiles; doc comments state the rule "no format leaks past the adapter". |
| F3 | **Platform and safety contracts** (interfaces and signatures only, no OS code): `platform.Spec` (argv, cwd, `Env []string` required, job scope), `platform.Process` (Wait, Kill via job, stdio), `platform.PTY` (from B1), `safety.Env` builder signature, `safety.Redactor` signature. | `internal/platform/platform.go`, `internal/safety/safety.go` | Compiles for windows/darwin/linux (`go vet`). |
| F4 | **API contract types** in `internal/api/types.go`: error body `{code, message, hint}`, WebSocket envelope `{type, topic, seq, data}`, health response, task-event message. Plus **audit** writer interface (`audit.Writer.Write(ctx, Entry)`). | `internal/api/types.go`, `internal/audit/audit.go` | Compiles. |
| F5 | **Migration 00001** (projects, repos, worktrees, tasks incl. `parent_task_id`, change_sets, proposals, builds + build_steps, task_events, audit_log) and `sqlc.yaml`. Queries are written in Batch 1. | `internal/db/migrations/00001_init.sql`, `sqlc.yaml` | `sqlc generate` runs offline; schema reviewed against AG-23. |
| F6 | **Generation pipeline**: `tygo.yaml` (core, agents events, api types → `web/src/api/gen/{core,agents,api}.gen.ts`, one file per Go package); `task generate` runs sqlc then tygo; CI `git diff --exit-code` already exists. | `tygo.yaml`, `Taskfile.yml` (generate only), `web/src/api/gen/**` | `task generate` is a no-op on a second run; a type change breaks `task typecheck`. |

Commit each step with its generated output (via `propose_commit`). Batch 1 starts only after F0–F6 are on `main` and you confirm.

---

## Batch 1 (3 parallel)

### T1 — Platform core · `platform-windows`
- **PRD:** PL-02, PL-04, PL-05, WE-04, SF-04, D12.
- **Owns:** `internal/platform/**` (everything except the PTY and port-detection files, which are Batch 2: `pty*.go`, `ports*.go`).
- **Must not touch:** other `internal/*`, `web/`, `cmd/`.
- **Work:** paths (long form, short worktree names helper, WSL `\\wsl$` detection with a plain-language error); executable/wrapper resolution (`mvnw.cmd`, `npm.cmd`, venv python); `platform.Start` with main job + nested task jobs (spike B2 decisions), required non-nil env, context cancel kills the job; open editor/folder outside the job; stubs for darwin/linux that compile and `go vet`.
- **Interfaces:** F3 `platform.go`.
- **Done:** `task lint`, `task test` pass; `GOOS=linux|darwin go vet ./...`; Windows tests: path with spaces, long path, drive letter, children and grandchildren die when the job closes, context cancel kills only that task, nil env refused, editor process survives.
- **Exit criteria:** E2, E3 (platform part).

### T2 — Database, audit, safety · `go-backend`
- **PRD:** SF-04, SF-05, IN-03.
- **Owns:** `internal/db/**` (except F5 migration), `internal/audit/**`, `internal/safety/**`.
- **Must not touch:** `internal/platform`, `internal/core` (ask the lead if a type must change), `web/`.
- **Work:** `db.Open` with the two-pool setup and pragmas from B3; goose provider over embedded migrations; sqlc queries (`queries/*.sql`) for each core entity (create/get/list/update status; append and list audit; append and list task events); store layer wrapping sqlc and mapping to `core` types; `audit` SQLite writer implementing F4's interface, which redacts before writing; `safety.Env` (allowlisted base env, drops `VIRTUAL_ENV`, `CONDA_PREFIX`, `PYTHONPATH`, refuses token names, never uses `os.Environ` outside this package) and `safety.Redactor` plus an `slog.Handler` wrapper (tokens, `Authorization`, URLs with credentials).
- **Done:** `task generate` clean; `task lint`, `task test` pass; tests: migrations up/down/up, FK cascade, DB path with spaces, concurrent writers (`task test:race`), env builder with token variables set in the parent, redaction table tests (invariant 4), audit entries never contain a token.
- **Exit criteria:** E3.

### T3 — Frontend shell · `frontend`
- **PRD:** UI-04, UI-05 (System + Harbour only), UI-08.
- **Owns:** `web/src/**` **except** `web/src/widgets/**` and `web/src/api/gen/**`.
- **Must not touch:** Go code, `Taskfile.yml`, `web/embed.go`.
- **Work:** router with routes `/`, `/tasks/:id`, `/projects`, `/settings` (placeholder pages); TanStack Query client + typed fetch wrapper that understands the F4 error body; WebSocket client (reconnect, `seq` gap detection, topic subscribe) in `web/src/api/ws.ts`; theme tokens (System, Harbour) as CSS variables plus the Zustand store (extends the current `stores/theme.ts`); top bar; `Ctrl+K` palette skeleton (`cmdk`, registers commands from a registry); notifications area; status pill; keyboard-reachable core flows.
- **Interfaces:** `web/src/api/gen/*.gen.ts` (read-only).
- **Done:** `task lint`, `task typecheck`, `task test:web`, `task build` pass; Vitest tests for the WS client (reconnect, gaps), theme store and palette registry.
- **Exit criteria:** E1 (shell).

**Batch 1 gate:** merge one at a time (T2 → T1 → T3), run the full check list, then `safety-reviewer` on the batch diff until PASS.

---

## Batch 2 (3 parallel; after Batch 1 is merged and reviewed)

### T4 — Platform PTY and ports · `platform-windows`
- **PRD:** PL-02, DV-04 (prep), DV-02 (prep), D12.
- **Owns:** `internal/platform/pty*.go`, `internal/platform/ports*.go` (no other platform files; T1 is merged by now).
- **Work:** own ConPTY wrapper (`CreatePseudoConsole` + attribute list with the pseudoconsole and job list, close order from B1, required env) behind the `platform.PTY` interface; `creack/pty` on Unix; port detection (parse `netstat`-equivalent via Win32 TCP table, no shell) by PID tree.
- **Done:** `task lint`, `task test`; Windows tests from B1 ported (output, Ctrl+C, resize, exit code, close kills tree, no leaked handles in a 200-session loop).
- **Exit criteria:** E2, E3.

### T5 — Git wrapper · `go-backend`
- **PRD:** WE-04, SF-03, SF-06 (prep).
- **Owns:** `internal/git/**`.
- **Interfaces:** `platform.Start`, `safety.Env` (no direct `exec`).
- **Work:** CLI wrapper for the safe subset only: status (porcelain v2), branch list, log, diff, show, fetch, ff-only pull, checkout, `worktree add/list` (no remove, no reset, no force push, no branch delete: those functions must not exist), always `-c core.longpaths=true`, `-c credential.helper=` and no token in args. Output parsers tested on fixture text.
- **Done:** `task lint`, `task test`; tests use `t.TempDir()` repos only; test that the API has no destructive method (reflection/AST check); CRLF and path-with-spaces cases.
- **Exit criteria:** E2, E3.

### T6 — Agent runner and mock adapter · `go-backend`
- **PRD:** AG-01, AG-02, AG-03, AG-21 (events only), D15.
- **Owns:** `internal/agents/runner.go`, `internal/agents/mock/**` (including `testdata/*.jsonl`), `internal/agents/testdata/**`.
- **Must not touch:** `adapter.go`, `event.go`, `capabilities.go` (lead-owned contract; ask the lead if it must change).
- **Work:** mock adapter replaying JSONL fixtures with configurable pacing and cancel; fixtures: plain task, tool calls and edits, error, **nested sub-agents** (derived from the scrubbed `spikes/agentstream/testdata/live/subagent-read.jsonl`, normalized to the F2 format); `Runner` that starts an adapter under a context, assigns monotonic sequence numbers and passes events to an `EventSink` interface defined in the runner (persistence and broadcast are wired by T8); capability checks.
- **Done:** `task lint`, `task test`, `-race`; tests: parent/child links preserved, cancel stops replay, sequence numbers have no gaps, goroutines stop on cancel (leak test).
- **Exit criteria:** E1 (backend).

**Batch 2 gate:** merge T4, T5, T6 one at a time; full checks; `safety-reviewer`.

---

## Batch 3 (3 parallel)

### T7 — API server · `go-backend`
- **PRD:** SF-01, SF-05, AG-01, UI-04.
- **Owns:** `internal/api/**` except `types.go` (lead contract).
- **Work:** HTTP server on `127.0.0.1` (non-loopback refused), Host and Origin checks (WS Origin check too), error format from F4, explicit-allowlist static handler for the embedded UI with SPA fallback and `.mjs` MIME fix, WebSocket hub (topics, bounded per-client buffers, `seq`, drops slow clients), `slog` handler using the redactor, endpoints: `GET /api/health`, `POST /api/tasks` (create + start via runner), `GET /api/tasks/{id}`, `GET /api/tasks/{id}/events`, `POST /api/tasks/{id}/cancel`; every state change writes an audit entry. Implements the runner's `EventSink` (persist via `db`, then broadcast).
- **Done:** `task lint`, `task test`, `-race`; tests: Host/Origin cases including DNS rebinding and `localhost.evil.com`, error format, WS reconnect with backfill, audit entry per mutation, goroutines stop on shutdown.
- **Exit criteria:** E1, E3 (invariants 7, 8).

### T8 — Claude Code adapter · `go-backend`
- **PRD:** AG-03, AG-05, AG-21, AG-22, D15, D17 (detection of repo hooks is Phase 3; only a `Check` note here).
- **Owns:** `internal/agents/claudecode/**`.
- **Interfaces:** F2 contract, `platform.Start`, `safety.Env`.
- **Work:** build the command line from the B4 findings (`--`, `--forward-subagent-text`, `--setting-sources project`, `--settings` file written to a temp dir, enforcing hook entry using exec form, `onFailure: "block"`, caps via env); parser mapping stream lines to normalized events with `ParentID`; hook subcommand logic exposed as a function that `cmd/ygg` wires (a hidden `ygg hook` subcommand, wired in T10); `Check(ctx)` for CLI present and signed in; usage and cost to `usage` events; raw `init`, `commands_changed`, `rate_limit_event` never emitted or logged.
- **Done:** `task lint`, `task test`; tests run only on recorded fixtures (the scrubbed live captures, copied into `testdata/`); no test calls the real CLI; sub-agent nesting, `permission_denied` string messages, malformed and unknown lines covered.
- **Exit criteria:** E3 (partial: adapter parsing). The reference adapter is checked against the real CLI manually (see checklist).

### T9 — Widgets and transcript · `frontend`
- **PRD:** UI-02, AG-14 (basic), AG-21 (rendering).
- **Owns:** `web/src/widgets/**`, `web/src/routes/tasks/**`, `web/src/components/transcript/**`.
- **Must not touch:** T3's shell files (merged earlier); if a shell change is needed, ask the lead.
- **Work:** widget module interface (`id`, `title`, `defaultSize`, `Component`, `actions`) and registry; grid on `react-grid-layout` with Customize mode (add, remove, move, persist in local storage); two example widgets ("Working now", "This machine" placeholders) to prove the interface; transcript view for a task: messages, tool calls, file edits, nested sub-agent tree from `parentId`, live updates through the WS client, backfill on reconnect.
- **Done:** `task lint`, `task typecheck`, `task test:web`, `task build`; Vitest: nested tree builder, out-of-order and duplicate events, widget registry; widgets don't import each other (ESLint rule).
- **Exit criteria:** E1.

**Batch 3 gate:** merge T7, T8, T9; full checks; `safety-reviewer`.

---

## Batch 4 (3 parallel)

### T10 — Wiring, doctor, mock-task path · `go-backend`
- **PRD:** IN-04, AG-05, SF-01.
- **Owns:** `cmd/ygg/**`.
- **Work:** build the real server in `main` (config from env, DB in `YGG_DATA_DIR`, main job created at startup, graceful shutdown that closes the job); `ygg doctor` (git, Node, Go; agent `Check` results; plain-language fixes; exit code); hidden `ygg hook` subcommand for the Claude Code adapter; `YGG_AGENT=mock` dev switch that registers the mock adapter so the full path works without a real agent.
- **Done:** `task lint`, `task test`, `task build`; `ygg doctor` golden-output test with fake probes.
- **Exit criteria:** E1.

### T11 — Architecture and end-to-end tests · `test-engineer`
- **PRD:** SF-04, SF-05, PL-02; docs/AGENTS.md sections 5.2, 5.3, 6, 8.
- **Owns:** new test-only package `internal/archtest/**` and `web/e2e/**` (Playwright config only if a pinned dependency is approved; otherwise Vitest integration against the mock server).
- **Work:** AST test: no `exec.Command*`, `runtime.GOOS`, `os.Environ` outside the allowed packages, `core` imports nothing forbidden, no destructive git function names; Go e2e test (`httptest` server + mock adapter + real WS client): create task → events arrive in order, nested events keep `ParentID`, audit rows exist; Windows-only test: starting a task through the API and killing the process tree leaves no child; list of invariant tests mapped to docs/AGENTS.md section 6 (a table in `internal/archtest/README` is **not** created; the mapping goes into this phase file).
- **Done:** `task test`, `task lint`.
- **Exit criteria:** E1, E2, E3.

### T12 — Settings and polish for the shell · `frontend`
- **PRD:** UI-04, UI-08.
- **Owns:** `web/src/routes/settings/**`, `web/src/routes/projects/**` (placeholders become real empty states), `web/src/components/notifications/**`.
- **Work:** notifications area wired to WS events (task finished / needs you: text limited to task name and status, AGENTS 5.9), status pill showing running tasks and connection state, composer stub on Home that starts a mock task, link to the transcript.
- **Done:** `task lint`, `task typecheck`, `task test:web`, `task build`.
- **Exit criteria:** E1.

**Batch 4 gate:** merge T10, T11, T12; full checks; `safety-reviewer` on the whole phase diff.

---

## Parallelism check

No two tasks in a batch own the same file or package: Batch 1 (`platform` / `db+audit+safety` / `web/src` shell), Batch 2 (`platform` pty+ports files / `git` / `agents` runner+mock), Batch 3 (`api` / `agents/claudecode` / `web/src/widgets`+`routes/tasks`), Batch 4 (`cmd/ygg` / `archtest`+`web/e2e` / `web/src/routes/settings`+`projects`). T1 and T4 both touch `internal/platform` but run in different batches with disjoint files.

## PLAN.md checklist → tasks

| PLAN.md item | Task |
|---|---|
| Data model and migrations | F1, F5, T2 |
| `internal/platform` | T1, T4 |
| `internal/safety` | T2 |
| `internal/git` | T5 |
| `internal/api` | F4, T7 |
| `internal/audit` | F4, T2 |
| `tygo` pipeline | F6 |
| Adapter, `Event`, `Capabilities` | F2 |
| Task model with `ParentTaskID` | F1, F5 |
| Mock adapter + nested fixture | T6 |
| Claude Code adapter | T8 |
| Frontend shell | T3, T12 |
| Widget interface and grid | T9 |
| `ygg doctor` | T10 |

## Safety-invariant coverage in this phase (AGENTS.md section 6)

| Invariant | Test owner |
|---|---|
| 1 never implement destructive git | T5, T11 |
| 3 tokens never reach child processes | T2, T1 (nil env refused), T11 |
| 4 tokens never in logs/URLs/audit | T2 |
| 7 bind to 127.0.0.1, Host/Origin | T7 |
| 8 state changes audited | T7, T11 |
| 2, 5, 6, 9, 10, 11 | Phase 3 (no code yet) |

## Manual verification on Windows (for you, at phase close)

1. `task build`, run `bin\ygg.exe`, open `http://127.0.0.1:4747`: shell loads, theme switch (System/Harbour), `Ctrl+K` opens the palette, keyboard-only navigation works.
2. `set YGG_AGENT=mock`, start a task from Home: transcript streams, nested sub-agent tree shows, reload the page and the transcript backfills.
3. Kill `ygg.exe` from Task Manager while a mock task and a test child process run: no child left.
4. `ygg doctor`: output is readable; unplug a prerequisite (rename `git` in PATH) and the message says what to do.
5. `curl -H "Host: evil.example" http://127.0.0.1:4747/api/health` is refused; a non-loopback `YGG_ADDR` is refused.
6. Run one task with the real Claude Code adapter (needs your OK in that session; Haiku, small budget): events appear, a sub-agent nests, cancel leaves no `claude`/`node` process. Not part of automated tests.
7. Defender test (spike B2 note): editor launch from `ygg` while it runs inside Windows Terminal; the editor survives closing `ygg`.
8. Antivirus/long-path: a repo under a path longer than 260 characters works with the git wrapper.

## Risks and open points

- **Dependencies need your confirmation** (F0) before anything else; the shell is offline.
- T1 is the biggest and riskiest task; if it stalls, nothing past Batch 1 that spawns a process can be verified on Windows.
- Ctrl+C handling for ConPTY children (side effect on `ygg`'s own console) is decided in T4 and noted here.
- The `Event` payload shape (F2) is the contract most likely to need changes; T6, T8 and T9 must ask the lead instead of editing it.
- Playwright is not planned for this phase (`task test:e2e` stays a stub); T11 uses Go/Vitest integration instead. Say if you want Playwright now.

## Decisions / deviations log
- F0 (2026-10-15): `go mod tidy` removes requires that nothing imports yet, so dependencies are only safe in `go.mod` once code imports them. `tygo` pulls an old `x/tools`; `go get` upgraded it to v0.51.0. `sqlc` needs `go mod tidy` for its `go.sum` entries.
- F6: `tygo` writes one file per Go package (`web/src/api/gen/{core,agents,api}.gen.ts`), not a single `types.gen.ts`. `type_mappings` are per package; a cross-package type without a mapping becomes `any` silently, so every one is mapped by name. `web/eslint.config.ts` ignores `src/api/gen`. `.claude/agents/frontend.md` still names `types.gen.ts` (the lead can't edit `.claude/`).
- Batch 1 merged: T1 `52471a4`, T3 `01865fc`, T2 `ac73152` (T2 restarted once, because `go.mod` lacked its dependencies). After the merges `task generate` (no diff), `lint`, `typecheck`, `test` and `test:web` pass.
- Batch 1 safety review (read-only researcher): **FAIL, 3 Blocking.**
  - **B1 editor fallback env — decision, not changed.** Inside a foreign outer job that forbids breakaway, `OpenEditor` falls back to Explorer's `ShellExecute` (spikes B2 decision). The editor then has Explorer's environment, the user's own session environment, never Yggdrasil's (`.env` tokens are loaded into `ygg`, not into Explorer). The alternative is to refuse and tell the user to start `ygg` outside that terminal. Kept, pending the human's veto. To revisit before v1.
  - **B2 JSON-escaped configured secrets in audit detail — fixed.** `NewRedactor` also masks each secret's JSON-escaped forms (with and without HTML escaping); test `TestRedactJSONEscapedSecret`.
  - **B3 struct, map and slice log attributes bypassed redaction — fixed.** The slog handler now formats any other value with `%+v` and redacts it; test `TestLogHandlerRedactsStructAttrs`.
  - Should-fix 4 (audit key pairs narrower than log keys) fixed: `credential` and `private_key` added to the pair pattern.
- Should-fix carried forward (reviewer numbers):
  - 5 env builder checks only names of Extra, not values; `looksSecret` misses AUTH, COOKIE, SESSION. → T10 or a small follow-up.
  - 6 `ws.ts` resets backoff on every open, so a server that accepts then closes is retried every 500 ms. → T12 (frontend shell polish).
  - 7 `OpenFolder` searches PATH for `explorer.exe` when `SystemRoot` is empty; 8 a path starting with `-` is read as an editor option (make it absolute). → T4 (platform) follow-up.
  - 9 `fixtures/demo-gen` uses `runtime.GOOS`; it is a separate tooling module. → accepted, T11 allowlists it.
  - 10 `db.Store.InsertAudit` is exported and unredacted (needed by `internal/audit` across packages). → document; T11 test forbids other callers.
  - 11 nothing is wired yet (slog handler, redactor, audit writer, `platform.Shutdown`). → T10.
  - 12 no tracked-repo check in `platform` or `CheckPath`. → Phase 3 path guard, before any agent call site.
  - 13 `cancelTree` with a `TaskID` kills the whole task job, so one per-call context cancel kills siblings. → documented on `Start`; add a test in T4.
- Batch 2 (2026-10-15/16): T5 `git` merged (`7f8f18d`). T6 runner and mock merged as WIP (`6f85423`; the first pass was never compiled) and fixed by the lead (`9ed0c05`: one wrong test assertion, `gofmt`); `go vet`, `test`, `-race`, `gofmt` and `lint` pass for `internal/agents/...`. The T6 continuation subtask (#9) and the first T4 attempt (#5) were stopped by "Bash/Edit denied" in their sessions and changed nothing.
- **In-flight state, T4 platform (#8, branch `worktree/sunny-spinning-pelican`):** stopped by the human's usage limit with 14 uncommitted files (+1495 −8): `pty_windows.go`-style ConPTY wrapper, `pty_unix.go`, `ports_windows.go`, `ports_unix.go`, tests (B1 port, 200-session handle count) and the review follow-ups (OpenFolder SystemRoot, `filepath.Abs`, task-job tests). Not compiled or verified. Its last action was removing the old leak test from `pty_windows_test.go`. Resume plan: commit the WIP in the worktree, start a continuation subtask with `base` = that branch, run `go vet`, `go test`, `gofmt -l`, `task lint`, `GOOS=linux|darwin go vet`, check the follow-ups and the Ctrl+C decision comment, then merge.
- T4 merged as WIP (`3db0494`) and verified by the lead (2026-10-16). Four fixes, none in production code except a lint annotation:
  - **Test fork-chain (important):** `OpenEditor` now applies `filepath.Abs`, which turned the stand-in editor's argument `editor-sleep:<file>` into `<cwd>\editor-sleep:<file>`. The platform `TestMain` no longer recognised it and ran the whole suite (`m.Run()`) inside the "editor", which started more editors: a chain of `platform.test.exe` and console windows that filled the human's machine. `TestMain` now treats only `-test.*` as a test run, finds the editor prefix anywhere in the argument, and exits with an error on any other argument. Lesson: a test helper must never fall through to `m.Run()`.
  - `TestTaskJobClosesWhenAllProcessesExit`: the `orphan` helper exited at once, so the grandchild died before the test opened it; it now waits 1 s.
  - `TestListeningPortsDescendants`: the "sibling" assertion was wrong (the test process is an ancestor); it now asserts the ancestor sees the port.
  - `TestPTYNoHandleLeak`: slack `max(25, n/4)`; measured noise is 6 over 20 sessions and 24 to 30 over 200 (a real leak adds at least n).
  - `pty_windows.go:151`: the gosec annotation moved to the flagged line.
  - T4's Ctrl+C approach: `allowCtrlC` clears the inherited "ignore Ctrl+C" flag only when ygg itself has it set, only during `CreateProcess`, and restores it (mutex-serialised). ygg's own console behaviour does not change. Accepted.
  - Checks: `go vet` for windows, linux and darwin, `gofmt -l`, `golangci-lint` (0 issues), `task test` all pass. Manual-only: the editor tests open a console window for about 2 s, and `task test` is slow (platform about 45 to 80 s).
- Batch 2 safety review (read-only researcher): **FAIL, 2 Blocking, both fixed (2026-10-16).**
  - B1 the `internal/git` invariant scan skipped `_test.go` files, so a destructive call in a test helper would pass. It now scans test files too (no destructive words, no `--discard`, no `os/exec`; a plain `push` to a local bare remote is allowed there). New tests prove it catches force push, hard reset, discard, `os/exec` and `checkout -f` in a test file.
  - B2 `parseStatus` panicked on a status entry with an empty or one-byte XY field. Both the `1 ` and `2 ` entries now check `len(XY) == 2`; `TestParseStatusMalformed` covers it.
  - Should-fix done: `Resolve` skips relative PATH entries (it promised never to search the current directory); the mock adapter declares only what it does (no hook enforcement, no sub-agent limits; enforcement "limited"); `allowCtrlC` returns without a restore when `SetConsoleCtrlHandler` fails.
  - Should-fix carried forward: git `baseConfig` lacks `core.fsmonitor=false` and `core.hooksPath` (untrusted repos, SF-09, Phase 3); the scan bans only `os/exec` (add `os.StartProcess`, `syscall`/`windows.CreateProcess`, walk subdirectories) → T11; `Close` on timeout leaves pipes open with the drain goroutine blocked (T4 follow-up, low risk); `ctrlCMu` spans job setup, not only `CreateProcess` (narrow it); `Execute` blocks forever if an adapter ignores cancel (add a stuck-adapter test or a bounded wait) → T7 uses it; the Unix reaper goroutine is not tied to `ctx` (document).
- Known limits: the redactor masks `Basic <8+ chars>` prose; SQLite triggers can be dropped by anyone who can write the DB file.
