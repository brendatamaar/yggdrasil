# Yggdrasil — Implementation Plan

> Status: Draft v0.5 · 2026-10-14 (decisions D5–D17 from docs/PRD.md section 12 applied)
> Goal: release **v1.0.0** with the full scope in docs/PRD.md.
> Approach: **clean rewrite in Go (backend) and React + TypeScript (frontend)**, delivered as one binary. The personal tool is a behavioural reference only; no code or data is migrated. Each phase ends with a tagged pre-release (`v0.x`). From Phase 4 on, the rewrite should be good enough to replace the personal tool in daily use.

Requirement IDs refer to docs/PRD.md. Stack and conventions are in docs/PRD.md section 5.1 and docs/AGENTS.md.

---

## Phase 0 — Spikes and project setup → `v0.1.0`

Goal: prove the risky parts on Windows before building on them, and set up a public-ready repo.

**Spikes (throwaway code, findings written to `docs/spikes.md`):**
- [x] **ConPTY spike:** try **`aymanbagabas/go-pty` first** (D12). Open PowerShell and cmd, stream to xterm.js over WebSocket, resize, run an interactive program, exit cleanly. If it fails any of these, build the fallback: a thin wrapper on `golang.org/x/sys/windows` plus `creack/pty` on Unix. Record the result in `docs/spikes.md`.
- [x] **Job object spike:** start a process tree (e.g. `npm run dev` spawning children), kill Yggdrasil with Task Manager, confirm every child dies. Also confirm that an editor launched outside the job (PL-05) stays open.
- [x] **Agent streaming spike:** run Claude Code headless with streaming JSON output from Go, parse events, cancel mid-run, confirm the process tree stops. Also: trigger a sub-agent and confirm (a) its messages arrive with `parent_tool_use_id` when `--forward-subagent-text` is passed, (b) `PreToolUse` hooks fire for its tool calls, (c) the depth and concurrency environment variables are respected, and (d) sub-agent `isolation: worktree` can be blocked.
- [x] **Pure-Go SQLite spike:** `modernc.org/sqlite` + sqlc + goose with `CGO_ENABLED=0` on Windows. *(Tests, `-race` and `go version -m` all pass.)*

**Project setup:**
- [ ] Pick the GitHub org/repo name and domain (PRD open question 1). Create a fresh public repo; add Apache-2.0 `LICENSE` and `NOTICE` (D5). *(Repo `brendatamaar/yggdrasil`, LICENSE and NOTICE done; domain still open.)*
- [x] Set the Go module path (`github.com/<owner>/yggdrasil`) and binary name `ygg` (D14).
- [x] Repo skeleton following docs/AGENTS.md section 4: `cmd/`, `internal/`, `web/`, `fixtures/`, `docs/`.
- [x] `go.mod` with pinned Go version; `web/` with Vite + React + TypeScript strict + pnpm; `.nvmrc` (IN-02).
- [x] Taskfile with `build`, `run`, `dev`, `generate`, `test`, `test:web`, `test:e2e`, `lint`, `typecheck`.
- [ ] `go:embed` of `web/dist` into the binary; `task build` produces one executable serving everything on one port (IN-01).
- [ ] CI (GitHub Actions): Windows required (`CGO_ENABLED=0`, `go test -race`, golangci-lint, ESLint, typecheck, Vitest); macOS and Linux allowed to fail.
- [ ] CI dependency license check (Go and npm) against the allowed list in docs/AGENTS.md section 2.
- [ ] Windows CI runner installs the toolchains used by tests: Node + pnpm, Java + Maven + Gradle, .NET SDK, Python + uv + Poetry.
- [ ] GoReleaser config prepared but not publishing (IN-08).
- [x] Script that generates `fixtures/demo-project/`: fictional repos (`acme-shop`: `shop-api`, `shop-web`, `shop-admin`), history, branches (IN-05).
- [x] `.env.example` skeleton (IN-06). README skeleton with platform statement (PL-01). docs/AGENTS.md, docs/CONTRIBUTING.md, docs/CHANGELOG.md.

**Exit:** all four spikes pass on Windows; CI green; `task build` on a clean Windows machine with only Go and Node installed produces a binary that serves an empty UI.

---

## Phase 1 — Foundations → `v0.2.0`

Goal: the structure every feature builds on.

- [x] Data model and first migrations: projects, repos, worktrees, tasks, change sets, proposals, builds, audit log (`internal/core`, `internal/db`).
- [x] `internal/platform`: paths, executable/wrapper resolution, job objects, `platform.Start`, PTY, port detection, open editor/folder, WSL path detection (PL-02, PL-04).
- [x] `internal/safety`: clean environment builder and token redaction for logs (SF-04).
- [x] `internal/git`: CLI wrapper for the safe subset, with `core.longpaths` (WE-04).
- [x] `internal/api`: HTTP server bound to `127.0.0.1`, Host/Origin checks, WebSocket hub, error format (SF-01).
- [x] `internal/audit`: audit writer used by every state change (SF-05).
- [x] `tygo` pipeline: Go types → `web/src/api/gen/*.gen.ts`, wired into `task generate`.
- [x] Agent `Adapter` interface, `Event` types with optional `ParentID`, and `Capabilities` including sub-agent support and budget cap (AG-01, AG-02).
- [x] Task model with optional `ParentTaskID` in the first migration (AG-23).
- [x] Mock adapter replaying `testdata/*.jsonl`, including one fixture with nested sub-agents (AG-03).
- [x] Claude Code reference adapter, based on the Phase 0 spike (AG-03, AG-05). *(Ask mode only until worktrees and the path guard exist; needs a manual run against the real CLI, see docs/phases/phase-1.md.)*
- [x] Frontend shell: router, TanStack Query client, WebSocket client, theme tokens (System + Harbour first), top bar, `Ctrl+K` palette skeleton, notifications area, status pill (UI-04).
- [x] Widget module interface and grid with Customize (UI-02).
- [x] `ygg doctor` subcommand, first version (IN-04).

**Exit:** a mock agent task streams events end to end into a basic transcript view; every spawned process goes through `platform.Start`; safety helpers have tests.

---

## Phase 2 — Workspace parity → `v0.3.0`

Goal: the workspace half matches the personal tool.

- [ ] Project and repo tracking with folder browser, preview, rescan (WS-01).
- [ ] Repo status, filters, Projects page tabs with live counts (WS-02, WS-03, WS-04).
- [ ] Repo page: changes, diffs, history, commit detail, branches (WS-05).
- [ ] Git actions: fetch, fetch all, ff-only pull, checkout, cross-repo branch switch (WS-06, WS-07).
- [ ] Open in editor / show in folder (WS-08).
- [ ] Git hosts: GitLab (multiple instances), GitHub, Gitea; MR/PR and pipeline status; MR inbox; MR creation via push options; optional tokens (GH-01–GH-04).
- [ ] Toolchain module interface (BL-10) and `docs/writing-a-toolchain.md` draft.
- [ ] Toolchain modules: Node, Maven (with dependency ordering), Gradle, and the custom command fallback (BL-01, BL-02, BL-04, BL-11, BL-15).
- [ ] Builds: ordered steps, queue, presets, live logs, history, artifacts (BL-03, BL-05–BL-07).
- [ ] Code search with editor links (XR-01).
- [ ] Home: hero scene, composer placeholder, suggestion chips (UI-01).
- [ ] Widgets: Git activity, Merge requests, Needs attention, CI failures, Builds, Recent areas, This machine (UI-03).
- [ ] Remaining themes (UI-05).
- [ ] Performance check: status refresh for 50 repos under 10 seconds.

**Exit:** the demo project shows correct status, MRs, pipelines and builds; daily workspace use is possible.

---

## Phase 3 — Agents and safety → `v0.4.0`

Goal: the agent half matches the personal tool, with stronger enforcement.

- [ ] Worktree creation with auto-generated short names (AG-07, WE-04).
- [ ] Ask mode, Code mode, current-branch mode (opt-in, warning), whole-project mode (AG-06–AG-09).
- [ ] Mode switching per message (AG-10).
- [ ] **OpenCode reference adapter** (AG-03, D6), tested with a local model through Ollama. Building it here proves the adapter interface with two real agents before more features depend on it.
- [ ] Parallel agents with `maxAgents` (default 2, with a settings hint when the machine has room for more), queue, one editor per main checkout (AG-11, D7).
- [ ] Agent board: Working / Needs you / Review / Done (AG-12).
- [ ] Composer: scope, agent, model, saved prompts, `@` file mentions (AG-13).
- [ ] Transcript with inline tool calls, diffs, turn durations; session resume (AG-14).
- [ ] Sub-agent visibility: nested transcript tree, per-sub-agent usage counted toward the task (AG-21).
- [ ] Sub-agent limits: depth and concurrency caps per task; agent-native sub-agent worktrees blocked (AG-22).
- [ ] Usage display and agent usage widget (AG-15).
- [ ] Draft buttons for commit messages and MR descriptions (AG-20).
- [ ] Proposals: lifecycle, confirmation UI, execution, audit (RV-02).
- [ ] Path guard and command allowlist enforced by Yggdrasil; "limited enforcement" handling (SF-06).
- [ ] Ask-mode integrity check (SF-07).
- [ ] Repo trust: detect repo-supplied agent hooks and MCP servers, show what they would run, require trust once per repo; untrusted repos are Ask-only (SF-09).
- [ ] MCP server with all read, build, dev-server, safe-git and proposal tools; `propose_agent_file` naming (MCP-01–MCP-06).
- [ ] Tests for every safety invariant (docs/AGENTS.md section 6).
- [ ] Threat model (SF-08).
- [ ] Widgets: Working now, Recent chats, Agent usage (UI-03).

**Exit:** a Code-mode task on the demo project runs in a worktree with either reference adapter, cannot write outside it or run unlisted commands, and produces confirmable proposals. Rewrite can replace the personal tool for agent work.

---

## Phase 4 — Complete the core loop → `v0.5.0`

Goal: prompt → isolated work → verified → reviewed → shipped, across repos.

- [ ] Fast verify chains for Node, Maven, Gradle and custom commands, with per-repo override and full-test opt-in (BL-08, D8).
- [ ] Builds inside worktrees (BL-09).
- [ ] Toolchain bootstrap for new worktrees, Node, Maven and Gradle first (WE-05).
- [ ] Automatic verification; pass/fail on task card and in Review; "Send failure to agent" (VF-01–VF-03).
- [ ] Review tab with diffs and inline notes back to the agent (RV-01).
- [ ] Change sets: grouped proposals, confirm all or per repo, linked MRs (RV-03).
- [ ] Merge order with dependency warnings (RV-04).
- [ ] Local `--no-ff` merge, aborted on conflict (RV-05).
- [ ] Safe worktree removal with Windows file-lock handling (RV-06, PL-03).
- [ ] CRLF handling in agent diffs (PL-03).

**Exit:** a multi-repo task on the demo project runs end to end with automatic verification and a change set shipped as linked MRs against a test host.

---

## Phase 5 — Dev servers, terminal, worktree environment and more toolchains → `v0.6.0`

Goal: parallel worktrees can each run their own app without collisions, in every v1 ecosystem.

- [ ] Toolchain modules for Go, .NET and Python (uv, Poetry, pip + venv), each with detection, fast verify chain, bootstrap and dev-server commands (BL-12–BL-14, WE-05, D9).
- [ ] Python worktree bootstrap: `uv sync`, `poetry install`, or a new `.venv` + `pip install`; venv path resolution and clean-environment rules on Windows.
- [ ] Dev servers per repo or worktree for all modules, port detection, live logs, cleanup via job objects (DV-01–DV-03, DV-05).
- [ ] Terminal dock with single-use socket tokens (DV-04).
- [ ] Bootstrap profiles for new worktrees (WE-01).
- [ ] Port ranges per worktree (WE-02).
- [ ] Optional per-worktree docker compose services (WE-03).
- [ ] Widget: Running services (UI-03).

**Exit:** two worktrees of the same frontend+backend pair run side by side with no port conflicts; killing Yggdrasil leaves no processes behind; a Python (uv) repo and a .NET repo in the demo project pass verification in fresh worktrees.

---

## Phase 6 — Workspace → agent actions, notifications, new repos → `v0.7.0`

Goal: the dashboard becomes the agent's to-do list.

- [ ] CI job logs and issue reading (GH-05, GH-06).
- [ ] Actions: diagnose/fix failed pipelines, review MRs, propose commits, pull and check, issues to tasks (AC-01–AC-05).
- [ ] Actions on widget rows, repo rows and in the palette (AC-06).
- [ ] Noise detection for large untracked file counts (WS-09).
- [ ] Notifications: in-app plus browser notifications, minimal content (UI-06, D11).
- [ ] Standup widget with "polish with agent" (AU-02, UI-03).
- [ ] New repo, direct editing before a remote exists, dependency install, publish to GitHub (NR-01–NR-04).
- [ ] Empty states (UI-07).

**Exit:** every "Needs attention" item has at least one agent action.

---

## Phase 7 — Agent power features → `v0.8.0`

- [ ] Plan mode: on by default for whole-project tasks, skippable per task, skip remembered per project; off by default for single-repo tasks (AG-17, D10).
- [ ] Checkpoints with rewind inside the worktree (AG-18).
- [ ] Budgets per task, covering sub-agents and sub-tasks; use `--max-budget-usd` for Claude Code (AG-16).
- [ ] Overlap warnings between running tasks, including sibling sub-tasks (AG-19).
- [ ] Sub-tasks: "Run as sub-tasks" from an approved plan, manual split, and `propose_subtasks` MCP tool (AG-23, MCP-05).
- [ ] Sub-task rules: one level only, cap on children, shared `maxAgents` queue, budget split, scope and mode never wider than the parent (AG-24).
- [ ] Sibling dependencies with acyclic validation; a failed child blocks its dependents and moves the parent to "Needs you" (AG-25).
- [ ] Results: per-child verification, proposals merged into the parent's change set with merge order, optional integration pass, parent card with child progress on the board (AG-26).

**Exit:** a whole-project task can be planned, approved, run with a budget, rewound, and finished. The same task can also be run as sub-tasks: `shop-api` and `shop-web` children on different agents, `shop-web` waiting for `shop-api`, ending in one change set with linked MRs.

---

## Phase 8 — Community adapters → `v0.9.0`

- [ ] Codex CLI and Gemini CLI adapters with recorded fixtures, marked community-maintained (AG-03, D6).
- [ ] Claude Code alternative backends: Bedrock, Vertex, custom base URL, with warning (AG-04).
- [ ] Prerequisite checks per adapter in `ygg doctor` (AG-05, IN-04).
- [ ] Capability matrix in the README, showing reference vs community status.
- [ ] `docs/writing-an-adapter.md`.

**Exit:** the same demo task runs with all four adapters (community ones verified against fixtures).

---

## Phase 9 — Automation and cross-repo intelligence → `v0.10.0`

- [ ] Scheduled agent tasks, results as proposals, missed runs reported (AU-01).
- [ ] Release notes across a project (AU-03).
- [ ] Re-check `gotreesitter` maturity (release status, Java/TypeScript grammar quality, query engine) before starting XR-02 (D13).
- [ ] API contract checks, layered: `oasdiff` when an OpenAPI spec exists; `gotreesitter` for Spring mappings and frontend call sites; optional agent impact summary (XR-02).
- [ ] Opt-in read-only database connection (DB-02).
- [ ] Publish to self-hosted GitLab (NR-05).

**Exit:** a scheduled task runs overnight and leaves proposals; a backend endpoint change shows affected frontend callers.

---

## Phase 10 — Remote access mode → `v0.11.0`

- [ ] Login, session expiry, explicit bind address; off by default (RA-01, RA-02).
- [ ] Docs recommending a private network (RA-03).
- [ ] Threat model updated for remote mode.

**Exit:** Yggdrasil on a Linux machine is usable from another device over a private network, with login required.

---

## Phase 11 — Launch → `v1.0.0`

- [ ] Walk through every PRD v1 requirement; implement or explicitly defer in CHANGELOG.
- [ ] Privacy audit of code, history, docs and screenshots.
- [ ] README: positioning, demo GIF of a multi-repo task, install, capability matrix, platform support.
- [ ] Contributor guides: adapters, toolchain modules, widgets, platform implementations.
- [ ] Final dependency license check; `NOTICE` up to date.
- [ ] Full install test on a clean Windows machine.
- [ ] Tag `v1.0.0`, publish release notes.

**Exit:** PRD section 10 release criteria all met.

---

## Risks

| Risk | Mitigation |
|---|---|
| Rewrite takes longer than expected | Phases 2–4 restore parity first; the personal tool stays in use until Phase 4. Phases after 4 can move to v1.x without blocking a useful release. |
| ConPTY behaviour differs from Unix PTYs | Resolved in Phase 0 (D12): own thin wrapper on `x/sys/windows` + `creack/pty`, behind a small interface in `internal/platform`; last resort is a non-interactive log console for v1. |
| `gotreesitter` is pre-1.0 | XR-02 is advisory only; re-check maturity in Phase 9; `oasdiff` layer works without it. |
| Python tooling is fragmented | v1 supports uv, Poetry and pip + venv only; everything else uses the custom command. |
| Sub-tasks add scheduling complexity and multiply token use | One level only, cap on children, shared queue, split budget; the parent ID and task relation exist from Phase 1 so Phase 7 adds behaviour, not schema changes. |
| Agent CLIs change how sub-agents appear in their output | Sub-agent nesting is a capability flag; adapters without it show a flat transcript instead of failing. |
| Verification is slow or flaky on real projects | Fast chains don't run tests by default for Maven, Gradle and .NET; full tests are opt-in per repo. |
| Go MCP SDK lags the TypeScript SDK | Tool list is small and stable; keep MCP code isolated in `internal/mcp`. |
| A useful dependency needs cgo | Rule in docs/AGENTS.md; prefer pure-Go alternatives, WebAssembly (e.g. via `wazero`), or a separate helper process. |
| Agent CLIs change output formats | Normalized events and recorded fixtures per adapter; adapters isolated. |
| Enforcement gaps for adapters without permission hooks | "Limited enforcement" capability; restricted to worktree-only Code mode. |
| Two-language codebase | Generated shared types; clear backend/frontend split in docs/AGENTS.md. |
| Leaking private data | Fresh public repo, mock data only, privacy audit at launch. |
