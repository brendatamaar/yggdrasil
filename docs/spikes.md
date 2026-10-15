# Phase 0 spikes

Findings from the Phase 0 spikes (docs/PLAN.md). Spike code lives in `spikes/<name>/`, one Go module each. It's throwaway: Phase 1 rebuilds the parts it keeps in `internal/platform`, under the full rules in docs/AGENTS.md.

Status key: **auto** = covered by the spike's automated tests on Windows; **manual** = on the human checklist at the end; **pending** = not run yet.

---

## B1 — ConPTY terminal (`spikes/conpty`) · D12, PL-02, DV-04

**Setup.** A small `PTY` interface (start with a size, Read/Write, Resize, Wait for the exit code, Close). Implemented with `aymanbagabas/go-pty` v0.2.2. A demo server on `127.0.0.1:4848` bridges the PTY to xterm.js over `coder/websocket`: binary frames carry data, JSON text frames carry control messages (`resize`, `started`, `exit`). Each shell runs in its own job object.

| Check | PowerShell | cmd |
|---|---|---|
| (a) output, keystrokes, Ctrl+C interrupts the command and not the shell | auto | auto |
| (b) resize reaches the shell | auto | auto (`mode con`) |
| (c) interactive program | auto | auto (`choice`) |
| (d) `exit 3` gives exit code 3, socket closes with "exit code 3" | auto | auto |
| (e) closing the socket kills the shell tree, no `conhost`/`OpenConsole` left | auto | auto |
| (f) unicode and colors in the browser | manual | manual |
| Browser end to end (xterm.js, window resize) | manual | manual |

**Verdict: go-pty works, but we don't adopt it (D12).** Phase 1 builds its own thin wrapper on `x/sys/windows`, with `creack/pty` on Unix. go-pty v0.2.2 has four problems:
1. It leaks the `CreateProcess` process handle on every start (`cmd_windows.go`: it calls `os.FindProcess` and never closes `pi.Process`).
2. `Close` closes the pseudo console and both pipes at once, so unread output is lost.
3. It closes the thread handle. To join a job before the shell runs, the spike had to find the thread again with a Toolhelp snapshot.
4. If `Cmd.Env` is nil it uses the parent environment (an SF-04 risk). It also pins `x/sys` v0.16.0.

**Gotchas for Phase 1:**
- **Close order:** `ClosePseudoConsole` first, then drain the output pipe to EOF, then close the pipes. Keep reading even after the socket dies, because ConPTY can block on a full output pipe.
- **Ctrl+C:** a shell inherits the "ignore Ctrl+C" flag. The spike clears it once with `SetConsoleCtrlHandler(NULL, FALSE)`. The side effect is that Ctrl+C in `ygg`'s own console then stops `ygg`, so decide this in Phase 1.
- **`.mjs` MIME type:** Windows' registry often maps `.mjs` to `text/plain`, so register `text/javascript` (relevant to `cmd/ygg` too, if Vite emits `.mjs`).
- **Locale:** the tests match English console output (`mode con`, `choice`) and were run on Windows 11 22631 only. 24H2 changed when closing a console blocks.
- **Security:** the Host check plus WebSocket Origin check stops browsers, but any local process can open a shell. DV-04's single-use socket token is required in Phase 5. Serve static assets from an explicit allowlist rather than `http.Dir`, which doesn't block Windows device names such as `CON`.

**Phase 1 wrapper:** `CreatePseudoConsole` + `PROC_THREAD_ATTRIBUTE_PSEUDOCONSOLE` and `PROC_THREAD_ATTRIBUTE_JOB_LIST` in one attribute list, then `CreateProcess`. Close every handle. Require the environment from `internal/safety`; nil is not allowed. Use the close order above. B1's `PTY` interface carries over.

---

## B2 — Job objects (`spikes/jobobject`) · PL-02, PL-05

**Setup.** Jobs with `JOB_OBJECT_LIMIT_KILL_ON_JOB_CLOSE`, a non-inheritable handle, and nested per-task jobs. Four ways of getting children in were tested:

| Approach | How | Race-free | Notes |
|---|---|---|---|
| self | `ygg` assigns itself; children inherit | yes | Editors then need `BREAKAWAY_OK` on the main job (see the decision below) |
| perchild | `CREATE_SUSPENDED` → assign → resume (os/exec + Toolhelp) | yes | os/exec hides the thread handle |
| createprocess | raw `CreateProcess` suspended → assign → `ResumeThread` | yes | Keeps the thread handle |
| joblist | `PROC_THREAD_ATTRIBUTE_JOB_LIST` (Windows 10+) | yes | In the job from creation, with no suspend/resume |

| Check | Status |
|---|---|
| Kill the `ygg` stand-in: child and grandchild die (all four approaches) | auto |
| Nested task job: cancelling the context kills only that task's tree | auto |
| Outside-job process survives `ygg` | auto (breakaway or broker) |
| Kill the demo from Task Manager: children die, Notepad stays open | manual |

**Breakaway (PL-05):** `CREATE_BREAKAWAY_FROM_JOB` only leaves jobs that allow it. Under an outer job without `BREAKAWAY_OK` (some terminals, IDEs, CI), `CreateProcess` succeeds but the process silently stays in that outer job. The spike's fallback starts the process with Explorer as its parent (`PROC_THREAD_ATTRIBUTE_PARENT_PROCESS`). Only automated runs inside an outer job were tested; running with no outer job at all is untested.

**Decision for Phase 1 (lead):**
- **joblist / createprocess, not self-assign.** If `ygg` sits in its own job, editors need `BREAKAWAY_OK`. But then any agent or build process could break away and survive `ygg` (invariants 5 and 11). With per-child assignment `ygg` stays outside its jobs, and nothing in them may break away.
- **One main job (`KILL_ON_JOB_CLOSE`, no `BREAKAWAY_OK`), with a nested job per task.** Cancelling a task terminates its job.
- **Editors and Explorer (PL-05)** start outside every `ygg` job. Inside a foreign outer job, prefer asking Explorer to launch them through COM (`IShellDispatch2.ShellExecute`) over parent-process spoofing, which antivirus/EDR tools often flag. Test against Defender.
- **Every spawn uses the clean environment**, including editors. The spike's nil-env default must not carry over.

---

## B3 — Pure-Go SQLite (`spikes/sqlite`) · IN-03, D4

**Setup.** `modernc.org/sqlite` v1.60.1 (driver name `"sqlite"`), goose v3.29.0 with embedded migrations, sqlc v1.31.1 pinned as a `tool` directive in `go.mod` (it runs without cgo, using `wasilibs/go-pgquery` + wazero). Two tables: `projects` and `repos` (FK, `ON DELETE CASCADE`, `STRICT`). Go 1.26.

| Check | Status |
|---|---|
| Insert/select through sqlc, pragmas as configured | auto |
| FK enforcement | auto |
| DB path with spaces; a path containing `?` is refused | auto |
| Concurrent writers, also under `-race` (`task spikes:test:race`, gcc from WinLibs) | auto |
| Migrations up twice is a no-op; down works | auto |
| `go version -m` on the built `cmd/demo` shows `CGO_ENABLED=0` (modernc.org/sqlite v1.60.1, libc v1.78.0) | done (manual) |

`task spikes:test` and `task spikes:test:race` both pass for all four spikes on Windows (run by the human, 2026-10-15).

**Connection setup.**
- Pragmas on every connection (DSN `_pragma`): `busy_timeout(5000)`, `journal_mode(WAL)`, `foreign_keys(1)`, `synchronous(NORMAL)`.
- Two pools: one write connection (`_txlock=immediate`) and a read pool of `max(4, NumCPU)` with `_query_only=1`. Connections are never recycled.
- Goose uses `goose.NewProvider(goose.DialectSQLite3, …)` over the embedded `migrations/*.sql`, not the global `SetDialect`.
- sqlc: engine `sqlite`, schema read from the goose files (Down sections skipped), `emit_empty_slices: true`.

**Gaps for Phase 1:**
- `task test:race` covers the root module only; `task spikes:test:race` covers the spikes. Both need cgo and gcc (on Windows: `winget install BrechtSanders.WinLibs.POSIX.UCRT`, then a new terminal). Phase 1 CI needs the same.
- Wire the exact sqlc invocation into the Phase 1 `task generate`.
- Not shipped, so `task license` doesn't cover the spike's dependencies. The ones opened (modernc.org/sqlite, sqlc, wazero, pgx) are BSD, MIT or Apache-2.0; the rest weren't checked.

---

## B4 — Agent streaming (`spikes/agentstream`) · AG-01, AG-02, AG-21, AG-22

**Part 1 (harness, no CLI calls): done. Part 2 (live run, 2026-10-15, CLI 2.1.299, `--model haiku`): done. All checks pass. 8 runs cost about $0.04 in total.**

**Setup.** One module, one dependency (`golang.org/x/sys`). The CLI has `run`, `hook` and `scrub`. `run` starts `claude -p --output-format stream-json --verbose --forward-subagent-text --settings F --setting-sources project --permission-mode dontAsk [--model M] [--max-budget-usd N] <prompt>` in a scratch git repo it creates in a temp dir.
- **Process:** a job per run (`KILL_ON_JOB_CLOSE`, no breakaway), suspended start → assign → resume. Cancelling the context terminates the job. The environment goes through `CleanEnv`, which is never nil and refuses secrets and tokens.
- **Events:** a draft normalized `Event` (init, assistant, user, tool_use, `task_started`, result, usage/cost). `ParentID` comes from `parent_tool_use_id`, `AgentID` from `agent_id`.
- **Hook:** the `--settings` file registers `agentstream hook` in exec form (`command` + `args`, no shell) on `PreToolUse`, `PostToolUse`, `PostToolUseFailure` (matcher `*`), `SubagentStart`, `SubagentStop` and `WorktreeCreate`. It logs every call under a lock file. On `PreToolUse` it denies a sub-agent call that sets any `isolation` (`worktree` or `remote`), is deeper than the depth cap, or would exceed the concurrency cap. `WorktreeCreate` always fails, which blocks agent-native worktrees, including ones set in a sub-agent's frontmatter that `PreToolUse` can't see. The enforcing hooks set `onFailure: "block"`; the other hooks only log and never exit 2. Deny modes: `exit2` (default), `json`, `json-legacy`.
- **Env:** depth and concurrency caps, plus `CLAUDE_CODE_DISABLE_BACKGROUND_TASKS=1` so sub-agents run in the foreground. `--hook-max-depth` and `--hook-max-concurrent` set the hook's caps apart from the env vars, so the live run can test each one alone.
- **Tests:** synthetic JSONL for the parser and hook, env and settings checks, and a Windows test that cancels a fake helper and checks the tree is gone. Nothing runs `claude`.

| Check (live run, part 2) | Status | Evidence |
|---|---|---|
| (a) `parent_tool_use_id` on sub-agent messages | pass | Every `user`/`assistant` line of the sub-agent has `parent_tool_use_id` = the `Agent` `tool_use` id and `agent_id` = the `task_started` `task_id`; main-agent lines have `null` (`live/subagent-read.jsonl`). |
| (b) `PreToolUse` fires for sub-agent tool calls | pass | Hook log: `PreToolUse Read agent_id=<sub-agent>` between `SubagentStart` and `SubagentStop` (`live/subagent-read.hook.jsonl`). |
| (c) depth and concurrency caps respected | pass (env and hook, each alone) | Env only, concurrency 1: the 2nd parallel call gets `Concurrent subagent limit reached. You can run 1 subagents at once.` Env only, depth 1: the sub-agent isn't given the `Agent` tool at all (its `ToolSearch` finds none). Hook only (CLI defaults): `deny At most 1 sub-agents may run at once` and `deny Sub-agent nesting is limited to 1 levels` (depth 2 from `agent_id`). |
| (d) `isolation: worktree` blocked | pass (`exit2`) | On the call: `PreToolUse Agent iso=worktree deny`, and the model reports the hook's reason. In an `--agents` definition (PreToolUse sees no isolation): `WorktreeCreate deny`, then `PostToolUseFailure Agent`, and the model gets `WorktreeCreate hook failed: …`. `git worktree list` shows only `main`. The JSON deny modes weren't needed and weren't run. |
| (e) cancel kills the tree (real CLI) | pass | The sub-agent ran `ping -n 120` through Bash, and the harness timeout cancelled it at 50 s (`cancelled: true`). Checked within the 2 minutes ping would have run: no `PING.EXE`, and no process created during the run is alive. The only claude/git processes left belong to the lead's own Claude Code session. |

**Live-run findings for Phase 1:**
- **8.3 short paths break the permission check.** `TEMP` is in 8.3 short form (`C:\Users\DEVELO~1\…`-style) when the user name is long. With the scratch repo under it, `dontAsk` denied the sub-agent's `Read` of `README.md` in its own working directory (`system/permission_denied`, `live/dontask-shortpath-denied.jsonl`). The same run with a long-form `TEMP` passed. `internal/platform` must expand worktree paths to long form (`GetLongPathNameW`) before using them as an agent's cwd or in any path it is given.
- **End the options with `--` before the prompt.** A variadic flag (`--allowedTools <tools...>`) took the prompt as one of its values, and the CLI exited with "Input must be provided". `Args` now puts `--` before the prompt.
- **A hook denial shows the model the full hook command line**, exe path and args included (`PreToolUse:Agent hook error: [<exe> hook --log <path> …]: <reason>`). It also ends up in the stream and the transcript. Phase 1 should keep paths and settings out of the hook args: pass a short id, and read the rest from a file or env var.
- **The stream carries account data:** `init` (MCP servers, plugins, skills, memory paths), `system/commands_changed` (every command, MCP prompts included) and `rate_limit_event` (account usage). The adapter must not log or audit these raw. `scrub` now blanks them.
- **Line types seen:** `system/{init, commands_changed, task_started, task_progress, task_updated, task_notification, thinking_tokens, permission_denied}`, `assistant`, `user`, `rate_limit_event`, `tool_progress`, `result/success`. On `permission_denied`, `message` is a plain string. That crashed the parser and is now fixed.
- **Hook input keys seen:** `session_id`, `transcript_path`, `cwd`, `prompt_id`, `permission_mode`, `effort`, `hook_event_name`, `tool_name`, `tool_input`, `tool_use_id`, plus `agent_id`/`agent_type` inside a sub-agent and `tool_response`/`duration_ms` on PostToolUse. SubagentStop also has `agent_transcript_path`, `last_assistant_message`, `background_tasks` and `session_crons`. Agent ids look like `a` followed by 16 hex digits.
- **`dontAsk` allows the `Agent` tool.** With `--allowedTools`, `Bash(ping:*)` was allowed for the sub-agent.
- Manual runs only: Windows PowerShell 5.1 splits a native argument at embedded `"`, which cut one prompt short. Go's own escaping wasn't involved.

**Fixtures:** `testdata/live/*.jsonl` are scrubbed captures, one per run. `*.hook.jsonl` are the hook logs of the same runs. `TestParseLiveFixtures` parses every one without errors and checks the sub-agent links. The checks after scrubbing found no username, home path, `C:\Users\<real>`, machine or domain name, non-placeholder UUID or real agent id. The hand-written `testdata/*.jsonl` stay as edge cases.

**Checked against the docs (2026-10-15, CLI 2.1.299).** Sources: `claude --help`, and the Claude Code docs pages `hooks`, `sub-agents`, `env-vars`, `settings`, `headless` and `agent-sdk/typescript` on code.claude.com/docs/en/. The pages show no version or date.
- **Flags:** all in `--help`: `-p`, `--output-format stream-json`, `--verbose`, `--forward-subagent-text` (works only with `-p` and stream-json), `--settings <file-or-json>`, `--model`, `--max-budget-usd` (`-p` only), `--permission-mode` (`acceptEdits`, `auto`, `bypassPermissions`, `manual`, `dontAsk`, `plan`). `-p` says that settings files that fail validation are silently ignored, so the live run must confirm the hook log fills up.
- **Env vars:** `CLAUDE_CODE_MAX_SUBAGENT_SPAWN_DEPTH` sets the layers below the main conversation (default **3**; `1` turns nesting off). `CLAUDE_CODE_MAX_CONCURRENT_SUBAGENTS` sets how many sub-agents can run at once (default **20**; past it the error is `Concurrent subagent limit reached`). Both need 2.1.217 or later and take a positive integer only, so they can lower a cap but can't remove it. The old defaults (2 and 4) were wrong.
- **Hook shape:** `hooks.<Event>[].{matcher, hooks[].{type, command, args, timeout, onFailure}}`. Matcher `*`, `""` or no matcher means all tools. The sub-agent tool is **`Agent`** (renamed from `Task` in 2.1.63; `Task` stays an alias, and the init `tools` list still says `Task`). Hooks from settings files fire for a sub-agent's tool calls too. On Windows, shell form runs in Git Bash, or in PowerShell if Git Bash is missing. Exec form (`args` set) has no shell, so it's used now.
- **`--settings` merge:** it merges with the other files. It sits above project and user settings, a key overrides, lists combine, and "hook entries merge across settings levels". So the user's own hooks would run too; the harness passes `--setting-sources project` to keep them out. Without `--bare`, `-p` still loads `~/.claude` CLAUDE.md, plugins and MCP servers. `--bare` needs `ANTHROPIC_API_KEY` and doesn't fit a subscription login.
- **Deny:** exit 2 blocks the call, and stderr is the reason the model sees. JSON is `hookSpecificOutput.{hookEventName, permissionDecision: "deny", permissionDecisionReason}` with exit 0. Top-level `decision: "block"` is deprecated for PreToolUse. Exit 2 blocks even when the stdout JSON says allow. **Any other non-zero exit, a timeout or a hook that won't start lets the call through**, unless `onFailure: "block"` is set (2.1.295 or later). That flag is now on the enforcing hooks.
- **Hook input:** `session_id`, `transcript_path`, `cwd`, `hook_event_name`, `tool_name`, `tool_input`, `tool_use_id` (and `tool_response` on PostToolUse), plus `agent_id` and `agent_type` inside a sub-agent. **There is no `parent_tool_use_id` in hook input**, so the hook can only tell depth 1 from "2 or more". The depth env var is exact. Phase 1 can map `agent_id` to depth from the stream's `task_started` (`task_id` equals `agent_id`; it also has `tool_use_id` and `spawn_depth`).
- **Agent input:** `description`, `prompt`, `subagent_type?`, `model?`, `effort?`, `run_in_background?`, `name?`, `isolation?: "worktree" | "remote"`. PostToolUse on `Agent` fires when a foreground sub-agent completes (`status: "completed"`). A background one returns at once with `status: "async_launched"`, which is why background tasks are turned off.
- **Stream:** sub-agent `assistant` and `user` messages carry `parent_tool_use_id` (the Agent `tool_use` id), at every depth since 2.1.219. Main-agent messages carry `null`. Messages also carry `agent_id`. The first message of a foreground sub-agent is a `user` message with its prompt. `--forward-subagent-text` adds text and thinking blocks. `result` has `subtype`, `is_error`, `result`, `total_cost_usd`, `usage`, `num_turns`, `duration_ms` and `permission_denials`.
- **Not documented:** whether the CLI accepts an empty `--setting-sources`, so `project` is used, which loads nothing from an empty scratch repo. The `agent_id` format was seen in the live run (above).

**Done after the review:** `--model` defaults to `haiku` (`--max-budget-usd` defaults to 0.10); the scratch repo's `git` calls run through `startInGroup`; the scratch author is `dev@example.com`.

---

## Wave 2 safety review (B3, B4 part 1, B6)

**Verdict: PASS, no blocking items.** A read-only reviewer read the code; it didn't run tests or builds. Should-fix items: B4 model, scratch `git` job and scratch email are fixed; the B3 race task is added and passes; the env var sources were checked later (see B4).

- Passed: no cgo; no personal data, real hosts or tokens in scope (only fictional values); clean env on every agent spawn; job always attached to the agent, no breakaway; the hook denies `isolation: worktree` and fails closed; tests never run `claude`; `RemoveAll` only on the harness's own temp dir; no binaries committed (`bin/` ignored).
- **B6 deviation:** `fixtures/demo-gen` refuses a non-empty `-out` and never deletes, where the brief said it replaces. Safer, but a second `task fixtures` fails until `fixtures/demo-project` is removed by hand.
- The demo repos' `origin` is an absolute local path, so the generated `.git/config` holds the user's real path. The output is gitignored.
- Test-only helpers in `agentstream` (`setup_test.go`, `main_test.go`) spawn `git` or the test binary without a job or clean env; acceptable for tests.

---

## Manual checklist (human, Windows)

**B1 terminal:**
1. Run `pnpm -C web install --frozen-lockfile`, then `go run .` in `spikes/conpty`, and open `http://127.0.0.1:4848/`.
2. PowerShell: type commands, use the arrow keys, Ctrl+C a running `ping -t 127.0.0.1`. Expected: the ping stops and the shell stays.
3. Resize the browser window, then check `$Host.UI.RawUI.WindowSize`. Expected: it matches the new size.
4. Run `Read-Host`, type an answer. Expected: it's echoed back.
5. Run `Write-Host -ForegroundColor Green "é世😀"`. Expected: green text with correct glyphs.
6. Run `exit 3`. Expected: `[process exited with code 3]`.
7. Repeat steps 2–6 with `?shell=cmd`, using `mode con` and `choice /c yn`.
8. In cmd, run `start /b ping -n 300 127.0.0.1`, note the `cmd.exe`, `PING.EXE` and `conhost.exe` PIDs in Task Manager, then close the tab. Expected: all three are gone within about 1 s.
9. Ctrl+C the server. Expected: no spike `cmd.exe`, `powershell.exe` or `conhost.exe` is left.

**B2 job objects:**
1. In `spikes/jobobject`, run `go run ./cmd/demo` and note the printed PIDs.
2. In Task Manager → Details, end the `demo.exe` process.
3. Expected: every printed PID is gone within about 2 s, and the Notepad window stays open. On Windows 11 Notepad may hand off to the Store app; the visible window should survive.
4. Repeat with `-approach perchild`.
5. Close Notepad yourself.
6. After `go test` in either spike, no `*.test.exe` is left in Task Manager.
