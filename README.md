# Yggdrasil: a multi-repo workspace for code agents

Yggdrasil is a local server with a browser UI. It combines a multi-repo workspace (git status, builds, dev servers, merge requests, pipelines) with code agents that work in isolated git worktrees. It's one binary, `ygg`, that runs on `127.0.0.1` and keeps its state in SQLite.

> **Status: early development (Phase 0).** Nothing here is usable yet. See [docs/PLAN.md](docs/PLAN.md) for the roadmap and [docs/PRD.md](docs/PRD.md) for what we're building.

## Platform support

- **Windows:** supported and tested.
- **macOS and Linux:** experimental. They build and run, but aren't tested on every change. Contributions welcome.

## Build from source

Prerequisites:

- [Go](https://go.dev/dl/) 1.26 or newer (the exact toolchain is pinned in `go.mod` and downloaded automatically)
- [Node.js](https://nodejs.org/) 22 (see `.nvmrc`) and [pnpm](https://pnpm.io/)
- [Task](https://taskfile.dev/)

No C compiler is needed: Yggdrasil never uses cgo.

```sh
git clone https://github.com/brendatamaar/yggdrasil.git
cd yggdrasil
task build
./bin/ygg
```

Then open the link `ygg` prints in the terminal: `http://127.0.0.1:4747/?k=<token>`. Set `YGG_ADDR` to use another loopback port (see `.env.example`).

### Opening the UI (per-launch session)

Every start of `ygg` makes a new random launch token and keeps it in memory only. The link above trades the token for a session cookie (`ygg_session`, HttpOnly, SameSite=Strict) and then redirects to `/` without the token. Every `/api/` and `/ws` request needs that cookie; without it the server answers 401 `session_required`. Only `/api/health`, the MCP endpoint (it has its own per-task token) and the static page are open. A new start means a new token, so open the new link. If the page says a session is required, use the link from the terminal where `ygg` is running.

`task dev` sets `YGG_INSECURE_NO_SESSION=1` so the Vite proxy works without the cookie. `ygg` ignores that setting (and logs a warning) when `YGG_WRITE_MODES` is on. Do not use it outside development.

## Naming

The product is Yggdrasil; the binary and command are always `ygg`. It's unrelated to the Yggdrasil mesh network and other software with that name.

## Contributing

See [docs/CONTRIBUTING.md](docs/CONTRIBUTING.md) and [docs/AGENTS.md](docs/AGENTS.md).

## License

[Apache License 2.0](LICENSE). See also [NOTICE](NOTICE).
