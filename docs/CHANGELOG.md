# Changelog

All notable changes to Yggdrasil are documented here. The format follows [Keep a Changelog](https://keepachangelog.com/en/1.1.0/), and versions follow [Semantic Versioning](https://semver.org/). Requirement IDs refer to [docs/PRD.md](PRD.md).

## [Unreleased]

### Added
- `ygg` binary that serves the embedded browser UI on `127.0.0.1:4747`; `YGG_ADDR` changes the address and only accepts loopback IPs (IN-01).
- `ygg version` and `ygg help`.
- Empty browser app shell (top bar, theme switch for system/dark/light), built with Vite and embedded into `ygg` (IN-01, IN-02).
- Taskfile with build, dev, test, lint, typecheck and license tasks (IN-01, IN-02).
- Apache-2.0 `LICENSE` and `NOTICE` (D5).
