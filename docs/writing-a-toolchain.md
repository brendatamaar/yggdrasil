# Writing a toolchain

A toolchain is one language ecosystem (BL-10). Each lives in
`internal/builds/toolchains/<name>/` and implements `builds.Toolchain`
(`internal/builds/toolchain.go`). The Node module
(`internal/builds/toolchains/node/node.go`) is the worked example below.

## Rules

- **Detection reads files only.** `Detect(repo fs.FS)` never runs a command and
  never touches the network. Read each file once and keep what later steps need
  in `Detection.Facts` (plain text keys and values).
- **Commands go through the project's own runner.** Put the runner name in
  `Argv[0]` (`npm`, `mvnw`, `gradlew`). `internal/platform` resolves it at run
  time (`mvnw.cmd` vs `./mvnw`, `npm.cmd`). Never resolve executables or build
  OS paths in the module.
- **Modules never import each other.** Shared helpers live in
  `toolchains/internal/find`.
- **A repo can match several modules.** Spring backend plus a `frontend/`
  folder returns both; `toolchains.DetectAll` collects every match.
- **Every command is overridable** in the repo's build settings; the module only
  supplies defaults. The `custom` module covers ecosystems without a module.
- **Verify is the fast chain** (docs/PRD.md section 6.3): each step only if the
  repo has it configured, no step that needs a database or network services.
  `core/builds.Step` stops the chain at the first failure.

## Bootstrap, Env, verify and full tests

- **Bootstrap** (WE-05) prepares a task's own worktree before its first
  verification: restore dependencies, download the wrapper distribution. It runs
  automatically, but only inside a task worktree, only for a trusted repo, and
  through `platform.Start` with the clean environment. It never replaces
  **Install**, which changes the main checkout and stays a proposal. Give
  Bootstrap steps plain-language names ("Download Maven and dependencies"): the
  UI shows them as "bootstrap", apart from the verify chain.
  - Node: the Install command for the lock file (`npm ci`, `pnpm install
    --frozen-lockfile`, `yarn install --frozen-lockfile`, or `npm install`).
  - Maven: `<runner> -B -ntp dependency:go-offline`. It is chosen over
    `dependency:resolve` because that fails in a multi-module reactor when a
    module needs a sibling that is not installed.
  - Gradle: `<runner> --console=plain dependencies`, which downloads the
    distribution and resolves the root project's configurations.
  - Custom: `bootstrap` in the custom config, same shape and validation as
    `install`.
- **Env.** `Step.Env` is extra `KEY=VALUE` environment for one step, added to the
  clean environment (never `os.Environ`). Node's verify steps carry `CI=true` so
  that tools do not watch or prompt. Token-like names are refused.
- **Verify chain** (docs/PRD.md section 6.3): Node typecheck, lint, test;
  Maven `test-compile`; Gradle `compileJava compileTestJava`, plus `compileKotlin
  compileTestKotlin` when a build file mentions Kotlin.
- **Full tests are opt-in.** A toolchain lists its slow test run in
  `Commands.FullTests`. A repo's build settings (`fullTests: true`) append it
  after the verify chain; Node has none because its chain already runs the tests.
- **Per-repo settings.** `builds.RepoBuildSettings` holds `VerifyOverride`,
  `FullTests` and `Bootstrap` (default true). `builds.ResolveCommands(det, cmds,
  settings)` applies them to a toolchain's defaults. Modules do not read
  settings.
- **DepsFiles.** Implement `builds.DepsFiler` (`DepsFiles(d) []string`) to list
  the manifests and lock files that decide the dependencies, relative to the repo
  with forward slashes. `builds.HashDeps` hashes them so that a bootstrap runs
  again only when they change. List a lock file even when it is absent, so that
  adding one changes the hash.

## Steps

1. Create the package and a `Toolchain` type with `Name()`, `Detect`,
   `Commands` and `Artifacts`.
2. Add it to `All()` in `internal/builds/toolchains/all.go`.
3. Optionally implement `builds.Orderer` (`Modules`) when repos depend on each
   other (Maven does, for BL-04). Return coordinates only, leave `RepoID`
   empty; `corebuilds.OrderModules` does the ordering and keeps cycles together.
4. Write tests and fixtures (below).

## Worked example: Node

**Detect.** `find.Dir` looks for `package.json` in the repo root, then in
immediate sub folders. The package manager comes from the lock file:
`pnpm-lock.yaml`, `yarn.lock`, `package-lock.json`, else `npm`. The names of the
`scripts` go into `Facts["scripts"]`. Unreadable, oversized (> 4 MiB) or invalid
JSON means no match.

**Commands.**

| Purpose | Step |
|---|---|
| Install (a proposal, never automatic) | `npm ci` / `pnpm install --frozen-lockfile` / `yarn install --frozen-lockfile`; `npm install` when there is no lock file |
| Bootstrap (automatic, task worktree only) | the same command as Install |
| Verify | `<pm> run typecheck`, `lint`, `test` (each only if the script exists, each with `Env: CI=true`); if none exists, `build` |
| Build | `<pm> run build` |
| Per-environment builds | `node.EnvBuilds(d)` returns `build:<env>` scripts as named steps |
| Dev server | `dev`, else `start` |

**Dependency files.** `package.json` and the lock file of the detected package
manager (listed even when absent).

**Artifacts.** `dist/**`, `build/**`, `out/**`, prefixed with `Detection.Dir`
when the manifest is in a sub folder (forward slashes, relative to the repo).

## Worked examples: Go, .NET and Python

The three Phase 5 modules follow the same shape as Node; what differs:

- **Go** (`golang`): detected by `go.mod`. Verify is `go vet ./...` then `go build ./...` (tests run only with the full-tests opt-in); bootstrap is `go mod download`; the dev server is `go run .` (no port injection, so a program that ignores `PORT` can collide and the UI warns).
- **.NET** (`dotnet`): detected by a solution or project file. Bootstrap is `dotnet restore`, verify is `dotnet build --no-restore` (tests run only with the full-tests opt-in); the dev server is `dotnet run --project <target>`. A missing `dotnet` is a plain-language verification failure, not a crash.
- **Python** (`python`): detected by `pyproject.toml` or `requirements.txt`; the installer is picked by files only. `uv.lock` or a `[tool.uv]` table means `uv sync` (`--frozen` with a lock file); a `[tool.poetry]` table means `poetry install` with `POETRY_VIRTUALENVS_IN_PROJECT=true`; otherwise `python -m venv .venv` then the venv's Python with `-m pip install -r requirements.txt`.

Two rules for Python-style modules:

- **`.venv/python` convention.** Commands name the interpreter as `python`; `internal/platform` resolves it to `.venv\Scripts\python.exe` on Windows and `.venv/bin/python` elsewhere. The clean environment drops `VIRTUAL_ENV`, `CONDA_PREFIX` and `PYTHONPATH`.
- **`python-base` marker.** The command that creates the venv cannot use the venv's interpreter, which does not exist yet. A step whose argv starts with `python-base` runs the system Python (resolved by `platform.PythonBase`); use it only for creating the venv.

## Tests

- Detection: positive and negative cases with `testing/fstest.MapFS` (manifests
  and lock files only; fictional names such as `acme-shop`).
- One test per command list, including the "script missing" cases.
- A repo that matches two modules (see `toolchains/all_test.go`).
- A parser for a manifest format (like Maven's `ParsePOM`) gets: a successful
  parse, one test per rejection rule, and a fuzz target.
- Tests that run the real toolchain must skip when it is not installed.
