# Contributing to Yggdrasil

Thanks for helping. Before you start, read:

- [PRD.md](PRD.md): what we're building, with requirement IDs (e.g. `AG-07`).
- [PLAN.md](PLAN.md): the current phase.
- [AGENTS.md](AGENTS.md): architecture rules, safety invariants and conventions. These apply to humans and coding agents alike.

## Setup

You need Go 1.25+, Node.js 22 (`.nvmrc`) with pnpm, Task and golangci-lint v2. No C compiler: cgo is never used.

```sh
task build        # frontend + embed + bin/ygg
task dev          # Go server + Vite dev server
task --list       # all tasks
```

## Before you open a pull request

Run these and make sure they all pass, with generated files up to date:

```sh
task generate
task lint
task typecheck
task test
task test:web
```

## Rules that matter most

- **Windows first.** Windows is the supported platform; macOS and Linux must compile and pass `go vet`. All OS-specific code goes in `internal/platform`.
- **No cgo.** `CGO_ENABLED=0` must always build. Don't add dependencies that need cgo.
- **Licenses.** Dependencies must be MIT, BSD, ISC, Apache-2.0, MPL-2.0, 0BSD, CC0-1.0, BlueOak-1.0.0 or Unlicense. `task license` checks them. Explain every new dependency in your PR.
- **Safety invariants** (AGENTS.md section 6) are never weakened. If your change touches that area, say so in the PR description.
- **Mock data only.** Fixtures, screenshots and examples use fictional projects, people and hosts (e.g. `acme-shop` with `shop-api`, `shop-web`, `shop-admin`). Never commit real hostnames, usernames, paths or tokens.
- **Docs** go in `docs/`. `README.md` is the only Markdown file at the root.

## Commits and pull requests

- Conventional commits with requirement IDs: `feat(agents): add plan mode (AG-17)`.
- One logical change per pull request.
- Commit generated code together with its source.
- Update [CHANGELOG.md](CHANGELOG.md) for user-visible changes and tick [PLAN.md](PLAN.md) checkboxes when a task is done.
- If a requirement changes, update [PRD.md](PRD.md) in the same pull request.

## License

By contributing, you agree that your contributions are licensed under the [Apache License 2.0](../LICENSE).
