# AGENTS.md

Instructions for coding agents (and humans) working on the **Yggdrasil** codebase.

Read **docs/PRD.md** for what we're building and **docs/PLAN.md** for the current phase before starting work. Requirement IDs (e.g. `AG-07`, `SF-06`) come from docs/PRD.md; reference them in commits and PRs.

> This file lives in `docs/`, not the repo root. Claude Code loads it through `.claude/CLAUDE.md`. Other coding agents that only look for `AGENTS.md` at the root (e.g. Codex, OpenCode) must be pointed to `docs/AGENTS.md` explicitly.

---

## 1. What this project is

Yggdrasil is a local server with a browser UI that combines a multi-repo workspace (git status, builds, dev servers, MRs, pipelines) with code agents running in isolated worktrees. It is a **Go** backend with a **React + TypeScript** frontend embedded into a single binary. It runs on `127.0.0.1`, stores state in SQLite, and runs agents through pluggable adapters.

**Naming:** the product is "Yggdrasil"; the binary, process and command are always **`ygg`**. Never name a binary, Windows service or process `yggdrasil`, `yggd`, `yggctl` or `yggdev`: those belong to other software (docs/PRD.md section 11.1). Package names and UI labels stay descriptive (`internal/proposals`, "Review"), not Norse.

## 2. Stack

| Area | Choice |
|---|---|
| Backend | Go (version pinned in `go.mod`), `net/http` (or `chi`), `slog` |
| Realtime | WebSocket via `coder/websocket` |
| Database | SQLite via `modernc.org/sqlite` (pure Go), `sqlc` queries, `goose` migrations |
| Processes | `os/exec` + Windows job objects (`golang.org/x/sys/windows`) |
| Terminal | `aymanbagabas/go-pty` (server; fallback: thin wrapper on `x/sys/windows` + `creack/pty`) + xterm.js (browser) |
| API contracts | `oasdiff`, `gotreesitter` (pure Go) |
| MCP | Official Go MCP SDK |
| Frontend | React, TypeScript (strict), Vite, TanStack Query, TanStack Router, Zustand, Tailwind, Radix, cmdk, react-grid-layout |
| Shared types | Go structs → TypeScript via `tygo` |
| Tasks | Taskfile (`task <name>`) |
| Package manager (frontend) | pnpm |

**No cgo, ever.** `CGO_ENABLED=0` must always build. Don't add a dependency that needs cgo.

**License: Apache-2.0.** Every dependency (Go and npm) must have a compatible license: MIT, BSD, ISC, Apache-2.0 or MPL-2.0. No GPL, LGPL or AGPL. New source files don't need license headers; the root `LICENSE` and `NOTICE` cover them.

## 3. Commands

> Target commands. Update this section if the actual tasks differ.

| Task | Command |
|---|---|
| Build everything (frontend + embed + binary) | `task build` |
| Run the built binary | `task run` (or `./bin/ygg`) |
| Dev mode (Go server + Vite dev server with proxy) | `task dev` |
| Check prerequisites | `ygg doctor` |
| Generate code (sqlc, tygo) | `task generate` |
| Go tests | `task test` (runs `go test ./...`) |
| Frontend tests | `task test:web` |
| UI end-to-end tests | `task test:e2e` |
| Lint (golangci-lint + ESLint) | `task lint` |
| Type check frontend | `task typecheck` |

Before finishing any change: run `task generate`, `task lint`, `task typecheck`, `task test` and `task test:web`. Do not mark work done if any of them fail or if generated files are out of date.

## 4. Repository layout

```
cmd/
  ygg/               # main: wires config, server, embedded web assets; `doctor` subcommand
internal/
  core/              # data model: projects, repos, worktrees, tasks, change sets, proposals
  db/
    migrations/      # goose migrations (never edit an applied one)
    queries/         # SQL for sqlc
    gen/             # sqlc output (generated, do not edit)
  api/               # HTTP handlers, WebSocket hubs, Host/Origin checks, auth (remote mode)
  git/               # git CLI wrapper — safe operations only
  hosts/             # gitlab/, github/, gitea/ clients
  builds/            # build queue, presets, logs, artifacts, verification runner
    toolchain.go     # Toolchain interface: Detect, Commands, Artifacts, optional Order
    toolchains/
      node/  maven/  gradle/  golang/  dotnet/  python/
      custom/        # user-defined commands for any other ecosystem
  devservers/        # dev servers, port allocation, terminal sessions
  agents/
    adapter.go       # Adapter interface, normalized Event types, Capabilities
    claudecode/      # reference adapter
    opencode/        # reference adapter
    codex/  gemini/  # community-maintained adapters
    mock/            # replays recorded event streams (testdata/*.jsonl)
  safety/            # clean env, path guard, command allowlist, Ask-mode integrity check
  proposals/         # proposal lifecycle: create, confirm, reject, execute, audit
  audit/             # audit log writer
  platform/          # ALL OS-specific code (build tags: _windows.go, _darwin.go, _linux.go)
  mcp/               # MCP server tools
  contracts/         # XR-02 API contract checks (oasdiff + gotreesitter)
  scheduler/         # scheduled agent tasks
  notify/            # notification events for in-app and browser notifications
web/
  src/
    api/             # typed client; generated types from tygo live in src/api/types.gen.ts
    routes/          # TanStack Router routes
    widgets/         # one folder per widget module
    components/      # shared UI components
  dist/              # build output, embedded by cmd/ygg (not committed)
fixtures/
  demo-project/      # mock repos for first run, tests and screenshots (generated by a script)
docs/                # ALL Markdown docs except README.md
  PRD.md             # requirements and decision log
  PLAN.md            # phases and checklists
  AGENTS.md          # this file
  PROMPTS.md         # prompts for running the build with Claude Code
  CHANGELOG.md
  CONTRIBUTING.md
  phases/            # per-phase work breakdowns (phase-<n>.md)
  threat-model.md
  writing-an-adapter.md
  writing-a-toolchain.md
  spikes.md
.claude/
  CLAUDE.md          # imports docs/AGENTS.md + lead workflow for Claude Code
  settings.json
  agents/            # Claude Code subagent definitions
README.md            # the only Markdown file at the root
LICENSE              # Apache-2.0
NOTICE
Taskfile.yml
```

**Docs location rule:** new Markdown documentation goes in `docs/`. The only Markdown at the repo root is `README.md`. Claude Code configuration (`.claude/CLAUDE.md`, `.claude/agents/*.md`) stays in `.claude/` because that's where Claude Code looks for it.

## 5. Architecture rules

These are not style preferences. Breaking them is a bug.

### 5.1 Data model
- Every feature attaches to **Project → Repo → Worktree → Task → Change set**. If a new feature doesn't fit, discuss before adding a new top-level concept.
- `internal/core` holds domain types and logic and must not import `api`, `web` or any adapter package.

### 5.2 Platform package
- **All OS-specific behaviour lives in `internal/platform`**, split by build tags: path handling, shell and wrapper resolution (`mvnw.cmd` vs `./mvnw`), job objects and process-tree kill, PTY, port detection, opening editors and folders.
- No `runtime.GOOS` checks outside `internal/platform`.
- Use `path/filepath`. Never hardcode `\` or `/` as separators, never build paths by string concatenation.
- Windows is the supported platform. Darwin and Linux implementations may be minimal, but must compile and pass `go vet`.

### 5.3 Processes
- **Every** child process (git, builds, dev servers, terminals, installs, agents) is started through `platform.Start` (or the equivalent helper), which:
  1. uses the clean environment from `internal/safety` (never `os.Environ()` directly),
  2. attaches the process to Yggdrasil's job object on Windows,
  3. ties it to a `context.Context` so cancelling a task stops its processes.
- Never call `exec.Command` directly outside `internal/platform`.

### 5.4 Concurrency
- Long-running work (agent runs, builds, dev servers, log streaming) runs in goroutines owned by a `context.Context`. Every goroutine has a clear owner and stops when its context is cancelled.
- Use `errgroup` for groups of related goroutines. No fire-and-forget goroutines.
- Shared state is protected by a mutex or owned by a single goroutine; run tests with `-race`.

### 5.5 Agent adapters
- Each agent CLI is an adapter implementing the `Adapter` interface in `internal/agents/adapter.go`.
- Adapters convert the CLI's output into normalized `Event` values. **No core, API or UI code may depend on a specific adapter's output format.**
- Adapters declare `Capabilities` (resume, cost reporting, plan limits, permission hooks, enforcement level, sub-agents: none/flat/nested, budget cap). Code checks capabilities rather than assuming features.
- **Agent-native sub-agents** (AG-21, AG-22): every `Event` has an optional `ParentID`; adapters fill it from the CLI's own marker (Claude Code: `parent_tool_use_id`, with `--forward-subagent-text` passed). Adapters apply the task's sub-agent depth and concurrency caps, and must block agent-native worktree isolation for sub-agents (Claude Code `isolation: worktree`). Sub-agent usage is added to the task's usage.
- Adapters check their own prerequisites (`Check(ctx)`) and return plain-language errors.
- Every adapter has recorded fixtures in `testdata/` and tests that parse them.
- **Reference adapters** (Claude Code, OpenCode) are tested against the real CLI before every release, and their fixtures are re-recorded when the CLI's output changes. **Community adapters** (Codex, Gemini) rely on their fixtures in CI; a breaking CLI change may disable them with a clear message until a contributor fixes them.
- UI and log wording stays agent-neutral ("agent"), except inside that adapter.

### 5.6 Sub-tasks
- Sub-tasks (AG-23 to AG-26) are ordinary tasks with a `ParentTaskID`. Don't create a separate entity for them.
- **One level only.** A task with a parent can never create sub-tasks; the scheduler rejects it, and `propose_subtasks` is not offered to child tasks.
- A child never gets more than its parent: same or narrower repo scope, same or more restricted mode, its share of the parent's budget.
- Children go through the same `maxAgents` queue as every other task. Dependencies between siblings must be acyclic; validate on creation.
- Agents can only **propose** a split; starting sub-tasks always needs user approval (directly, or by approving a plan that contains the split).

### 5.7 Toolchain modules
- Each language ecosystem is a module in `internal/builds/toolchains/<name>/` implementing the `Toolchain` interface in `internal/builds/toolchain.go`: `Detect`, default `Commands` (install, verify, build, dev server), `Artifacts`, and optionally `Order` for cross-repo build ordering.
- Detection reads files only (lock files, manifests, `pyproject.toml` sections). It never runs commands.
- The default **verify** command is the fast chain from docs/PRD.md section 6.3. Each step runs only if the repo has it configured; the chain stops at the first failure. Don't add steps that need a database or network services to the default chain.
- Commands run through the project's own runner (`mvnw`, `gradlew`, `uv run`, `poetry run`, the venv's Python), resolved by `internal/platform`.
- Every repo can override any command, and the `custom` module covers ecosystems without a module.
- Modules don't import each other. A repo can match more than one module.

### 5.8 API and shared types
- API request/response types and `Event` types are Go structs with JSON tags; `task generate` produces the TypeScript types. Never hand-edit generated files.
- Breaking a shared type must break the frontend build, not fail at runtime.

### 5.9 Notifications
- At v1, notifications are in-app plus browser notifications (Web Notifications API). Don't add external channels (webhooks, ntfy, chat apps, email) before v1.
- Notification text contains only the task name and status: no code, diffs, file paths or repo contents.

### 5.10 Frontend
- Server state goes through TanStack Query; UI-only state in Zustand. No duplicating server data in stores.
- Each widget is a self-contained module in `web/src/widgets/<name>/` implementing the shared widget interface. Widgets must not import from each other.
- Accessible primitives (Radix) for menus, dialogs, popovers. Core flows must work with the keyboard.

## 6. Safety invariants

Yggdrasil's main promise is that it is safe to let agents work. **Never weaken these, even if a task seems to ask for it.** If a change touches this area, say so explicitly in the PR description.

1. **Never implement** force push, `git reset --hard`, branch deletion, or deleting files on disk. Not behind a flag, not as an "advanced" option. (`propose_delete_files` moves files into a `_to_delete/` folder for the user; it never deletes.)
2. **Every mutation of a main checkout or remote is a proposal** that requires UI confirmation: commit, push, MR, install, publish, agent-file changes, file removal.
3. **Tokens never reach child processes.** Every spawned process uses the clean environment (see 5.3).
4. **Tokens never appear in URLs, git config, logs or the audit log.** Use the redaction helper in `slog` handlers.
5. **Enforcement happens in Yggdrasil**, not only through agent permission flags: writable paths limited to the task's worktree(s), commands checked against the allowlist.
6. **Only tracked repos are visible** to agents and MCP. Don't add any path that can reach outside them.
7. **Bind to `127.0.0.1` by default** and keep Host/Origin checks. Remote access mode (RA-*) must require login and explicit configuration.
8. **Everything that changes state is written to the audit log.**
9. Ask mode must never change a working tree; the integrity check (SF-07) must stay in place.
10. **No agent runs in an untrusted repo outside Ask mode.** If a repo ships agent hooks or MCP servers (e.g. `.claude/settings.json`, `.mcp.json`), the user must trust it first (SF-09).
11. **Sub-agents and sub-tasks never widen access.** Sub-agents stay in their task's worktree under the same guard; sub-tasks get the same or narrower scope, mode and budget than their parent, and never start without user approval.

## 7. Coding conventions

### Go
- `gofmt` and `golangci-lint` clean. Errors are wrapped with context (`fmt.Errorf("start build %s: %w", id, err)`); no panics outside `main`.
- Errors shown to users are plain language and say what to do next; keep a separate user-facing message from the wrapped internal error.
- Small packages with one responsibility. Interfaces are defined where they are used, kept small.
- Database access only through sqlc-generated queries. Schema changes only through new goose migrations.
- Don't add a dependency without a reason in the PR, and confirm it is pure Go and has a compatible license (section 2).

### TypeScript
- Strict mode. No `any` without a comment explaining why.
- Components small and typed; no business logic in components that belongs on the server.

## 8. Testing

- **Never test against real repos or real hosts.** Use temporary git repos created by test helpers (`t.TempDir()`), and `fixtures/demo-project/`.
- Agent behaviour is tested with the **mock adapter** replaying recorded event streams. Tests must not call real agent CLIs or model APIs. Record at least one fixture with nested sub-agents, and test sub-task rules (one level only, no wider scope or mode, acyclic dependencies, shared queue).
- Toolchain detection is tested with small fixture repos per ecosystem (manifests and lock files only). Tests that run a real toolchain (e.g. `uv sync`, `dotnet build`) skip themselves when that toolchain isn't installed; the Windows CI runner installs all of them.
- Git host clients are tested with recorded HTTP fixtures (`httptest.Server`).
- Every safety invariant in section 6 has at least one test that fails if it's broken.
- Run Go tests with `-race`.
- Include Windows-specific cases: paths with spaces, long paths, drive letters, CRLF files, locked files during worktree removal, child processes killed when the job object closes.

## 9. Windows gotchas

- Long paths: set `core.longpaths=true` for Yggdrasil's git calls; keep worktree directory names short.
- File locks: removing a worktree can fail while a dev server, editor or antivirus holds files. Detect the error, explain it, offer retry.
- Process trees: killing a parent doesn't kill children on Windows. Rely on the job object, not on killing PIDs one by one.
- Outliving processes: editors and the file explorer opened by Yggdrasil must be started **outside** the job object (PL-05). Otherwise closing Yggdrasil closes the user's editor.
- Executables: resolve `.cmd`/`.bat`/`.exe` wrappers through `internal/platform` (e.g. `mvnw.cmd`, `gradlew.bat`, `npm.cmd`).
- Python venvs: the interpreter is `.venv\Scripts\python.exe` on Windows and `.venv/bin/python` elsewhere; resolve it through `internal/platform`. The clean environment must drop `VIRTUAL_ENV`, `CONDA_PREFIX` and `PYTHONPATH` from Yggdrasil's own environment.
- Line endings: don't let agent edits flip entire files between LF and CRLF.
- Terminal: ConPTY behaves differently from Unix PTYs (resize, exit codes, signals); test the terminal dock on Windows. Keep the PTY behind `internal/platform` so `go-pty` can be swapped for the fallback wrapper without touching callers.
- `\\wsl$` paths are unsupported at v1; detect and refuse with a clear message.

## 10. Mock data and privacy

- All fixtures, screenshots, docs and examples use **fictional** projects, repos, people, hosts and commit messages (e.g. project `acme-shop` with repos `shop-api`, `shop-web`, `shop-admin`).
- Never commit real hostnames, usernames, file paths from a real machine, tokens, or client data.

## 11. Commits and PRs

- Conventional commits: `feat(agents): add plan mode (AG-17)`.
- One logical change per PR. Reference PRD IDs.
- Commit generated code together with its source change.
- Update docs/PLAN.md checkboxes when a task is done.
- Update `docs/CHANGELOG.md` for user-visible changes.
- Update docs/PRD.md if a requirement changes; don't silently drift from it.

## 12. When unsure

- If a task conflicts with a safety invariant, stop and ask rather than finding a workaround.
- If a requirement is ambiguous, check the docs/PRD.md decision log and open questions, then ask.
- Prefer the smallest change that satisfies the requirement.
