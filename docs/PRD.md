# Yggdrasil — Product Requirements Document

> Status: Draft v0.5 · 2026-10-14 (sub-agents and sub-tasks added; see section 12)
> Product name: **Yggdrasil** (formerly "repo-hub") · Command: **`ygg`**
> License: **Apache-2.0**

---

## 1. Summary

Yggdrasil is a local, open-source control plane for developers who work across many repositories. It combines a **multi-repo workspace** (status, git, builds, dev servers, merge requests, pipelines) with **code agents** that do work in isolated worktrees, under rules that Yggdrasil enforces itself.

The two halves are one product, connected in both directions:

> **Everything you can see, you can hand to an agent. Everything an agent does shows up where you already look.**

It runs as a local server with a browser UI on `127.0.0.1`. Nothing is hosted.

## 2. Problem

Developers on multi-repo projects (backend services + frontends + shared libraries) juggle:

- Status spread across many repos: which are dirty, behind, failing CI, have open MRs.
- Builds and dev servers that must run in a particular order.
- Code agents that are designed around **one repo per task**, while real changes often span several repos (an API change plus the frontend that calls it).
- Agent tools whose safety depends on the agent behaving, which is hard to trust when several agents run at once.

Existing parallel-agent tools focus on single-repo worktree workflows and are often macOS-only. Multi-repo workspaces with self-hosted GitLab are poorly served.

## 3. Target users

**Primary:** Individual developers working on projects made of many repos, often on self-hosted GitLab, often with mixed stacks (Java/Spring + Node frontends), on Windows.

**Secondary:** Developers on GitHub or Gitea, on macOS or Linux (experimental at v1), who want a local agent workflow tool that is agent-agnostic.

**Not targeted at v1:** Teams wanting a shared, hosted, multi-user service.

## 4. Product principles

1. **One product.** The workspace is the agent's context; the agent is the workspace's hands.
2. **Multi-repo first.** Every step of the agent loop works across repos, not just in one.
3. **Safety is enforced by Yggdrasil, not by the agent.** Agent permission flags are a second layer, never the only one.
4. **Nothing destructive.** Force push, `reset --hard`, branch deletion and deleting files on disk are never implemented. Risky actions are proposals that a human confirms.
5. **Local only by default.** Bound to `127.0.0.1`. No telemetry by default.
6. **Agent-agnostic.** Any supported agent CLI can run a task. Claude Code and OpenCode are the reference adapters.
7. **Honest about capabilities.** If an agent or platform can't do something, the UI says so rather than pretending.

## 5. Core concepts

| Concept | Definition |
|---|---|
| **Project** | A folder containing one or more tracked repos, treated as one unit. |
| **Repo** | A tracked git repository inside a project. Untracked folders are invisible to agents and MCP. |
| **Worktree** | A git worktree created by Yggdrasil for a task, with an auto-generated name. |
| **Task** | One unit of agent work: a prompt, a scope (repo or project), an agent, a mode, and its worktree(s). |
| **Change set** | All proposals produced by one task across repos, shipped as one unit (linked commits/MRs, merge order). |
| **Proposal** | A pending action (commit, push, MR, install, publish, file change, delete) that needs UI confirmation. |
| **Agent adapter** | A module that runs one agent CLI and converts its output into Yggdrasil's event format. **Reference** adapters are tested by the maintainer before every release; **community** adapters are covered by recorded fixtures in CI and maintained by contributors. |
| **Toolchain module** | A module that recognizes one language ecosystem in a repo (e.g. Maven, Python with uv) and supplies its default install, verify, build and dev-server commands, artifacts and, optionally, cross-repo build ordering. |
| **Session** | A resumable agent conversation tied to a task. |
| **Sub-agent** | A helper an agent CLI spawns inside its own session (e.g. Claude Code's subagents). It runs in the same worktree and under the same rules as its task; Yggdrasil shows it and counts its cost but doesn't create it. |
| **Sub-task** | A child task that Yggdrasil creates from a parent task, with its own scope, worktree(s) and agent. Its proposals join the parent's change set. |
| **Build** | An ordered sequence of build steps run by Yggdrasil, stored with logs. |
| **Dev server** | A long-running process (e.g. `npm run dev`) started by Yggdrasil in a repo or worktree. |

Data model: **Project → Repo → Worktree → Task → Change set**. Every feature should attach to one of these. A task may have a parent task (sub-tasks, one level only); this is a relation between tasks, not a new level.

### 5.1 Technology stack (decided)

Yggdrasil v1 is a **rewrite**. The personal tool stays private and serves only as a behavioural reference.

| Layer | Choice |
|---|---|
| Backend | **Go** (toolchain pinned in `go.mod`), standard library `net/http` (or `chi`), `slog` for logging |
| Realtime | WebSocket (`coder/websocket`) for agent events, logs, status and the terminal |
| Database | SQLite via `modernc.org/sqlite` (pure Go), `sqlc` for type-safe queries, `goose` for migrations |
| Git | The git CLI, called only through the `git` package |
| Processes | `os/exec` plus Windows **job objects** (`golang.org/x/sys/windows`), so every child process dies with Yggdrasil, even after a crash |
| Terminal | `aymanbagabas/go-pty` on the server (tried first in the Phase 0 spike; fallback is a thin ConPTY wrapper on `golang.org/x/sys/windows` plus `creack/pty` on Unix), xterm.js in the browser |
| MCP | Official Go MCP SDK |
| Git hosts | GitLab and GitHub Go client libraries; plain HTTP for Gitea |
| Frontend | **React + TypeScript (strict) + Vite**, TanStack Query, TanStack Router, Zustand, Tailwind, Radix primitives, cmdk, react-grid-layout, xterm.js, a diff viewer component |
| Shared types | Defined in Go; TypeScript types generated with `tygo` |
| Delivery | Frontend embedded with `go:embed` into **one binary**; Taskfile for build tasks; GoReleaser binaries after v1 |
| Tests | `go test` with temporary fixture repos; Vitest for frontend logic; Playwright for UI on Windows CI |
| API contracts | `oasdiff` (OpenAPI breaking-change checks) and `gotreesitter` (pure-Go tree-sitter) for XR-02 |
| Constraint | **No cgo.** Builds never need a C compiler (`CGO_ENABLED=0` enforced in CI). |
| License | **Apache-2.0.** All dependencies must be compatible (MIT, BSD, ISC, Apache-2.0, MPL-2.0, 0BSD, CC0-1.0, BlueOak-1.0.0, Unlicense; no GPL/AGPL). |

## 6. Scope — v1

Status legend (everything is rebuilt in the rewrite; the status says how much prior design exists): **Existing** = proven in the personal tool, rebuild with the same behaviour · **Extend** = proven, but changes are needed · **New** = not built before.

### 6.1 Projects and repos (WS)

| ID | Requirement | Status |
|---|---|---|
| WS-01 | Opt-in tracking: add/remove project folders through a folder browser, with preview and rescan. "Stop tracking" never touches files on disk. | Existing |
| WS-02 | Repo status: branch, uncommitted changes, ahead/behind, last commit, health flags. | Existing |
| WS-03 | Repo filters: All, Uncommitted, Behind, Ahead, Failed pipeline, Problems. | Existing |
| WS-04 | Projects page with tabs: Repos, Builds, Services, with live counts. | Existing |
| WS-05 | Repo page: changes, diffs, commit history, commit detail, branches. | Existing |
| WS-06 | Git actions: fetch, fetch all, fast-forward-only pull (per repo or bulk), checkout. | Existing |
| WS-07 | Cross-repo branch switch for a project; skips repos with uncommitted changes and reports which were skipped. | Existing |
| WS-08 | "Open" buttons: open repo in editor (VS Code, Cursor, IntelliJ), show file in folder. | Existing |
| WS-09 | Noise detection: when a repo shows an unusually large number of untracked files, detect likely build output or dependencies and offer to add them to `.gitignore` (as a proposal). | New |

### 6.2 Git hosts (GH)

| ID | Requirement | Status |
|---|---|---|
| GH-01 | Support self-hosted GitLab (multiple instances), GitHub, and Gitea (Gitea marked community-maintained). | Existing |
| GH-02 | MR/PR status, pipeline status, MR inbox, links to each host. | Existing |
| GH-03 | MR creation via GitLab push options (no token needed) or `propose_merge_request`. | Existing |
| GH-04 | Optional tokens in `.env` add detail; GitHub fine-grained tokens supported. Every token documented in `.env.example` as optional. | Extend |
| GH-05 | Fetch CI job logs for failed pipelines (needed by AC-01). | New |
| GH-06 | Read issues from GitLab/GitHub (needed by AC-05). | New |

### 6.3 Builds (BL)

| ID | Requirement | Status |
|---|---|---|
| BL-01 | Maven module: `mvnw clean install -B` for builds, wrapper preferred. | Existing |
| BL-02 | Node module: npm/pnpm/yarn `run <script>`, including per-environment scripts like `build:<env>`; package manager detected from the lockfile. | Existing |
| BL-03 | Ordered steps; a build stops at the first failure; one build at a time, the rest queue. | Existing |
| BL-04 | "Order by dependencies": read poms, handle circular dependencies (Maven only at v1). | Existing |
| BL-05 | Build presets: saved sequences runnable from UI, command palette, or agent. | Existing |
| BL-06 | Live logs; history stored in SQLite with log files. | Existing |
| BL-07 | Build artifacts (jar, war, dist, wheel, binaries) with "show in folder". | Extend |
| BL-08 | Per-repo **verify command**: defaults to the toolchain's **fast verify chain** (table below), overridable per repo; each repo can opt in to running its full tests. Used by VF-01. | New |
| BL-09 | Builds can run inside a worktree, not only the main checkout. | Extend |
| BL-10 | **Toolchain module interface**: detect, default commands (install, verify, build, dev server), artifacts, optional ordering. Modules are self-contained and contributable, like agent adapters. A repo can match more than one module (e.g. a Spring backend with a Node frontend folder). | New |
| BL-11 | Gradle module: `gradlew` / `gradlew.bat` preferred; multi-project builds handled by Gradle itself. | New |
| BL-12 | Go module: `go build ./...`, `go vet ./...`, `go test ./...`. | New |
| BL-13 | .NET module: `dotnet restore`, `dotnet build`, `dotnet test`; solution file detected. | New |
| BL-14 | Python module: detects **uv** (`uv.lock` or `[tool.uv]`), **Poetry** (`poetry.lock` or `[tool.poetry]`) and **pip + venv** (`requirements.txt`); runs commands through the project's runner (`uv run`, `poetry run`, or the venv's own Python). Pipenv, PDM, Hatch and conda use the custom command at v1. | New |
| BL-15 | **Custom command** fallback for any repo, in any ecosystem (Rust, PHP, unsupported Python tools…): user-defined install, verify, build and dev-server commands. | New |

**Default fast verify chains (BL-08).** Each step runs only if the repo has it configured; the chain stops at the first failure.

| Toolchain | Fast verify chain | Full tests (opt-in per repo) |
|---|---|---|
| Node | `typecheck` → `lint` → `test` scripts, run with `CI=true`; if none exist, `build` | — (already included when a `test` script exists) |
| Maven | `mvnw -B test-compile` (compile main and test code, don't run tests) | `mvnw -B test` |
| Gradle | `gradlew compileJava compileTestJava` (or the Kotlin equivalents) | `gradlew test` |
| Go | `go vet ./...` → `go build ./...` | `go test ./...` |
| .NET | `dotnet build --no-restore` | `dotnet test` |
| Python | `ruff check` → `mypy` or `pyright` → `pytest`, each only if configured; if none, `python -m compileall -q`. Django repos also run `manage.py check`. | `pytest` when not configured by default |
| Custom | the user's verify command | — |

Tests that need a database or other services are why Maven, Gradle and .NET don't run tests by default.

### 6.4 Dev servers and terminal (DV)

| ID | Requirement | Status |
|---|---|---|
| DV-01 | Dev servers, per repo or per worktree, with commands from the toolchain module: Node `npm run dev|start|serve`, Maven `mvn spring-boot:run`, Gradle `bootRun`, .NET `dotnet run`/`dotnet watch`, Python `uvicorn`/`fastapi dev`/`flask run`/`manage.py runserver`, or the custom command. | Extend |
| DV-02 | Port detection from server output plus `netstat`, behind the `platform` package. | Extend |
| DV-03 | Live server logs. On Windows, every child process is attached to a job object so it dies with Yggdrasil; on other platforms, servers left from a crashed run are listed as orphaned with a way to stop them. | Extend |
| DV-04 | Web terminal dock (xterm.js in the browser, ConPTY on the server, `Ctrl+\``); each socket needs a single-use token. | Extend |
| DV-05 | Dev servers and terminals start only inside a tracked repo or worktree, never receive `.env` tokens, and are killed when Yggdrasil stops. | Existing |

### 6.5 Agents (AG)

| ID | Requirement | Status |
|---|---|---|
| AG-01 | **Adapter interface**: start task, stream normalized events (message, tool call, file edit, command, usage, error), resume session, cancel, report usage. Every event carries an optional **parent ID** so sub-agent activity can be nested. | New |
| AG-02 | **Capability flags** per adapter (resume, cost reporting, plan limits, permission hooks, enforcement level, **sub-agents: none / flat / nested**, budget cap). UI hides or greys out unsupported features. | New |
| AG-03 | Adapters at v1: **Claude Code** and **OpenCode** (reference), **Codex CLI** and **Gemini CLI** (community-maintained), and a **mock adapter** that replays recorded event streams for tests and demos. OpenCode is also the free test path for non-Claude models (e.g. local models through Ollama). | Extend |
| AG-04 | Claude Code alternative backends as an advanced setting (Bedrock, Vertex, custom base URL), with a warning that non-Claude models in Claude Code's harness are unsupported. | New |
| AG-05 | Each adapter checks its prerequisites at startup (CLI installed, signed in, Windows requirements) and reports problems in plain words. | New |
| AG-06 | **Ask mode**: read-only. Questions, triggering builds, data debugging (agent writes SELECT queries, never runs them unless DB-02 is enabled). | Extend |
| AG-07 | **Code mode**: each task gets its own worktree (auto-named), may edit only inside it, may run only allowlisted test/build/lint commands. | Existing |
| AG-08 | **Current-branch mode**: opt-in per task, behind a warning; agent edits the main checkout directly. | Existing |
| AG-09 | **Whole-project mode**: agent edits all tracked repos of a project at once and proposes one commit per repo; everything else in the project folder is unreadable to it. | Existing |
| AG-10 | Mode switching per message within a chat (Ask ↔ Code). | Existing |
| AG-11 | Parallel agents with `maxAgents` (**default 2**) and a queue; only one agent may edit a given main checkout at a time. Settings show a hint when the machine has room for more. | Existing |
| AG-12 | Agent board: Working / Needs you / Review / Done. | Existing |
| AG-13 | Composer: scope (repo or project), agent, model, saved prompts; `@` mentions fuzzy-search files and folders. | Extend |
| AG-14 | Transcript: tool calls inline, file edits as diffs, duration per turn. | Existing |
| AG-15 | Usage display: per-task tokens and cost (best effort per adapter), plan limits where the adapter supports them, agent usage widget. | Extend |
| AG-16 | **Budgets**: optional token or cost cap per task, including its sub-agents and sub-tasks; the agent pauses when reached and the task moves to "Needs you". Adapters use the agent's own cap where available (Claude Code `--max-budget-usd`), otherwise Yggdrasil tracks reported usage and stops the run. | New |
| AG-17 | **Plan mode**: the agent writes a plan first; the user edits or approves it before the agent gets write access. **On by default for whole-project tasks**, skippable per task; a "skip" choice can be remembered per project. Off by default for single-repo Code mode. | New |
| AG-18 | **Checkpoints**: snapshot worktree state after each agent turn; user can rewind a worktree to any earlier checkpoint (within the worktree only, never the main checkout). | New |
| AG-19 | **Overlap warnings**: warn when two running tasks (including sibling sub-tasks) edit the same files or modules. | New |
| AG-20 | Draft buttons: one-shot calls that write a commit message, or MR title and description, from a diff. | Existing |
| AG-21 | **Sub-agent visibility**: the transcript shows agent-native sub-agents as a nested tree (built from parent IDs; Claude Code adapter passes `--forward-subagent-text`), with per-sub-agent tool calls, duration and usage. Their usage counts toward the task. | New |
| AG-22 | **Sub-agent limits**: per-task caps on sub-agent nesting depth (default 2) and concurrency (default 4), applied through each adapter's settings (Claude Code: `CLAUDE_CODE_MAX_SUBAGENT_SPAWN_DEPTH`, `CLAUDE_CODE_MAX_CONCURRENT_SUBAGENTS`). Agent-native worktree isolation for sub-agents (Claude Code `isolation: worktree`) is blocked inside Yggdrasil tasks, because it creates unmanaged worktrees from the default branch. | New |
| AG-23 | **Sub-tasks**: a task can be split into child tasks, each with its own scope (one or more repos), brief, agent and model, and its own worktree(s). Splits are created from an approved plan ("Run as sub-tasks", AG-17), by the user, or proposed by an agent through `propose_subtasks` (needs confirmation). | New |
| AG-24 | **Sub-task rules**: one level only (children cannot create sub-tasks); a cap on children per task (default 6); children share the `maxAgents` queue; the parent's budget is split across children; children inherit the parent's mode and permissions and never get more (a child of an Ask task cannot use Code mode). | New |
| AG-25 | **Sub-task ordering**: children can depend on siblings (e.g. `shop-web` starts after `shop-api` passes verification). Dependencies form a simple acyclic order shown in the UI; a failed child blocks its dependents and moves the parent to "Needs you". | New |
| AG-26 | **Sub-task results**: each child is verified on its own (VF-01); all children's proposals join the parent's change set with merge order (RV-03, RV-04); an optional **integration pass** lets the parent agent review all child diffs together before you review. The board shows the parent card with its children's progress. | New |

### 6.6 Verification (VF)

| ID | Requirement | Status |
|---|---|---|
| VF-01 | When an agent marks a task done, automatically run each changed repo's verify command (BL-08) in its worktree. | New |
| VF-02 | Show pass/fail per repo on the task card and in Review before the user opens the diff. | New |
| VF-03 | On failure, offer "Send failure to agent" with the log attached, to start another round. | New |

### 6.7 Review and ship (RV)

| ID | Requirement | Status |
|---|---|---|
| RV-01 | Review tab: worktree diffs, inline notes sent back to the agent, then commit, push, or create MR. | Existing |
| RV-02 | All commits, pushes and MRs are proposals that need UI confirmation. | Existing |
| RV-03 | **Change sets**: proposals from one multi-repo task are grouped; "confirm all" or per-repo confirm; linked MRs reference each other. | New |
| RV-04 | Change-set **merge order**: user (or dependency analysis) defines order; UI warns when merging a dependent repo before its dependency. | New |
| RV-05 | Merge locally: `--no-ff` merge of the agent's branch into the main checkout; aborted on conflict; never pushed automatically. | Existing |
| RV-06 | Remove worktree only after confirmation, and only if it is clean or its branch was merged or discarded. Handle Windows file locks with a clear message and retry. | Extend |

### 6.8 Workspace → agent actions (AC)

| ID | Requirement | Status |
|---|---|---|
| AC-01 | Failed pipeline → "Diagnose with agent" (Ask mode, CI job log attached) → optional "Fix in worktree" (Code mode). | New |
| AC-02 | Open MR → "Review with agent": agent drafts review comments as proposals; nothing is posted without confirmation. | New |
| AC-03 | Uncommitted changes → "Propose commits": agent groups changes and drafts commit proposals. | New |
| AC-04 | Repo behind → "Pull and check": fast-forward pull, or explain why it can't. | New |
| AC-05 | Issue → task: pull GitLab/GitHub issues into the board as draft tasks. | New |
| AC-06 | Actions appear on the relevant widget rows and repo rows, and in the command palette. | New |

### 6.9 Worktree environment (WE)

| ID | Requirement | Status |
|---|---|---|
| WE-01 | Bootstrap profiles per repo: files to copy into new worktrees (env files, local config), similar to `.worktreeinclude`. | New |
| WE-02 | Port ranges per worktree, so dev servers in parallel worktrees never collide. | New |
| WE-03 | Optional per-worktree services via docker compose with offset ports (e.g. a database per worktree). | New |
| WE-04 | Short worktree paths and `core.longpaths` set, to stay under Windows path limits. | New |
| WE-05 | **Toolchain bootstrap**: before the first verification, each toolchain module prepares the worktree's dependencies (e.g. `npm ci`, `uv sync`, `poetry install`, or a new `.venv` + `pip install -r requirements.txt`). Progress is shown on the task; failures are reported in plain words. | New |

### 6.10 Automation (AU)

| ID | Requirement | Status |
|---|---|---|
| AU-01 | Scheduled agent tasks (e.g. nightly dependency bumps, flaky-test triage, update agent instruction files from recent commits). Results land as proposals. Runs only while Yggdrasil is running; missed runs are reported. | New |
| AU-02 | Standup summary from commits, MRs and builds, with "polish with agent" and copy. | Existing |
| AU-03 | Release notes across all repos of a project, between two tags or dates. | New |

### 6.11 Cross-repo intelligence (XR)

| ID | Requirement | Status |
|---|---|---|
| XR-01 | Code search: `git grep` across tracked repos, with links that open VS Code, Cursor or IntelliJ. | Existing |
| XR-02 | API contract checks: when a backend change alters an endpoint, detect frontend callers that may break and show them on the change set. **Layered approach:** (1) when a repo has an OpenAPI spec, compare specs with `oasdiff` to find breaking changes; (2) use `gotreesitter` to find Spring endpoint mappings and frontend call sites, and match them by path and method; (3) optionally, the agent writes a plain-language impact summary on top. Checks are advisory, never blocking. | New |

### 6.12 Data debugging (DB)

| ID | Requirement | Status |
|---|---|---|
| DB-01 | In Ask mode the agent writes SELECT queries for the user to run; it never runs them by default. | Existing |
| DB-02 | Optional, opt-in read-only database connection: replica or read-only credentials only, SELECT-only parser check, row limit, statement timeout, results logged to the audit log. | New |

### 6.13 MCP server (MCP)

| ID | Requirement | Status |
|---|---|---|
| MCP-01 | Read tools: `list_repos`, `repo_status`, `recent_commits`, `changed_files`, `get_diff`, `remote_status`. | Existing |
| MCP-02 | Build tools: `build_info`, `build_order`, `start_build`, `list_build_presets`, `run_build_preset`, `list_builds`, `build_log`. | Existing |
| MCP-03 | Dev server tools: `list_dev_servers`, `dev_server_log`. | Existing |
| MCP-04 | Safe git: `fetch`, `pull`. | Existing |
| MCP-05 | Proposal tools (each needs UI confirmation): `propose_commit`, `propose_push`, `propose_merge_request`, `propose_install`, `propose_publish`, `propose_claude_file` (rename to `propose_agent_file`, agent-agnostic), `propose_delete_files`, and `propose_subtasks` (AG-23). | Extend |
| MCP-06 | MCP visibility limited to tracked repos. | Existing |

### 6.14 New repo and publish (NR)

| ID | Requirement | Status |
|---|---|---|
| NR-01 | New repo inside a tracked project: `git init`, README, `.gitignore`, first commit; optionally start a code agent from a first prompt. | Existing |
| NR-02 | Before a remote exists, the agent edits the folder directly. | Existing |
| NR-03 | Dependency install: command from the repo's toolchain module; runs only in the main checkout; never gets `.env` tokens. | Existing |
| NR-04 | Publish to GitHub: create repo under user or org (private by default), add origin, push. Token never put in the URL or git config. | Existing |
| NR-05 | Publish to GitLab (self-hosted). | New |

### 6.15 Home and UI (UI)

| ID | Requirement | Status |
|---|---|---|
| UI-01 | Hero scene: procedural, follows time of day (or dawn, day, dusk, night, off), with the task composer on top and suggestion chips. | Existing |
| UI-02 | Widget grid, customizable. **Widgets are self-contained modules** with a small interface so they can be added, removed or contributed independently. | Extend |
| UI-03 | Widgets at v1: Working now, Merge requests, Running services, Git activity, Needs attention, Recent areas, CI failures, Builds, Recent chats, Standup, Agent usage, This machine. | Existing |
| UI-04 | Shell: floating top bar, `Ctrl+K` command palette, notifications, running-status pill. | Existing |
| UI-05 | Themes: System, Harbour, Daylight, Dracula, Rosé Pine, Nord, Gruvbox. | Existing |
| UI-06 | Notifications for "Needs you", verification results and finished tasks: in-app, plus **browser notifications** (Web Notifications API) when the tab is in the background. Content stays minimal: task name and status, no code or file paths. External channels (webhooks, ntfy, chat apps) come after v1. | New |
| UI-07 | Empty states that make sense with zero or few repos. | New |
| UI-08 | All UI wording agent-neutral ("agent", not a specific vendor). | Extend |

### 6.16 Safety and security (SF)

| ID | Requirement | Status |
|---|---|---|
| SF-01 | Bound to `127.0.0.1` by default; Host and Origin checks against DNS rebinding and cross-site requests. | Existing |
| SF-02 | Only tracked repos are visible to agents and MCP. | Existing |
| SF-03 | Never implemented: force push, `reset --hard`, branch deletion, deleting files on disk. | Existing |
| SF-04 | Tokens kept out of every child process (clean environment). | Existing |
| SF-05 | Audit log in SQLite for every proposal, confirmation, agent command and git mutation. | Existing |
| SF-06 | **Enforcement outside the agent**: writable directory limited to the task's worktree(s); command allowlist checked by Yggdrasil (via adapter permission hooks where available). Adapters without hooks are marked "limited enforcement" and are restricted to worktree-only Code mode. | New |
| SF-07 | **Ask-mode integrity check**: after each Ask turn, verify the working tree is unchanged; if changed, stop the task, flag it, and show what changed (no automatic revert). | New |
| SF-08 | Threat model document published in the repo. | New |
| SF-09 | **Repo-supplied agent config**: agent CLIs can run a repo's own hooks and MCP servers without a trust prompt (Claude Code in `-p` mode runs `.claude/settings.json` hooks and `.mcp.json` servers unless `--bare` is used, and `--bare` doesn't work with subscription login). Before the first agent run in a repo, Yggdrasil detects such files, shows what they would run, and requires the user to trust the repo once (recorded in the audit log). Untrusted repos run only in Ask mode. | New |

### 6.17 Remote access mode (RA)

| ID | Requirement | Status |
|---|---|---|
| RA-01 | Optional mode to run Yggdrasil on another machine (e.g. a Linux server) and open it from a browser elsewhere. Off by default. | New |
| RA-02 | Requires login (local account with strong password or token), session expiry, and explicit bind address configuration. | New |
| RA-03 | Documentation recommends a private network (e.g. Tailscale/WireGuard) and warns against exposing it to the public internet. | New |

### 6.18 Installation and onboarding (IN)

| ID | Requirement | Status |
|---|---|---|
| IN-01 | Install by cloning: `git clone`, then `task build` builds the frontend, embeds it and produces one binary; `ygg` (or `task run`) serves everything on one port. | Extend |
| IN-02 | Pinned toolchains: Go version in `go.mod`, Node version for the frontend build in `.nvmrc`, committed `go.sum` and frontend lockfile, one package manager (pnpm). | New |
| IN-03 | No native modules and no cgo: the build needs only Go and Node, never a C compiler or Visual Studio Build Tools. | New |
| IN-04 | `ygg doctor`: checks git, the toolchains used by tracked repos (Node, Java/Maven/Gradle, Go, .NET, Python with uv/Poetry), agent CLIs and sign-in, and explains what's missing. | New |
| IN-05 | First run with nothing tracked offers a **demo project with mock repos** (fictional names and history). The same data is used for README screenshots. | New |
| IN-06 | `.env.example` documenting every setting and token, all optional. | New |
| IN-07 | Tagged releases with a changelog; updating = `git pull` to a tag + `task build`. | New |
| IN-08 | Release pipeline (GoReleaser) prepared so downloadable binaries can be switched on after v1 without code changes. | New |

### 6.19 Platform (PL)

| ID | Requirement | Status |
|---|---|---|
| PL-01 | Windows: supported and tested. macOS and Linux: experimental, contributions welcome. Stated in the README. | New |
| PL-02 | All OS-specific behaviour lives in one **`platform` package** (build-tagged files per OS): paths, shell and wrapper resolution (`mvnw.cmd`, `gradlew.bat`, `npm.cmd`, venv `Scripts\python.exe` vs `bin/python`), job objects / process-tree kill, PTY, port detection, opening editors/folders. No OS checks elsewhere. | New |
| PL-05 | Processes meant to outlive Yggdrasil (editors, file explorer) are started outside its job object, so closing Yggdrasil never closes the user's editor. | New |
| PL-03 | Handle Windows specifics: long paths, file locks on worktree removal, line endings (`core.autocrlf`) in agent diffs, antivirus slowness (documented tip). | New |
| PL-04 | Repos inside WSL (`\\wsl$`) are unsupported at v1 and detected with a clear message. | New |

## 7. Non-goals (v1)

- Force push, `reset --hard`, branch deletion, deleting files on disk — never.
- Hosted service or multi-user team mode.
- Writing Yggdrasil's own agent loop or model provider layer (agents come from adapters).
- Installers or package-manager distribution (cloning only at v1; binaries prepared but not published).
- Migrating data or code from the personal tool (clean rewrite).
- Official macOS/Linux support (experimental only).
- External notification channels: webhooks, ntfy, Discord, Slack, Telegram, email (planned after v1).
- Toolchain modules for Rust, PHP, Ruby, and Python tools other than uv, Poetry and pip + venv (custom command at v1; community modules welcome).
- Cross-repo build ordering for toolchains other than Maven.

## 8. Non-functional requirements

- **Performance:** status refresh for 50 repos completes in under 10 seconds on a typical laptop; UI stays responsive during builds and agent runs.
- **Reliability:** a crash or restart never leaves processes running (job objects on Windows; orphan detection elsewhere) and never corrupts a worktree or main checkout.
- **Resources:** idle memory under 100 MB for the server process.
- **Privacy:** no telemetry by default. If added later, opt-in only.
- **Security:** all items in 6.16; dependencies audited in CI.
- **Accessibility:** keyboard navigation for core flows; readable contrast in all themes.

## 9. Success measures

- A new Windows user with Go and Node installed goes from `git clone` to a running demo project in **under 10 minutes** without build-tool errors.
- A new user completes a first agent task (prompt → verified → reviewed → committed) in under 15 minutes.
- Zero reports of Yggdrasil modifying the main checkout without a confirmed proposal.
- External contributions to adapters, toolchain modules, widgets, or platform implementations within the first months.

## 10. v1 release criteria

- All v1 requirements implemented or explicitly deferred in the changelog.
- CI green on Windows with `CGO_ENABLED=0`; macOS/Linux jobs running (allowed to fail).
- Apache-2.0 `LICENSE` and `NOTICE` files in place; dependency licenses checked.
- Threat model, README, contributing guide, and `.env.example` published.
- Demo project and screenshots contain only mock data.
- No internal hostnames, real project names, usernames or client data anywhere in code or git history.

## 11. Open questions

1. GitHub org/repo name and project domain (the bare `yggdrasil` names are taken; see D14).

### 11.1 Naming notes

"Yggdrasil" is already used by other software, so the project avoids their command and process names:

| Existing project | What it is | Names to avoid |
|---|---|---|
| Yggdrasil Network | Open-source encrypted mesh network, written in Go, with a Windows installer | `yggdrasil`, `yggdrasilctl` binaries; "Yggdrasil" Windows service |
| Red Hat yggdrasil | System daemon for remote management on RHEL | `yggd`, `yggctl` |
| yggdev (npm) | "Yggdrasil Development CLI" for running several projects at once | `yggdev` |

Rules that follow from this:
- The binary, process and command are **`ygg`** (`ygg.exe` on Windows), never `yggdrasil`.
- Docs, README and the repo description always pair the name with a descriptor, e.g. "Yggdrasil: a multi-repo workspace for code agents", so search results aren't confused with the mesh network.
- The Go module path uses the owner's namespace (`github.com/<owner>/yggdrasil`), so imports don't collide.
- User-facing UI uses plain words for features ("Review", "Needs you"), not Norse names; mythology stays in branding, docs and visuals.

## 12. Decision log

| # | Date | Question | Decision |
|---|---|---|---|
| D1 | 2026-10-13 | Product shape | Workspace and agents as one product, connected both ways; multi-repo first. |
| D2 | 2026-10-13 | Platform | Windows supported; macOS and Linux experimental. |
| D3 | 2026-10-13 | Installation | Clone and build at v1; binaries prepared for later. |
| D4 | 2026-10-13 | Tech stack | Clean rewrite: Go backend, React + TypeScript frontend, single binary, no cgo. |
| D5 | 2026-10-14 | License | **Apache-2.0**: permissive, explicit patent grant, grants no rights to the project name. |
| D6 | 2026-10-14 | Reference adapters | **Claude Code + OpenCode** (OpenCode testable for free with local models). Codex CLI and Gemini CLI are community-maintained. |
| D7 | 2026-10-14 | Default `maxAgents` | **2**, with a hint in settings when the machine has room for more. |
| D8 | 2026-10-14 | Default verify commands | **Detected fast chain** per toolchain (section 6.3); full tests opt-in per repo. |
| D9 | 2026-10-14 | Toolchains at v1 | Modules for **Node, Maven, Gradle, Go, .NET, Python** (uv, Poetry, pip + venv); custom command for everything else. |
| D10 | 2026-10-14 | Plan mode | **On by default for whole-project tasks, skippable**, skip remembered per project; off by default for single-repo tasks. |
| D11 | 2026-10-14 | Notifications | **In-app + browser notifications only** at v1; external channels after v1. |
| D12 | 2026-10-14 | ConPTY library | Try **`aymanbagabas/go-pty`** first in the Phase 0 spike; fallback is a thin wrapper on `golang.org/x/sys/windows` plus `creack/pty` on Unix. **Outcome (Phase 0 spike B1):** go-pty works, but we use the **own thin wrapper**, because go-pty v0.2.2 leaks a process handle per session, closes the console and pipes together (losing output), discards the thread handle needed for race-free job assignment, and defaults `Cmd.Env` to the parent environment (SF-04 risk). |
| D13 | 2026-10-14 | XR-02 approach | **Layered**: `oasdiff` when a spec exists, `gotreesitter` for code, optional agent impact summary. Re-check `gotreesitter` maturity (pre-1.0) when Phase 9 starts. |
| D14 | 2026-10-14 | Product name | **Yggdrasil**, the world tree connecting the nine worlds, like one workspace connecting many repos and their branches. Command and binary **`ygg`**, to avoid clashes with existing Yggdrasil software (section 11.1). |
| D15 | 2026-10-14 | Agent-native sub-agents | **Show, limit and count them** (AG-21, AG-22): nested transcript, caps on depth and concurrency, agent-native sub-agent worktrees blocked. Yggdrasil doesn't create them. |
| D16 | 2026-10-14 | Sub-tasks across agents | **In v1**: approved split, one level deep, shared queue, split budget, dependencies between siblings, results in one change set (AG-23 to AG-26). Agents can propose splits but never start sub-tasks without approval. |
| D17 | 2026-10-14 | Repo-supplied agent config | **Trust per repo** before the first agent run when a repo ships agent hooks or MCP servers (SF-09). |
