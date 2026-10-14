# Yggdrasil: a multi-repo workspace for code agents

Yggdrasil is a local server with a browser UI. It combines a multi-repo workspace (git status, builds, dev servers, merge requests, pipelines) with code agents that work in isolated git worktrees. It's one binary, `ygg`, that runs on `127.0.0.1` and keeps its state in SQLite.

> **Status: early development (Phase 0).** Nothing here is usable yet. See [docs/PLAN.md](docs/PLAN.md) for the roadmap and [docs/PRD.md](docs/PRD.md) for what we're building.

## Platform support

- **Windows:** supported and tested.
- **macOS and Linux:** experimental. They build and run, but aren't tested on every change. Contributions welcome.

## Build from source

Prerequisites:

- [Go](https://go.dev/dl/) 1.25 or newer (the exact toolchain is pinned in `go.mod` and downloaded automatically)
- [Node.js](https://nodejs.org/) 22 (see `.nvmrc`) and [pnpm](https://pnpm.io/)
- [Task](https://taskfile.dev/)

No C compiler is needed: Yggdrasil never uses cgo.

```sh
git clone https://github.com/brendatamaar/yggdrasil.git
cd yggdrasil
task build
./bin/ygg
```

Then open <http://127.0.0.1:4747>. Set `YGG_ADDR` to use another loopback port (see `.env.example`).

## Naming

The product is Yggdrasil; the binary and command are always `ygg`. It's unrelated to the Yggdrasil mesh network and other software with that name.

## Contributing

See [docs/CONTRIBUTING.md](docs/CONTRIBUTING.md) and [docs/AGENTS.md](docs/AGENTS.md).

## License

[Apache License 2.0](LICENSE). See also [NOTICE](NOTICE).
