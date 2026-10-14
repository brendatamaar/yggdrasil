# Phase 0 — Spikes and project setup → `v0.1.0`

> Status: **plan, awaiting approval** · 2026-10-14
> Source: docs/PLAN.md Phase 0; docs/PRD.md IN-01–IN-08, PL-01–PL-05, D5, D12, D14; docs/AGENTS.md sections 2–5.

Module path: `github.com/brendatamaar/yggdrasil` (resolves PRD open question 1 for the repo; the domain is still open). Binary: `ygg` (`ygg.exe`).

**Exit criteria (from PLAN.md):** all four spikes pass on Windows; CI green; `task build` on a clean Windows machine with only Go and Node installed produces a binary that serves an empty UI.

---

## Blockers to resolve before starting (need your answer)

1. **Subagents — resolved (with adaptations).** repo-hub subtasks (`propose_subtasks`) give each task its own worktree and branch, started from **committed** `main`. `propose_subtask_merge` does a local `git merge --no-ff` after your confirmation. That's the CLAUDE.md workflow, with these differences:
   - The `.claude/agents/*` files aren't applied automatically. Each subtask prompt embeds the rules, done criteria and report format from its agent file, and starts with "act as `<agent>` per `.claude/agents/<agent>.md`".
   - Model choice (sonnet) can't be set per subtask.
   - `safety-reviewer` runs as a read-only `researcher` agent with the reviewer brief.
   - Still max 3 at once (CLAUDE.md), even though repo-hub allows 6.
   - Each start, commit and merge is a card for you to confirm.
2. **Shell — partly resolved.** Single commands work; chained commands (`a; b`) are denied, so I run checks one by one. Toolchain on this machine:
   - Go **1.23.2**: too old. It's past end of support, and current `modernc.org/sqlite`, goose and golangci-lint v2 need a newer Go. **Proposal:** install current stable Go. A `toolchain` line in `go.mod` would make Go auto-download it, but explicit is better.
   - Node **22.22.3**: OK. `.nvmrc` pinned to `22` (LTS until 2027-04). Say if you'd rather use 24.
   - pnpm **11.4.0**: OK.
   - Re-check (2026-10-14): Task and golangci-lint **v2.14.0** are now installed via winget, but this session's PATH predates the install, so neither is found. Restart repo-hub (or the session) to pick up the new PATH.
   - Go: the system install is still 1.23.2, but the module cache already has the auto-downloaded toolchain **go1.25.11**. Pinning `go 1.25` / `toolchain go1.25.11` in `go.mod` uses it with no download, provided `GOTOOLCHAIN` is the default `auto`.
3. **Author identity — resolved, no leak.** The repo-local git identity is the owner's public one and overrides any global identity. Both commits on `main` (`3880ee1`, `5f8ab7d`) use it; the first was committed through GitHub's web UI. Note that `main` was rewritten since the first plan (HEAD is now `5f8ab7d`), and `LICENSE` (Apache-2.0) is already committed, so A3 only adds `NOTICE`.
4. **`-race` needs cgo.** On Windows, `go test -race` needs `CGO_ENABLED=1` and gcc, which conflicts with `CGO_ENABLED=0` everywhere.
   **Proposal:** `task test` = `CGO_ENABLED=0 go test ./...` (proves the pure-Go build); `task test:race` = `CGO_ENABLED=1 go test -race ./...`, for test runs only, never for shipped binaries. CI Windows runs both, using the runner's preinstalled MinGW gcc. Product builds stay `CGO_ENABLED=0`. AGENTS.md sections 3 and 8 get updated to match.
5. **License allowlist gaps.** Common transitive npm licenses (`0BSD` e.g. tslib, `CC0-1.0`, `BlueOak-1.0.0`, `Unlicense`) aren't in the AGENTS.md list.
   **Proposal:** check **production** dependencies only (what ships in the binary); add `0BSD`, `CC0-1.0`, `BlueOak-1.0.0` and `Unlicense` to the allowed list in AGENTS.md section 2 and PRD 5.1. Otherwise the first CI run fails on the scaffold.
6. **NOTICE holder.** **Proposal:** `Copyright 2026 The Yggdrasil Authors`, with no personal name.
7. **Default port.** **Proposal:** `127.0.0.1:4747`, overridable with `YGG_ADDR`.

---

## Structure decisions (lead)

- **Untracked `docs/` and `.claude/` are committed first (A0).** Worktrees branch from `main`; without this, subagents can't see AGENTS.md or this plan.
- **Each spike is its own Go module** (`spikes/<name>/go.mod`). This keeps throwaway dependencies out of the root `go.mod`/`go.sum`, so parallel tasks never share those files, and `go test ./...` at the root skips them. `task spikes:test` runs the non-interactive spike tests.
- **Spike findings go in the subagent's final report.** I consolidate them into `docs/spikes.md` during integration, so no parallel task owns a shared doc.
- **go:embed contract owned by the lead (Batch A):** `web/embed.go` (`package web`, `//go:embed all:dist`) plus `web/dist/.gitkeep`, so `cmd/ygg` builds before the frontend exists. `cmd/ygg` serves the embedded FS and shows a "frontend not built — run `task build`" page if `index.html` is missing. The frontend task fills `web/` and must not touch `web/embed.go` or `cmd/ygg`.
- **Taskfile ↔ frontend contract:** web tasks call `pnpm -C web run <script>`; `web/package.json` must expose the scripts `build`, `dev`, `lint`, `typecheck` and `test`. Until `web/package.json` exists, these tasks print "web/ not scaffolded yet" and exit 0.
- **Lint-enforced architecture rules** (`.golangci.yml`, `forbidigo`): `exec.Command*` and `runtime.GOOS` are banned outside `internal/platform`, and `os.Environ` outside `internal/safety` (AGENTS.md 5.2/5.3). Spikes and the demo generator are separate modules, so they're outside this lint.
- **Demo generator uses `go-git`** (pure Go, Apache-2.0) in its own module `fixtures/demo-gen/`. It creates repos without spawning `git`, so there's no `exec.Command` outside platform. Output `fixtures/demo-project/` is gitignored and regenerated by `task fixtures`.
- **Extra tasks beyond AGENTS.md section 3:** `build:web`, `test:race`, `fixtures`, `license`, `spikes:test`, `release:check`. AGENTS.md section 3 gets updated in A1.

---

## Batch A — lead, sequential, committed to `main` before Batch B

| # | Task | PRD | Owns | Definition of done |
|---|---|---|---|---|
| A0 | Commit existing `docs/` + `.claude/` (after blocker 3) | — | `docs/**`, `.claude/**` (as-is) | On `main`. |
| A1 | Skeleton + Go module: `go.mod` (`github.com/brendatamaar/yggdrasil`, `go` + `toolchain` pinned to current stable); `cmd/ygg/main.go` (bind `127.0.0.1:4747` / `YGG_ADDR`, serve embedded UI, `ygg version`); `web/embed.go` + `web/dist/.gitkeep`; `doc.go` per top-level `internal/` package from AGENTS.md section 4; `.nvmrc` (current Node LTS); `.golangci.yml`; update AGENTS.md section 3 | IN-01, IN-02, IN-03, PL-02, D14 | `go.mod`, `go.sum`, `cmd/ygg/**`, `web/embed.go`, `web/dist/.gitkeep`, `internal/*/doc.go`, `.nvmrc`, `.golangci.yml`, `docs/AGENTS.md` (section 3 only) | `CGO_ENABLED=0 go build ./...` and `go vet` pass; `GOOS=darwin` and `GOOS=linux go vet ./...` pass; `golangci-lint run` clean; no binary/process named other than `ygg`. |
| A2 | `Taskfile.yml`: `build` (`build:web`, then `CGO_ENABLED=0 go build -o bin/ygg{{exeExt}} ./cmd/ygg`), `build:web`, `run`, `dev` (Go server + `pnpm -C web dev` with proxy), `generate` (stub, gets sqlc/tygo in Phase 1), `test`, `test:race`, `test:web`, `test:e2e` (stub until Playwright), `lint`, `typecheck`, `fixtures`, `license`, `spikes:test`, `release:check` | IN-01 | `Taskfile.yml` | Every task runs on Windows (stubs exit 0 with a message); `task build` produces `bin/ygg.exe` showing the "not built" page. |
| A3 | Legal + docs: Apache-2.0 `LICENSE`, `NOTICE`; extend `.gitignore` (`bin/`, `web/dist/*` with `!web/dist/.gitkeep`, `fixtures/demo-project/`, `dist/` for GoReleaser); `README.md` skeleton ("Yggdrasil: a multi-repo workspace for code agents", platform statement: Windows supported/tested, macOS and Linux experimental, build from source, license); `docs/CHANGELOG.md` (Keep a Changelog, Unreleased); `docs/CONTRIBUTING.md` (prereqs, tasks, conventional commits + PRD IDs, no cgo, license allowlist, mock data only); `.env.example` skeleton (`YGG_ADDR`, `YGG_DATA_DIR`, host tokens: all optional, commented, no real values) | D5, PL-01, IN-06, IN-07 | `LICENSE`, `NOTICE`, `.gitignore`, `README.md`, `docs/CHANGELOG.md`, `docs/CONTRIBUTING.md`, `.env.example` | README is the only root Markdown; no real hosts, users or paths anywhere. |
| A4 | CI `.github/workflows/ci.yml`. **Windows (required):** setup-go (`go.mod`), setup-node (`.nvmrc`), pnpm, Task, golangci-lint; the toolchains tests will need (Java + Maven + Gradle, .NET SDK, Python + uv + Poetry); `CGO_ENABLED=0`: `task generate` + `git diff --exit-code`, `task lint`, `task typecheck`, `task test`, `task test:web`, `task build`, `task spikes:test`; `task test:race` with cgo (blocker 4). **macOS + Linux:** same Go/web steps, `continue-on-error: true`. **License job:** `task license` = `go-licenses check` (prod Go deps) + `license-checker-rseidelsohn --production --onlyAllow` on `web/`. Actions pinned to commit SHAs; `permissions: contents: read`. | PL-01, IN-03, D5 | `.github/workflows/**` | YAML valid; runs green after Batch B merges (push needs your approval). |
| A5 | `.goreleaser.yaml` (v2): `before` hook `task build:web`; builds `ygg` for windows/darwin/linux × amd64/arm64 with `CGO_ENABLED=0` and version ldflags; archives include LICENSE + NOTICE; **`release: disable: true`**, no publishers | IN-08 | `.goreleaser.yaml` | `task release:check` (`goreleaser check` + `release --snapshot --clean`) passes locally; nothing is published. |

After A0–A5 I propose a commit per step (only my files) and wait for your confirmation before Batch B.

---

## Batch B — parallel, max 3 at once

Two waves. Wave 1 starts with the riskiest items (ConPTY, job objects) and the frontend, which is on the critical path for the exit criteria. No two tasks in a wave own the same path.

### Wave 1

#### B1 — ConPTY spike · `platform-windows`
- **PRD:** D12, PL-02, DV-04 (prep).
- **Owns:** `spikes/conpty/**` (own `go.mod`; xterm.js installed with pnpm into `spikes/conpty/web/`, lockfile committed, no CDN).
- **Must not touch:** root `go.mod`, `internal/`, `web/`, `cmd/`.
- **Work:** `aymanbagabas/go-pty` first. A small server on `127.0.0.1` streams a PTY to xterm.js over `coder/websocket`. Check: PowerShell and cmd, resize, an interactive program (e.g. `python` REPL or `choice`), clean exit with exit code, and closing the browser tab kills the PTY. If any check fails, build the fallback (thin ConPTY wrapper on `x/sys/windows`; `creack/pty` on Unix) behind the same small interface and compare.
- **Done:** `CGO_ENABLED=0 go build` and `go vet` pass (plus `GOOS=linux go vet`); one automated test (start a shell, write `echo`, read the output back, exit); report covers each check pass/fail, the library verdict, and a numbered manual checklist (browser steps).

#### B2 — Job object spike · `platform-windows`
- **PRD:** PL-02, PL-05.
- **Owns:** `spikes/jobobject/**` (own `go.mod`).
- **Must not touch:** root `go.mod`, `internal/`, `web/`, `cmd/`.
- **Work:** a parent process creates a job with `JOB_OBJECT_LIMIT_KILL_ON_JOB_CLOSE` and starts a process tree (a Go helper that spawns grandchildren, plus a Node `npm run dev`-style chain if Node is present). It assigns children race-free (`CREATE_SUSPENDED` → assign → resume, or the `PROC_THREAD_ATTRIBUTE_JOB_LIST` approach; document which). It also launches an "editor" (e.g. `notepad.exe`) **outside** the job (`CREATE_BREAKAWAY_FROM_JOB` or launching from a non-job parent; document what works when Yggdrasil itself already sits inside a job, e.g. under Windows Terminal or CI).
- **Done:** automated test: start the tree, kill the parent with `TerminateProcess`, assert every child PID is gone, and the outside-job process survives (then clean it up). Context cancel stops the tree. Manual checklist: kill via Task Manager → all children gone; notepad stays open.

#### B5 — Web scaffold · `frontend`
- **PRD:** IN-01, IN-02, IN-03.
- **Owns:** `web/**` **except** `web/embed.go` and `web/dist/.gitkeep`.
- **Must not touch:** `cmd/`, `internal/`, `Taskfile.yml`, root `go.mod`.
- **Work:** Vite + React + TypeScript (`strict`, `noUncheckedIndexedAccess`), pnpm with a committed `pnpm-lock.yaml` and `packageManager` field; TanStack Query + Router (root route + index route), Zustand (one UI store, e.g. theme), Tailwind, Radix (one primitive used, e.g. Tooltip); ESLint (flat config, typescript-eslint strict); Vitest with one test. It serves an empty shell (top bar "Yggdrasil", empty main area). Vite `dev` proxies `/api` and `/ws` to `127.0.0.1:4747`; `build` outputs to `web/dist`. Scripts: `build`, `dev`, `lint`, `typecheck`, `test`. All deps under the allowed licenses, with no native/postinstall builds.
- **Done:** `task lint`, `task typecheck`, `task test:web`, `task build` pass; `bin/ygg.exe` serves the shell at `http://127.0.0.1:4747`, and deep links fall back to `index.html`; `task license` passes for web.

### Wave 2 (starts after Wave 1 is merged and reviewed)

#### B3 — Pure-Go SQLite spike · `go-backend`
- **PRD:** IN-03, D4.
- **Owns:** `spikes/sqlite/**` (own `go.mod`, `sqlc.yaml`, `migrations/`, `queries/`, generated `gen/`).
- **Must not touch:** root module, `internal/db`.
- **Work:** `modernc.org/sqlite` + goose (embedded migrations, `goose.SetDialect("sqlite3")` with the modernc driver name) + sqlc (engine `sqlite`; sqlc runs as a tool via `go run`, or document an install that needs no cgo). Two tables (`projects`, `repos` FK), insert/select through sqlc, WAL mode + `busy_timeout`, concurrent writers with `-race` (via `test:race`), and a DB path with spaces.
- **Done:** `CGO_ENABLED=0 go test ./...` passes on Windows; `go version -m` on the built binary shows `CGO_ENABLED=0`; report covers pragmas, driver name, and sqlc version/installation notes for Phase 1.

#### B4 — Agent streaming spike · `go-backend` · ⚠️ real Claude Code CLI, needs your go-ahead to run
- **PRD:** AG-01, AG-02, AG-03, AG-21, AG-22, D15.
- **Owns:** `spikes/agentstream/**` (own `go.mod`).
- **Must not touch:** root module, `internal/agents`.
- **Work, split in two parts:**
  1. *Subagent (no CLI calls):* a harness that runs `claude -p --output-format stream-json --verbose --forward-subagent-text` in a **scratch git repo it creates in a temp dir** (fictional content, never this repo); parses events line by line into a draft normalized `Event` (with `ParentID` from `parent_tool_use_id`); cancels mid-run via context and checks that the process tree is gone (job object code copied from B2's findings). It writes a `--settings` file with a `PreToolUse` hook (a tiny Go hook binary in the spike) that logs every tool call and **denies a sub-agent spawn whose input has `isolation: worktree`**. It sets the sub-agent concurrency/depth env vars, with the exact names taken from current Claude Code docs or `claude --help` (don't guess; record the source). Cheapest model, small `--max-budget-usd`, clean env (no tokens other than what the CLI needs). Unit tests run only on synthetic JSONL.
  2. *Live run (after merge, only after you approve in that session):* I or you run the harness and record (a) `parent_tool_use_id` on sub-agent messages, (b) `PreToolUse` firing for sub-agent tool calls, (c) depth/concurrency caps respected, (d) `isolation: worktree` blocked, (e) cancel kills the tree. Captured streams are scrubbed (no paths, usernames or session IDs) and saved as candidate fixtures for the Phase 1 mock adapter.
- **Done:** part 1: `go vet` and synthetic-parse tests pass, harness builds with `CGO_ENABLED=0`, nothing invokes `claude` in tests. Part 2: checks (a)–(e) pass/fail recorded in `docs/spikes.md`.

#### B6 — Demo-project generator · `test-engineer`
- **PRD:** IN-05.
- **Owns:** `fixtures/demo-gen/**` (own `go.mod`, `go-git`).
- **Must not touch:** root module, `Taskfile.yml` (`task fixtures` is already wired to `go -C fixtures/demo-gen run . -out fixtures/demo-project`).
- **Work:** a deterministic generator (fixed seed, fixed author dates) for project `acme-shop` with `shop-api` (Maven layout: `pom.xml`, `mvnw` wrappers as text stubs), `shop-web` (Node/pnpm: `package.json` + lockfile stub) and `shop-admin` (Node). Each repo gets 15–30 commits by 3–4 fictional authors (`@example.com`), `main` + 2–3 feature branches (one ahead, one diverged), and a fictional `origin` remote as a local bare repo under the output dir. There's a dirty working tree in one repo and a CRLF file in one repo. Re-running replaces the output **only inside `-out`**, and refuses if `-out` doesn't end in `demo-project`.
- **Done:** `go test ./...` in the module passes (generates into `t.TempDir()`, asserts commit counts, branches, authors, determinism of commit hashes across runs); `CGO_ENABLED=0` build; no real names, hosts or paths.

### Integration (lead)
For each finished task, one at a time: review the report and diff, `git merge --no-ff`, then run `task generate`, `task lint`, `task typecheck`, `task test`, `task test:web` (+ `task spikes:test`). After each wave: `safety-reviewer` on `git diff <wave-start>..main` until PASS. Then `docs/spikes.md`, PLAN.md checkboxes, CHANGELOG.

## Dependencies

```
A0 → A1 → A2 → A3 → A4 → A5 → [commit to main]
                                   ├─ Wave 1: B1, B2, B5   (parallel)
                                   └─ Wave 2: B3, B4(part 1), B6   (parallel, after Wave 1 merged)
                                                └─ B4 part 2 (live run, your approval)
CI green ← B5 merged (web lint/typecheck/test have real content)
```

## Phase-close manual checklist (preview)
1. ConPTY in the browser: PowerShell and cmd, resize, interactive program, clean exit (B1).
2. Kill the spike parent in Task Manager → no orphan children; notepad survives (B2).
3. Agent spike live run (B4 part 2).
4. Clean Windows machine with only Go + Node (+ corepack pnpm, Task): `git clone`, `task build`, `bin\ygg.exe` → empty UI on `127.0.0.1:4747`.
5. CI run on GitHub is green on Windows (needs a push, with your approval).

## Decisions / deviations log
- Each spike is a separate Go module (not in PLAN/AGENTS); findings are consolidated by the lead.
- `web/embed.go` is a Go file inside `web/`, owned by the lead as a contract.
- `fixtures/demo-gen` uses go-git instead of the git CLI, to avoid `exec.Command` before `internal/platform` exists.
- Blockers 4–7 accepted as proposed (2026-10-14). Blockers 1, 2 and 3 resolved.
- Blocker 2 resolved (2026-10-14): Task 3.54.0 and golangci-lint 2.14.0 are on PATH; `GOTOOLCHAIN=auto`, so `go.mod` pins `go 1.25` / `toolchain go1.25.11` (cached, works offline).
- Batch B runs as repo-hub subtasks instead of `.claude/agents` worktree subagents (blocker 1).
- A2: `task license` and `task release:check` call installed `go-licenses` / `goreleaser` binaries instead of `go run …@version`. The lead session has `GOPROXY=off`, so versions can't be resolved here. CI installs pinned versions (A4).
- A2: npm license check is `scripts/check-npm-licenses.mjs` (new top-level `scripts/` dir, added to AGENTS.md section 4).
- A2: `task test` also runs `fixtures/demo-gen` tests when that module exists.
- A2: `task fixtures` passes an absolute `-out` (`{{.ROOT_DIR}}/fixtures/demo-project`), because `go -C` changes the working directory.
- A4: actions are pinned to **major tags**, not commit SHAs: SHAs can't be resolved offline, and inventing them would break CI. `.github/dependabot.yml` keeps action versions current; switching to SHA pins is a follow-up once CI is green.
- A4: Task v3 lets OS env override Taskfile `env:`, so `CGO_ENABLED` is set inline on each go command (`CGO_ENABLED=0 go build …`, `CGO_ENABLED=1 go test -race …`). CI doesn't set it at job level.
- A4: `.gitattributes` forces LF (CRLF for `.cmd`/`.bat`/`.ps1`). Windows runners default to `core.autocrlf=true`, which would make gofmt flag every Go file.
- A4: `task license:install` installs the pinned `go-licenses`; CI uses it, so the version lives only in the Taskfile.
- A4: pnpm setup in CI is skipped until `web/package.json` exists, and then reads `packageManager` from it. **B5 must set `"packageManager": "pnpm@<version>"`.**
- A4: CI toolchains: Temurin 21, Gradle via setup-gradle, Maven from the runner image, .NET 8, Python 3.13, uv, Poetry (pipx). Test-time only; tighten versions when the toolchain modules land.
- A5: `.goreleaser.yaml` (v2): six `CGO_ENABLED=0` targets, archives with LICENSE/NOTICE/README, `release.disable: true`, changelog disabled (CHANGELOG.md is the source). GoReleaser isn't installed, and the lead session can't run it, so `task release:check` is unverified locally. A `release-config` CI job runs `goreleaser check` instead. All six targets were cross-compiled with `go build -trimpath` into `bin/release-check/` (ignored; safe to delete).
- Batch A complete (2026-10-14): A0 `7046b28`, A1 `da44d46`, A2 `8a62e08` + `521a408`, A3 `449a41c`, A4 `b444055`, A5 pending commit.
- Wave 1 (2026-10-14): B2 proposed as a subtask. B1 and B5 are blocked offline: the module cache lacks `aymanbagabas/go-pty`, `coder/websocket` and `creack/pty`; the pnpm store lacks React, TanStack, Zustand, Radix, typescript-eslint, `@vitejs/plugin-react` and xterm. They wait for network access or a prefetch.
- Wave 1 deps (2026-10-14), fetched by the lead via `propose_dependencies` and committed to `main` before B1/B5 start:
  - `spikes/conpty`: go-pty v0.2.2, coder/websocket v1.8.12, creack/pty v1.1.24 (+ u-root v0.11.0, x/crypto v0.17.0, x/sys v0.16.0).
  - `web/`: `@latest` resolved and saved exact. React 19.3.0, TanStack Query 5.104.2 / Router 1.170.44, Zustand 5.0.15, Radix Tooltip 1.4.1, xterm 6.0.0 + addon-fit 0.11.0; Vite 8.3.4 (Node ^20.19 || >=22.12, OK with `.nvmrc` 22), plugin-react 6.1.2, Tailwind 4.3.3, ESLint 10.12.0, typescript-eslint 8.71.2, Vitest 5.0.4, jsdom 30.1.2, TypeScript 5.9.3.
  - `web/package.json` scripts are placeholders until B5.
- `web/pnpm-workspace.yaml` makes `web/` its own pnpm workspace root. A `pnpm-workspace.yaml` in a parent folder (here, the user's home) otherwise captures the install and writes the lockfile there.
- B1 outcome (2026-10-14, human approved): **own ConPTY wrapper** in Phase 1 `internal/platform`. It's built on `x/sys/windows` (`CreatePseudoConsole` + attribute list + `CreateProcess`, keeping the thread handle for suspended job assignment, a required clean env, close order console then drain then pipes), with `creack/pty` on Unix. go-pty is dropped. Reasons are in PRD D12. B1's `PTY` interface carries over. Reporting the leak upstream to go-pty is optional and the human's call.
- Wave 1 merged (2026-10-14): B2 `bd0cd66`, B1 `f0bb127`, B5 `051c6b4`.
  - After B5: `task generate`, `lint` (golangci 0 issues + ESLint clean), `typecheck` (`tsc -b`), `test`, `test:web` (5/5) and `build` all pass; the tree is clean after the build (`web/dist/.gitkeep` kept).
  - Spike tests aren't re-run by the lead: the session shell blocks `go -C` and `task spikes:test`. Subtasks ran them; CI runs them.
- Wave 1 safety review (researcher acting as safety-reviewer): **FAIL → fixed.**
  - The one Blocking item was the author's name and email in this file (blocker 3 note); removed. It still exists in earlier commits of this file; rewriting history is the human's call.
  - Fixed now:
    - the spike test cleanup kills a surviving shell;
    - PRD 5.1, AGENTS.md section 2/9 and PLAN risks no longer name go-pty;
    - IN-03 wording clarified (prebuilt npm platform binaries allowed, no install-time native builds);
    - `web/src/stores/` added to AGENTS.md section 4.
  - Carried into Phase 1 (recorded in docs/spikes.md):
    - clean env required for every spawn, including editors;
    - always attach a job, with no `NoJob` path;
    - `BREAKAWAY_OK` never on task/build/agent jobs; per-child job assignment instead of self-assign;
    - a single-use socket token for terminals (DV-04);
    - static assets served from an allowlist.
  - Kept as is: `@xterm/xterm` and `@xterm/addon-fit` are in `web` dependencies on purpose (requested for the terminal dock, DV-04, used by the B1 spike); `brendatamaar` in module paths is the public repo owner.
- Go 1.26.9 (2026-10-14, human approved, `1b9c500`): Go 1.27 is out, so 1.25 is unsupported, and current `modernc.org/sqlite`, goose, sqlc and go-git need 1.26. Root and all spike/fixture modules are on `go 1.26` / `toolchain go1.26.9` (cached, works offline).
- Wave 2 deps (`1b9c500`): `spikes/sqlite` gets modernc.org/sqlite v1.60.1, goose v3.29.0 and sqlc v1.31.1 (it uses `wasilibs/go-pgquery` + wazero, so no cgo); `fixtures/demo-gen` gets go-git v5.19.3. `task test` skips demo-gen until it has Go code.
- A2 verification: `build`, `generate`, `test`, `lint`, `typecheck` and `test:web` were run in the lead session. `test:race`, `test:e2e`, `fixtures`, `spikes:test`, `license`, `release:check`, `run` and `dev` are blocked by the lead session's shell allowlist and need a manual run or CI.
