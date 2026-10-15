# Changelog

All notable changes to Yggdrasil are documented here. The format follows [Keep a Changelog](https://keepachangelog.com/en/1.1.0/), and versions follow [Semantic Versioning](https://semver.org/). Requirement IDs refer to [docs/PRD.md](PRD.md).

## [Unreleased]

## [0.1.0] - 2026-10-15

### Added
- `ygg` binary that serves the embedded browser UI on `127.0.0.1:4747`; `YGG_ADDR` changes the address and only accepts loopback IPs (IN-01).
- `ygg version` and `ygg help`.
- Empty browser app shell (top bar, theme switch for system/dark/light), built with Vite and embedded into `ygg` (IN-01, IN-02).
- Taskfile with build, dev, test, lint, typecheck and license tasks (IN-01, IN-02).
- Apache-2.0 `LICENSE` and `NOTICE` (D5).
- Deterministic demo-project generator (`task fixtures`): fictional `acme-shop` repos with history, branches, a dirty tree and a CRLF file (IN-05).
- Phase 0 spike findings in `docs/spikes.md` (ConPTY, job objects, pure-Go SQLite, agent streaming) and `task spikes:test` / `task spikes:test:race` for the throwaway spike modules (D4, D12, IN-03).
