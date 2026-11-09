# Changelog

All notable changes to Yggdrasil are documented here. The format follows [Keep a Changelog](https://keepachangelog.com/en/1.1.0/), and versions follow [Semantic Versioning](https://semver.org/). Requirement IDs refer to [docs/PRD.md](PRD.md).

## [Unreleased]

### Added
- Dev servers, terminal and worktree environment (Phase 5): toolchain modules for Go, .NET and Python (uv, Poetry, pip with a new `.venv`) with detection, a fast verify chain, bootstrap and dev-server commands; a missing `uv`, `poetry` or `dotnet` is reported in plain language (BL-12 to BL-14, WE-05). Dev servers per repo or worktree with port detection, live logs, a Running services widget and cleanup when `ygg` ends (on Windows through the job object; on Linux a restarted `ygg` lists leftovers as orphaned and lets you stop them) (DV-01 to DV-03, DV-05, UI-03). A terminal dock for tracked folders with a single-use, 30-second socket token; opening and closing are audited, keystrokes are not, and agents and MCP cannot reach it (DV-04). Each worktree gets a block of 20 ports from a configurable range (default 20000-29999) passed as `PORT` and `YGG_PORT_*` (WE-02). Bootstrap profiles copy named files (such as `.env`) into new worktrees, never overwrite, and are off for agent tasks unless you turn it on per repo (WE-01). Optional docker compose per worktree: `up -d`, `stop` and `down` only, never volume removal, and you confirm the compose file and its `.env` first; files that use `include`, `extends` or `env_file` are refused (WE-03). Nothing in this release pushes, tags or deletes anything.
- Core loop (Phase 4): fast verify chains per toolchain (Node, Maven, Gradle, custom) with a per-repo override and a full-tests opt-in, builds in worktrees and toolchain bootstrap for new worktrees (BL-08, BL-09, WE-05); automatic verification after each turn with a badge on the task card and in Review, and "Send failure to agent" (in whole-project mode it asks for your opt-in at the click) (VF-01 to VF-03); a Review tab with worktree diffs, line-ending-only changes hidden behind a chip, and inline notes that go outdated when the diff changes and can be sent back to the agent (RV-01, PL-03); change sets with confirm all or per repo, a merge order with dependency warnings you acknowledge, and merge requests that link to the earlier ones (RV-03, RV-04); local `--no-ff` merge that aborts on conflict and refuses a dirty checkout (RV-05); safe worktree removal that names whoever holds a locked file and lets you retry (RV-06, PL-03); `ygg doctor` warns when git is older than 2.38. Merge and removal are proposals you confirm; nothing is pushed by a local merge, and removal never deletes a branch.
- Agents and safety (Phase 3): worktrees with short names; Ask, Code, current-branch and whole-project modes with a per-turn mode switch; parallel agents with `maxAgents`; the agent board; composer with saved prompts and `@` mentions; transcript with sub-agent tree; draft buttons; proposals you confirm (commit, push, merge request, agent file, install, move-to-`_to_delete`) with the hash you saw; Yggdrasil-side path guard and command allowlist; Ask-mode integrity check; repo trust for agent configs; MCP server with per-task tokens; threat model (AG-06 to AG-15, AG-20 to AG-22, RV-02, GH-03, SF-06 to SF-09, MCP-01 to MCP-06). Write modes stay off until `YGG_WRITE_MODES` is set. OpenCode adapter is not part of this release.
- The UI now needs the per-launch link printed in the terminal (and opened in your browser); `--no-browser` or `YGG_NO_BROWSER=1` skip the opening (SF-01).
- Data model and first database migration: projects, repos, worktrees, tasks (with an optional parent task for sub-tasks), change sets, proposals, builds and an append-only audit log, in SQLite with a pure-Go driver (AG-23, SF-05, IN-03).
- `internal/platform`: process start inside Windows job objects (every child dies with `ygg`), ConPTY terminal sessions, port detection, editor and folder launch outside the job, long-path and WSL-path handling (PL-02, PL-04, PL-05, WE-04).
- `internal/safety`: clean child environment and token redaction for logs and the audit log (SF-04).
- `internal/git`: wrapper for the safe subset of git; force push, hard reset, branch deletion and clean cannot be called (SF-03).
- HTTP and WebSocket server on `127.0.0.1` with Host and Origin checks, a uniform error format, an allowlisted static file server and an audit entry for every task change (SF-01, SF-05).
- Agent adapter interface with normalized events (optional parent ID for nested sub-agents) and capability flags; a mock adapter with recorded sessions, including nested sub-agents; the Claude Code reference adapter with a hook that enforces Ask mode, the repo folder and sub-agent limits (AG-01 to AG-03, AG-21, AG-22, SF-06).
- Browser app: router, live status pill and notifications (task name and status only), `Ctrl+K` palette, a customizable widget grid ("Working now", "This machine"), a composer and a live task transcript with a tree of sub-agents (UI-02, UI-04, AG-14, AG-21).
- `ygg doctor` checks Git, the data folder, Claude Code and the optional toolchains and says what to do about what is missing (IN-04). `ygg seed <folder>` tracks a repo until the project pages arrive.
- `YGG_DATA_DIR`, `YGG_LOG` and `YGG_MOCK_DELAY_MS` settings (IN-06).
- Types are generated from Go into `web/src/api/gen` with `task generate` (`sqlc` and `tygo`).
- Architecture tests that fail when a process is started outside `internal/platform`, the environment is read outside `internal/safety`, or a package imports one it must not (docs/AGENTS.md section 5).
- Repo tracking API: browse folders, preview the git repos under a folder, track a project with the repos you pick, rescan, stop tracking a repo or project, rename a project. Stopping tracking hides the repo everywhere (agents included) but never touches files on disk, and tracking the same folder again restores it (WS-01). `ygg seed <folder>` now tracks the folder as a project with every repo found under it.
- Repo status API with live updates: branch, uncommitted changes, ahead/behind, last commit and health flags (missing folder, not a repo, detached, no upstream, conflicts, diverged), filters and counts per project (WS-02 to WS-04). Status reads never take git's index lock.
- Home page: a procedural scene that follows the time of day (or a fixed dawn, day, dusk, night, or off), with the composer on top and suggestion chips that fill it (UI-01).
- Themes: System, Harbour, Daylight, Dracula, Rosé Pine, Nord and Gruvbox, all checked for readable contrast (UI-05).
- `task smoke` (also in CI) checks the built `ygg.exe` on Windows: doctor, seed, a demo task, loopback and Host/Origin refusal, static files, the refusal of a repo with its own Claude Code configuration, and that killing `ygg` ends an agent's whole process tree; `task smoke:real` runs a short real Claude Code session (a sub-agent, the Ask-mode and repo-folder refusals, cancel).

- Repo page with changes, diffs, history, commit detail and branches; git actions (fetch, fast-forward-only pull, checkout, cross-repo branch switch) that never force, reset or delete (WS-05 to WS-07). Open a repo in your editor or show it in the folder view (WS-08).
- Git hosts: GitLab (several instances), GitHub and Gitea. Merge/pull request and pipeline status per repo, an MR inbox, tokens optional and read from `.env`; links from hosts are only shown when they are http(s) (GH-01, GH-02, GH-04).
- Builds: toolchain modules for Node, Maven (with dependency ordering), Gradle and custom commands; an ordered build queue with presets, live logs, history and artifacts (BL-01 to BL-07, BL-10, BL-11, BL-15). Draft of `docs/writing-a-toolchain.md`.
- Code search across tracked repos with `git grep`, literal or regex, file filters and editor links (XR-01).
- Home widgets: Git activity, Merge requests, Needs attention, CI failures, Builds and Recent areas (UI-03).
- `task fixtures` generates the demo project with extra repos and recorded host data; `task perf` checks that 50 repos refresh in under 10 seconds (WS-02).

### Changed
- Only Ask mode (read-only) can be started in this version; Code mode needs worktrees and the path guard (Phase 3).
- An agent will not run in a repo that has its own Claude Code configuration (settings, MCP servers, agent definitions) until repo trust exists (SF-09).
- Every state change and its audit entry are now written in one database transaction, and a change made in parallel is refused instead of overwriting the other (SF-05).
- Error responses say what to do next and never carry internal text; a stale change answers 409 (SF-01).
- A repo can't be untracked while a build or an agent task is running in it.
- The "light" theme is now called Daylight.

### Fixed
- Status refresh for 50 repos: about 8 s instead of 15 to 20 s, by reading the last commit from the git object store without a process (falls back to `git log` when unsure) and caching it by HEAD (WS-02).
- Terminal sessions now check a `.cmd` or `.bat` program's arguments the same way as other processes do. No child process on Windows looks for commands in its working folder, so a program planted in a worktree can't run instead of the real one (SF-02).
- WSL paths written with `\\.\UNC\`, `\??\` or `GLOBALROOT` device prefixes are refused like `\\wsl$` paths (PL-04).
- Live task status messages always arrive in order, so the browser no longer refetches on a gap that wasn't one.
- Stopping one process of a task no longer stops the task's other processes; ending the task still stops all of them.

## [0.1.0] - 2026-10-15

### Added
- `ygg` binary that serves the embedded browser UI on `127.0.0.1:4747`; `YGG_ADDR` changes the address and only accepts loopback IPs (IN-01).
- `ygg version` and `ygg help`.
- Empty browser app shell (top bar, theme switch for system/dark/light), built with Vite and embedded into `ygg` (IN-01, IN-02).
- Taskfile with build, dev, test, lint, typecheck and license tasks (IN-01, IN-02).
- Apache-2.0 `LICENSE` and `NOTICE` (D5).
- Deterministic demo-project generator (`task fixtures`): fictional `acme-shop` repos with history, branches, a dirty tree and a CRLF file (IN-05).
- Phase 0 spike findings in `docs/spikes.md` (ConPTY, job objects, pure-Go SQLite, agent streaming) and `task spikes:test` / `task spikes:test:race` for the throwaway spike modules (D4, D12, IN-03).
