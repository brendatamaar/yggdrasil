# Phase 1 — Foundations → `v0.2.0`

> Status: **done, waiting for your review before the tag** · plan written 2026-10-15, closed 2026-10-16
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
- Batch 3 (2026-10-16): all three subtasks (#10 T7, #11 T8, #12 T9) stopped at once with "Bash denied" and changed nothing, so the lead wrote them: T7 `58116307`, T8 `a1eb4301`, T9 `7f7403f0` (+ `ae298826` for the `tasks` topic and list types).
- Batch 3 safety review (read-only researcher): **FAIL, 4 Blocking, all fixed.**
  - **B1 prompt text on the `tasks` topic and in notifications.** The title was the first 60 characters of the prompt. Now `CreateTaskRequest.Title` is what the person typed (single line, 80 runes), else `Task <last 6 of id>`; the composer has an optional "Task name" field. Tests: the topic never carries the first line or the secret line.
  - **B2 Ask mode was only requested by CLI flags.** `HookConfig.AllowedTools` makes the hook deny any tool call outside Read, Grep, Glob (sub-agent calls keep their own rules; calls from inside a sub-agent are held to the same list), and `--disallowedTools` is a second request.
  - **B3 Read/Grep/Glob were not limited to the repo (invariant 6).** `HookConfig.AllowedDir`: every path field must lie inside the repo (long form, links resolved, lexical `..` as Windows reads it; nonexistent files handled through their deepest existing folder), a Read must name a file, and globs may not contain `..`, a drive, a stream marker or be absolute. Reasons shown to the agent hold no path.
  - **B4 the repo's own `.claude` settings and hooks ran in Ask mode (invariant 10).** `Start` refuses a repo that has `.claude/settings.json`, `.claude/settings.local.json`, `.mcp.json` or `.claude/agents`, with a plain message, until repo trust exists (SF-09, Phase 3). Consequence: a repo with a `settings.local.json` (common) can't run an agent yet.
  - Should-fix done: non-user errors from `Check` are no longer sent to the client (`UserMessage()` interface); `failTask` and the session write are audited first; at most 64 subscriptions per WebSocket client; model names validated (`^[A-Za-z0-9][A-Za-z0-9._:-]{0,63}$`, API and adapter); event text, tool input, diffs and commands are redacted before they are stored or sent (JSON stays valid); a panic in a run fails that task only; `POST /api/tasks` is refused during shutdown and `Close` serialises with `start` so a task can't join the group after `Wait`; a `Content-Security-Policy` header.
  - Accepted and documented: the hook `command` still holds the absolute `ygg` path (the CLI shows it in "hook error" lines); event reads are unauthenticated, which is fine on a loopback single-user server with 64-bit random task IDs.
  - Manual checks that need the real CLI (not automated on purpose): (1) the hook receives `YGG_HOOK_CONFIG` from the CLI's environment (if not, every tool call is denied: safe but useless); (2) `--disallowedTools` is accepted by the installed CLI; (3) the CSP does not break the UI in a browser (grid, palette).
- Batch 4 (2026-10-16), written by the lead like Batch 3 (subagents kept stopping on "Bash denied"):
  - T10 `cmd/ygg`: `config.go` (settings: `YGG_ADDR`, `YGG_DATA_DIR`, `YGG_LOG`, `YGG_MOCK_DELAY_MS`), `app.go` (wiring: database, audit, redacting logger, demo agent `mock` and `claude-code`, the server), `doctor.go`, `seed.go`, the hidden `ygg hook`, and `main.go`. `internal/api` now serves the UI, so `cmd/ygg/ui.go` is removed. `platform.Shutdown` (closing the process-wide job) is called once by `main`, not by `app.run`, so tests in one process can keep starting processes.
  - `ygg seed <folder>` is a stand-in for the project pages (WS-01, Phase 2): it tracks a repo, audits it and prints the two IDs the composer asks for. It never changes the repo.
  - T11 `internal/archtest`: reads the module's source and fails if a process starts outside `internal/platform` (`os/exec`, `os.StartProcess`, `syscall.StartProcess`, `windows.CreateProcess`), `runtime.GOOS` is read outside it, `os.Environ` is read outside `internal/safety`, `InsertAudit` (no redaction) is called outside `internal/audit`/`internal/db`, or a package imports one it must not (`core`, `safety`, `platform` are leaves; `db` knows `core` only; the agent contract knows `core` only; `api` imports no adapter; `git` and `audit` stay below `api`/`agents`). Every rule is also run on source that breaks it. Only the `ygg` binary may exist. It found two bugs in its own rules while being written.
  - Not done on purpose: a Windows end-to-end test that kills `ygg` and counts child processes. The job-object tests in `internal/platform` cover the mechanism and `cmd/ygg/app_test.go` covers the wiring; the combination is on the manual list below.
  - Playwright is still not in use (`task test:e2e` stays a stub).

## Phase close (2026-10-16)

Proposed tag: **`v0.2.0`**, for you to create. I did not tag, push or start Phase 2.

### Exit criteria

| Criterion | Met | Evidence |
|---|---|---|
| A mock agent task streams events end to end into a basic transcript view | yes, in tests; manual run left | `cmd/ygg` `TestAppRunsADemoTaskEndToEnd` (seed, POST, events with parent links stored in order); `internal/api` live WebSocket, backfill and cancel tests; `web` `useTranscript`, `Transcript`, `TaskPage` tests |
| Every spawned process goes through `platform.Start` | yes | `internal/archtest` `TestArchitectureRules/os/exec` and siblings pass on the whole tree; `golangci-lint` `forbidigo` bans `exec.Command*` outside platform; `internal/git` and the Claude Code adapter test that their only start is `platform.Start` |
| Safety helpers have tests | yes | `internal/safety` (env builder, redaction incl. JSON-escaped secrets, log handler), `internal/audit`, `internal/db` (append-only triggers) |

### Safety invariants (docs/AGENTS.md section 6) in this phase

| # | Invariant | Test |
|---|---|---|
| 1 | no force push, hard reset, branch deletion, file deletion | `internal/git` `TestNoDestructiveGit` (source and test files), `TestScanCatchesDestructiveCode` |
| 3 | tokens never reach child processes | `safety` env tests; `platform` nil-env refusal; Claude Code `TestRunCommandLineEnvironmentAndCleanup` (tokens set in the parent never arrive) |
| 4 | tokens never in logs, URLs, audit | `safety` redaction tests; `api` `TestEventsAreRedactedBeforeTheyAreStored`; `cmd/ygg` `TestAppLogsNeverHoldATokenShape` |
| 5 | enforcement outside the agent | Claude Code hook tests (`TestHookEnforcesAskModeAndTheRepoFolder`, guard tests); API refuses Code mode |
| 6 | only tracked repos visible | `api` `TestCreateTaskRefusals` (repo of another project), guard path tests |
| 7 | loopback only, Host/Origin checks | `api` `TestHostAndOriginChecks`, `TestRunRefusesNonLoopbackListener`; `cmd/ygg` `TestServeRefusesARemoteAddress`; `archtest` `TestDefaultAddressIsLoopback` |
| 8 | state changes audited | `api` `TestCancelEndsTheTaskAndIsAudited`; `cmd/ygg` `TestSeedTracksARepoAndPrintsTheIDs` |
| 9, 10, 11 | Ask mode integrity, repo trust, sub-agents never widen access | partly: Ask-only API and hook, `ownAgentConfig` refusal, hook depth/concurrency/isolation tests. The full integrity check (SF-07) and repo trust (SF-09) are Phase 3 |
| 2 | every mutation of a main checkout or remote is a proposal | nothing mutates a checkout yet; Phase 3 |

### Manual verification on Windows (what only you can confirm)

1. **Clean build:** `task build`, then `bin\ygg.exe version`, `bin\ygg.exe doctor`. Expected: git and the data folder are `ok`; the rest `ok` or `--` with a hint. Run `doctor` once with Git renamed out of PATH: it must say `FAIL` and what to do, exit code 1.
2. **Start and demo task:** `bin\ygg.exe seed <path of any local git repo>`, copy the two IDs; `bin\ygg.exe`; open `http://127.0.0.1:4747`; enter a prompt (and optionally a name), the IDs, agent "Demo agent", Start. Expected: the transcript streams (about 250 ms per event), a nested sub-agent block appears with tool-call count and duration, the status pill shows 1 running and then a notification "your task name: Done". Reload the page: the transcript is back. Check the notification shows the name or "Task xxxxxx", never your prompt.
3. **Customize:** on the start page choose Customize, remove "This machine", reload (it stays removed), Add widget brings it back, Reset layout restores both.
4. **Kill test:** start a task with the real agent (step 6) or leave the server idle, then end `ygg.exe` in Task Manager: no `claude`, `node` or `cmd` child is left within 2 s.
5. **Loopback:** `YGG_ADDR=0.0.0.0:4747` refuses to start; from another PC the port is unreachable; `curl -H "Host: evil.example" http://127.0.0.1:4747/api/health` is refused (403).
6. **Real Claude Code (needs your OK, uses a little quota; use a Haiku model and a repo without `.claude/settings*.json`, `.mcp.json` or `.claude/agents`):**
   - the run starts and streams; a sub-agent nests;
   - the hook gets its configuration (if every tool call is denied with "its configuration is missing", the CLI did not pass `YGG_HOOK_CONFIG` on);
   - `--disallowedTools` is accepted by your installed CLI;
   - asking it to edit a file or read `..\..\something` is refused with a plain reason;
   - Cancel leaves no `claude` or `node` process.
7. **Content Security Policy:** with the app open, the grid, palette and notifications menu work and the browser console shows no CSP errors.
8. **Platform tests on your desktop:** `go test -count=1 ./internal/platform/...` passes; afterwards no `platform.test.exe`, `cmd.exe` or `conhost.exe` is left (the editor tests open a console window for about 2 s); `TestLongPathExpandsShortNames` is a PASS, not a SKIP.
9. **From inside Windows Terminal:** repeat step 8; the editor tests (outer job) must still pass.
10. **Defender:** no alert when `ygg` opens an editor through Explorer inside an outer job (the fallback decided in spikes B2; B1 of the Batch 1 review is still waiting for your veto).

### Manual verification progress (the human, 2026-10-16)

- Step 1 **passed**: `task build`, `ygg version`, `ygg doctor` (exit 0 with one optional tool missing; with Git removed from PATH: `FAIL git` with a hint and exit 1). It showed Python 2.7.18 as usable; fixed (`0c028330`), Python 2 is now refused.
- Step 2 **passed**: `ygg seed` printed the IDs; the demo task streamed with a nested sub-agent (1 tool call, 2.3 s, 190 tokens; second sub-agent 752 ms, 70 tokens), total 770 tokens and $0.0115, ending "The agent finished"; the task name was shown, not the prompt. The human also confirmed that nothing looked wrong.
- Steps 3, 4 and 5 **passed** (Customize and its persistence; ending `ygg.exe` leaves nothing behind, with the demo agent, which starts no process, so the real test is step 6; `YGG_ADDR=0.0.0.0` refused, a foreign Host header gets 403).
- Step 6 **found a real bug** (2026-10-17). 6a passed (a repo with `.claude/settings.local.json` is refused; the reason is now shown on the page, not only in the log). 6b (a clean repo, real Claude Code) failed twice with exit code 1 and no event, stderr `'C:\Program' is not recognized as an internal or external command`. Running the same flags by hand worked, so the flags are valid in CLI 2.1.299 (`--disallowedTools` included; the tool list in `init` no longer had Bash, Edit or Write).
  - **Cause:** `claude` is a `.cmd` shim in a folder with a space. `platform.Start` handed it to `os/exec`, and cmd.exe strips the outer quotes of a command line that starts with a quote and holds more than two, so it looked for `C:\Program`. `claude --version` worked because it has no quoted argument. Reproduced at the OS level by `TestBatchFileInAFolderWithSpacesTakesArgumentsWithSpaces`, which failed with the same message before the fix.
  - **Fix (`internal/platform/batch_windows.go`):** a `.cmd` or `.bat` is run as `cmd.exe /d /s /c ""<file>" "<arg>"..."`. Because a batch file is a shell that may expand its arguments again without quotes, arguments holding `" % & | < > ^ !`, a line break or a NUL are refused, not escaped. Backslashes before a closing quote are doubled.
  - **Consequence for the adapter:** the prompt is free text, so it is no longer an argument: it goes to the CLI on **stdin** (`claude -p` reads it from there), which also removes the 8,191-character line limit of cmd.exe. A model name and a session ID are validated before they reach a command line. Tests: any prompt (quotes, `&`, `%`, line breaks, a dash, emoji, 28 KB) arrives whole; the full chain through a `claude.cmd` in `...\Program Files\nodejs`.
  - **Confirmed by hand (the human, 2026-10-17): 6b passed** after the fix: the real Claude Code ran in a clean repo, with the prompt on stdin, `--disallowedTools` accepted, and the hook reading its configuration from `YGG_HOOK_CONFIG` (a missing configuration would have denied every tool call).
- 6c (Ask mode and the repo folder enforced by the hook) and 6d (Cancel leaves no process): not reported yet.
- Steps 7 to 10: not reported yet.

### Decision

- **Editor fallback inside an outer job (Batch 1 review B1): keep it** (the human, 2026-10-16). Inside a foreign job that forbids breakaway, `OpenEditor` starts the editor through Explorer's `ShellExecute`, so it gets the user's session environment, never Yggdrasil's. To be written into the threat model (SF-08) in Phase 3.

### Left open (carried to Phase 2 or later)

- Batch 1 and Batch 2 should-fix items listed above (env value check, ws backoff, `-` option paths, pipes on PTY close timeout, `ctrlCMu` span, scan breadth).
- No project or repo pages: tracking is `ygg seed` until WS-01.
- The bundle is 563 KB; code splitting is a polish item.
- Phase 0 leftovers in PLAN.md (domain name; the CI checkboxes were never ticked although CI is green) are for you.
- The `.claude/agents/frontend.md` file still names `types.gen.ts` (the lead cannot edit `.claude/`).

- Known limits: the redactor masks `Basic <8+ chars>` prose; SQLite triggers can be dropped by anyone who can write the DB file.
