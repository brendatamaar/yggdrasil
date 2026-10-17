# Changelog

All notable changes to Yggdrasil are documented here. The format follows [Keep a Changelog](https://keepachangelog.com/en/1.1.0/), and versions follow [Semantic Versioning](https://semver.org/). Requirement IDs refer to [docs/PRD.md](PRD.md).

## [Unreleased]

### Added
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
- `task smoke` (also in CI) checks the built `ygg.exe` on Windows: doctor, seed, a demo task, loopback and Host/Origin refusal, static files, the refusal of a repo with its own Claude Code configuration, and that killing `ygg` ends an agent's whole process tree; `task smoke:real` runs a short real Claude Code session (a sub-agent, the Ask-mode and repo-folder refusals, cancel).

### Changed
- Only Ask mode (read-only) can be started in this version; Code mode needs worktrees and the path guard (Phase 3).
- An agent will not run in a repo that has its own Claude Code configuration (settings, MCP servers, agent definitions) until repo trust exists (SF-09).

## [0.1.0] - 2026-10-15

### Added
- `ygg` binary that serves the embedded browser UI on `127.0.0.1:4747`; `YGG_ADDR` changes the address and only accepts loopback IPs (IN-01).
- `ygg version` and `ygg help`.
- Empty browser app shell (top bar, theme switch for system/dark/light), built with Vite and embedded into `ygg` (IN-01, IN-02).
- Taskfile with build, dev, test, lint, typecheck and license tasks (IN-01, IN-02).
- Apache-2.0 `LICENSE` and `NOTICE` (D5).
- Deterministic demo-project generator (`task fixtures`): fictional `acme-shop` repos with history, branches, a dirty tree and a CRLF file (IN-05).
- Phase 0 spike findings in `docs/spikes.md` (ConPTY, job objects, pure-Go SQLite, agent streaming) and `task spikes:test` / `task spikes:test:race` for the throwaway spike modules (D4, D12, IN-03).
