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
| Install (a proposal, never automatic) | `npm ci` / `pnpm install --frozen-lockfile` / `yarn install --frozen-lockfile` |
| Verify | `<pm> run typecheck`, `lint`, `test` (each only if the script exists); if none exists, `build` |
| Build | `<pm> run build` |
| Per-environment builds | `node.EnvBuilds(d)` returns `build:<env>` scripts as named steps |
| Dev server | `dev`, else `start` |

`builds.Step` has no environment field, so the Verify chain's `CI=true` is
exposed as `node.Env()` for the runner to apply on top of the clean environment.

**Artifacts.** `dist/**`, `build/**`, `out/**`, prefixed with `Detection.Dir`
when the manifest is in a sub folder (forward slashes, relative to the repo).

## Tests

- Detection: positive and negative cases with `testing/fstest.MapFS` (manifests
  and lock files only; fictional names such as `acme-shop`).
- One test per command list, including the "script missing" cases.
- A repo that matches two modules (see `toolchains/all_test.go`).
- A parser for a manifest format (like Maven's `ParsePOM`) gets: a successful
  parse, one test per rejection rule, and a fuzz target.
- Tests that run the real toolchain must skip when it is not installed.
