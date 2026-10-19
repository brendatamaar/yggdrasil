# Phase 2 — Workspace parity

## Functional core review (2026-10-17, baseline)

**Scope:** `--all` over `.functional-effect.json` core + shell: `internal/core`, `internal/db`, `cmd/ygg`, `internal/api`, `internal/platform`, `web/src/main.tsx`. 67 files, not counting generated `internal/db/gen`. No design documents: `docs/design/` doesn't exist yet. The review ran as three reviewers: core+db+cmd, api+web, and platform.

**Mechanical check** (`fe-check scan`):

```
Model Functional Effect: 4 core file(s) checked, no effects found.
```

The check only scans `internal/core/**`. Core today holds types and `Validate` only. No `step` function exists yet, so every lifecycle decision currently lives in the shell.

Labels: **fix now** means it blocks Phase 2 work that builds on this code, or it breaks purity or a safety rule. **fix when touched** means record it and fix it the next time the code changes.

### internal/core

| label | sev | file:line | problem | fix in model terms |
|---|---|---|---|---|
| fix now | Should | model.go:118-139 | `BuildStep` has optional `ExitCode`/`StartedAt`/`FinishedAt` (a sum type in disguise), and `Build.Status` can disagree with the step statuses. | In `core/builds`, use a sealed `StepState` (`Queued` / `Running{StartedAt, Attempt}` / `Finished{Outcome, ExitCode, …}`) and derive build status from the steps. Keep `core.Build` as the read model only. |
| fix now | Should | model.go:118-128, migration build_steps | Steps have no attempt or run ID, so a late exit from a cancelled or re-run step can't be told apart from the current one. | Carry `Attempt` on `Running`, the `StartStep` command and the `StepExited` input. `step` ignores an input with a stale attempt. Persist it in migration 00002. |
| fix now | Consider | model.go JSON tags, tygo.yaml:9 | Core is the tygo source, so core types are also the wire format, and sealed interfaces won't map through tygo. | Keep `core/builds` types free of tags. Put build DTOs in `internal/api` and convert at the boundary. |
| when touched | Should | model.go:104-116 | `Proposal.Kind` + `Payload json.RawMessage` is untyped, and which IDs are required depends on the kind without anything enforcing it. | Use a sealed `ProposalAction` and parse into it in `internal/proposals`. This becomes fix now if MR-via-push-options (GH-04) goes through proposals. |
| when touched | Should | model.go:71-91 | `Task.Validate` skips `ProjectID`, the `ParentTaskID` form, each `RepoID`, and the "ScopeRepo ⇒ exactly one repo" rule. | Add those checks, with one rejection test per rule. |
| when touched | Should | enums.go:43-49, 77-83; ids.go:19 | `WorktreeStatus`, `ChangeSetStatus` and `AuditID` have no `Validate`. | Add `Validate` and call it in the store. |
| when touched | Consider | model.go:64-67, 115 | `FinishedAt`/`SessionID`/`DecidedAt` are optional fields tied to status. | Let `step` set them from `Input.Now`. |
| when touched | Consider | model.go:146-150 | `AuditEntry.Actor`/`Subject` encode a sum type as strings. | Use a typed `Actor` with a parser, and a `Subject` built from typed IDs. |
| when touched | Consider | core_test.go:19-66 | Enum and `Task.Validate` tests cover only some rules. | Table tests: accept every constant, and one rejection test per rule. |

### internal/db

| label | sev | file:line | problem | fix in model terms |
|---|---|---|---|---|
| fix now | **Must** | db.go:124-127; store.go:867; internal/audit/sqlite.go:39 | State and its audit entry can't be committed in one transaction, because the audit writer uses its own write connection and `WithTx` forbids `Writer()` inside it. This breaks 5.11 step 3 and invariant 8. | Add a tx-scoped unit of work, `Store.Commit(ctx, func(*db.Tx) error)`, with typed writes and `AppendAudit`. Redaction becomes a pure helper. The audit ID and time come from the input. |
| fix now | **Must** | queries/builds.sql:11,21; tasks.sql:16; proposals.sql:15; change_sets.sql:11; worktrees.sql:12 | Every update is a blind `WHERE id = ?`, so a cancel racing a step finish can overwrite `cancelled` with `succeeded`. | Migration 00002 adds `version` (builds and build_steps first, then tasks and proposals): `UPDATE … WHERE id=? AND version=?`. When 0 rows change, return `ErrConflict`; the owner reloads and re-runs `step`. |
| fix now | Should | store.go:832-855 | `UpdateBuildStatus` decides `finished_at` from the status and reads the clock. | `core/builds.step` sets times from `Input.Now`, and the store persists the state it is given (`SaveBuild(tx, state, expectedVersion)`). |
| fix now | Should | (missing) | There is no outbox and no restart reconciliation, so after a crash, steps stay `running` forever. | Decide in the builds design doc: a `commands` outbox with deterministic IDs, or at minimum a startup `ProcessLost{BuildID, Attempt}` input. |
| fix now | Should | store.go:139-140, 204-205, 311-315 | UNIQUE and FK violations come back as raw driver errors, and `ErrSubTaskDepth` is detected by matching a string. WS-01 makes "already tracked" a normal outcome. | Map SQLite extended codes (2067, 787, 1811) to `ErrConflict`/`ErrNotFound`/`ErrSubTaskDepth`. The owning package turns them into domain outcomes. |
| fix now | Should | store.go:125-135, 189-199 | `CreateProject`/`CreateRepo` validate only the ID. | Add `core.Project.Validate`/`core.Repo.Validate`, and move path parsing (now in seed.go:22-39) into a WS-01 boundary parser shared by the API and seed. |
| when touched | Should | store.go:363-376, 687-700 | Task and proposal status updates decide timestamps and allow any transition (`executed → pending`). | `core/tasks` and `core/proposals` step functions own transitions; the store saves with a version check. |
| when touched | Consider | store.go:265-270, 527-530, 607-611, 718-721 | Row → core mapping casts strings without `Validate`. | Call `Validate` in each `xxxFrom`. |
| when touched | Consider | queries/tasks.sql:25 | `ListTaskRepos ORDER BY repo_id` loses the order of `RepoIDs`. | Add a `position` column, or document the order as a set. |
| when touched | Consider | store.go:35-42, 126-131 | The store fills in empty IDs and `time.Now()`. | The owner generates them and passes them as input fields; `Create*` rejects an empty ID. |
| when touched | Consider | store.go:244-271, 330-340 | N+1 queries in task lists. | Use a join or `IN` query. |

### cmd/ygg

| label | sev | file:line | problem | fix in model terms |
|---|---|---|---|---|
| when touched | Should | seed.go:53-70, 86-115 | Find-or-create is decided inline (check-then-act) and committed in four separate writes. | Use a pure `track(state, TrackRepo{…})` returning `ProjectCreated`/`RepoCreated`/`AlreadyTracked`/`NameTakenAtOtherPath`, committed through `Store.Commit` (WS-01). |
| when touched | Consider | seed.go:29-31, 107 | `LongPath` adds `\\?\` only for long paths, so the equality check depends on path length. | Normalise to one canonical form in the shared path parser. |

### internal/api

| label | sev | file:line | problem | fix in model terms |
|---|---|---|---|---|
| fix now | **Must** | tasks.go:185-199, 330-336, 340-347, 372-376 | State and audit entries are written separately (same root cause as the db finding). | Use `Store.Commit`, with audit emitted as `WriteAudit` commands committed alongside the state. |
| fix now | Should | hub.go:54-90; tasks.go:277 | `nextSeq` and `publish` take the lock separately, so concurrent publishes can arrive out of order, and `ws.ts` then reports a false gap and refetches. | Add `hub.publishNext(topic, data)` that assigns the seq and enqueues under one lock. |
| fix now | Should | hub.go:124-131; types.go:57-62, 77 | Topics are raw strings checked by a bool function, and Phase 2 adds repo, build and build-log topics. | Make `Topic` a closed type with `ParseTopic`/`String`, plus round-trip, rejects-each-rule and fuzz tests. |
| fix now | Should | errors.go:27-40 | `fail` matches db sentinels directly, so Phase 2 git, host and build errors would leak through or become 500s. | Define error classes (NotFound/Conflict/Invalid/Unavailable, each with a user message) that owning packages translate their errors into. |
| fix now | Should | server.go:28-72 | `Deps` takes a concrete `*db.Store`, and handlers make decisions themselves. New endpoints would copy this pattern. | Handler parses the request into a core input → calls a narrow api-side interface (`RepoService`, `BuildService`) implemented by the owning package. No `*db.Store` in new handlers. |
| when touched | **Must** | tasks.go:184-200, 326-347 | Tasks run in an in-memory goroutine with no startup reconciliation, so a crash leaves them `queued`/`running` forever. | Phase 3 `core/tasks` gets a `Recovered{Now}` input. `core/builds` should include the same input from the start. |
| when touched | Should | tasks.go:119-202 | Create rules (prompt, Ask-only SF-06, one repo, repo∈project) are decided inline in the handler. | Move them to `core/tasks.step(None, CreateTask{…})`. The Ask rule moves to `core/guard`. |
| when touched | Should | tasks.go:146-149, 204-205 | The model-name rule exists only in this handler. | Add a `core.ModelName` value type with `ParseModelName`. |
| when touched | Should | tasks.go:273-279; store.go:363-376 | `setStatus` writes any status with no from-state guard. | Status changes come only from `step`, saved with a version check. |
| when touched | Should | tasks.go:340-347 | `running` is published before the `task.start` audit entry. | Fixed by `Store.Commit`; until then, swap the order. |
| when touched | Should | tasks.go:378-389 | The `RunStatus → TaskStatus` mapping happens inline. | Make it a `RunFinished{Status, Now}` input. |
| when touched | Should | tasks.go:24-27 | Sub-agent caps (AG-22) are api constants. | Move them to `core/tasks` and carry them on the task. |
| when touched | Should | tasks.go:289-290 | `failTask` drops errors with `_ =`. | Log them, or turn it into a `RunAborted` input. |
| when touched | Consider | tasks.go:298-304 | DB and audit IO happen while holding `s.mu`. | Release the lock first. |
| when touched | Consider | tasks.go:36-61, 102 | N+1 queries, filtering in Go, and an unbounded events list. | Add an sqlc query with filters and a limit, and a `limit` parameter. |
| when touched | Consider | tasks.go:38 | The `project` query param isn't validated. | Parse it with `core.ProjectID.Validate`, and return 404 when unknown. |
| when touched | Consider | types.go:49-52; hub.go:35 | `MsgGap` is never sent, and the `tasks` seq resets on restart. | Send `MsgGap` on subscribe or drop, or remove it. |
| when touched | Consider | middleware.go:29 | Requests without an Origin header are allowed on state-changing methods. | Require `Origin` or `Sec-Fetch-Site: same-origin` on non-GET methods. |
| when touched | Consider | types.go:10-12 | The comment points to the wrong generated file path. | Fix the comment. |

### internal/platform

| label | sev | file:line | problem | fix in model terms |
|---|---|---|---|---|
| fix now | **Must** | pty_windows.go:101, 168 | `StartPTY` passes a resolved `.cmd`/`.bat` straight to `CreateProcess`. This skips `batchArgOK` (BatBadBut-style injection), and cmd.exe then searches cwd (the worktree) for argv[0]. Breaks invariant 5 and the promise in resolve.go:18. | If `isBatchFile(exe)`, build the line with `batchCommandLine` and run comspec, as `Start` does (start_windows.go:62-72). Or refuse batch files in `StartPTY` until Phase 5. Add a test with `a&b`. |
| fix now | Should | start_windows.go:22-27, 91, 158-167; pty_windows.go:72, 319-328; platform.go:54-56 | With `Spec.TaskID` set, cancelling any one process's ctx kills the whole task job, so cancelling a build would kill the agent and dev servers too. | ctx cancel kills only the process's own job. Task-wide kill becomes an explicit `KillTask(taskID)` emitted from a `CancelTask` command. Update tasks_windows_test.go:16. |
| fix now | Should | platform.go:62-70 | `Start` is a package function with no interface, fake or contract suite. | Add a consumer-side `StartFunc`, plus `platformtest` with a fake and a `ProcessContract(t, start)` run against both. Move the clauses from platform_windows_test.go:350-686 into it. |
| fix now | Should | batch_windows.go:37, 47-49, 70 | `errBatchArg` is unexported, and its message names only some of the refused characters. | Export `ErrBatchArg` and a pure `BatchArgsOK(argv)` that names the character, so builds can reject a preset when it is saved (BL-05, BL-15). |
| fix now | Should | paths.go:21-24 | `CheckPath` doesn't normalise `\\.\UNC\`, so `\\.\UNC\wsl$\…` passes PL-04. | Strip the `\\.\unc\` and `\\?\`/`\\.\` prefixes. Add a fuzz target ("accepted ⇒ no wsl host") and one test per rejection rule. |
| fix now | Should | platform_test.go:13-31 | No test proves a child sees only `Spec.Env`, and `StartPTY` builds its own env block (invariant 3, AGENTS.md 8). | Helper mode that prints its environment: set `GITHUB_TOKEN`, start through both `Start` and `StartPTY`, and assert the child env equals `Spec.Env`. |
| fix now | Consider | batch_windows.go:47 | BL-15 custom commands may need `&&` or pipes, which the batch path refuses. | Decide in the builds design doc: argv-only steps, or an explicit audited `Spec.Shell`. |
| when touched | Should | pty_windows.go:383-391 | When `Close` times out, it leaks the drain goroutine and two handles. | On timeout, still close `in`/`out` and wait briefly for `drained`. |
| when touched | Consider | shell_windows.go:144-153; open_windows.go:69-78 | The Explorer COM fallback ignores ctx, so it can hang the WS-08 handler. | `select` on `ctx.Done()` vs `errc`. |
| when touched | Consider | paths.go:41-58 | `ShortDirName` maps invalid UTF-8 to U+FFFD without adding the hash, so two names can collide. | Set `changed`, and add a property/fuzz test. |
| when touched | Consider | ports_windows.go:55-86 | Stale parent pids can attribute an unrelated process's ports. | Extract a pure `descendants(edges, root)` that checks creation times. |
| when touched | Consider | ports_windows.go:110-126 | `tablePorts` parses kernel bytes with no fuzz test. | Add a fuzz target. |
| when touched | Consider | pty_windows.go:264-282 | `envBlock` doesn't de-duplicate keys or sort by key. | Make it a pure function: case-insensitive dedup (last wins), sorted by key, with unit tests. |
| when touched | Consider | pty.go:16 | `Size.validate` is unexported. | Export `NewSize(cols, rows) (Size, error)`. |
| when touched | Consider | start_windows.go:133; start_unix.go:73 | `ErrWaitDelay` is swallowed, so a truncated log looks complete. | Return an `OutputTruncated` flag or sentinel. |
| when touched | Consider | paths.go:17-31 | A drive letter mapped to `\\wsl$` passes `CheckPath`. | Resolve mapped drives before the check. |
| when touched | Consider | open_unix.go:66 | `go cmd.Wait()` has no owner. | Document it, or double-fork. |

### Fine

- **internal/core:** types, `Validate` and `Terminal` only. No effects, clock reads or IDs. Plain statuses are validated string enums. Purity is enforced by archtest `corePure` and depguard/forbidigo.
- **internal/db:** pool split, pragmas, `BEGIN IMMEDIATE`, rollback on error, embedded migrations. STRICT tables with CHECK enums, plus triggers for one-level sub-tasks and an append-only audit log. Tests use real SQLite under `t.TempDir()` with a path containing a space.
- **cmd/ygg:** `config.go` parses env at the boundary, with one test per rule. There is a single entry point. `doctor` runs only through `platform.Start` with the clean env and a timeout, and makes no retries.
- **internal/api:** strict Host/Origin guard, loopback-only listener, static allowlist with traversal and device-name rejection. Every goroutine has an owner. Publishing never blocks. The sink redacts before it stores and publishes. No internal text appears in error bodies.
- **web/src/main.tsx:** no findings.
- **internal/platform:** job-object nesting with suspended start, breakaway for editors, `ErrNoEnv` on every entry point, cwd never searched, `runtime.GOOS` kept inside the package. Pure helpers are already split out (`wrapperFile`, `candidates`, `batchCommandLine`, `checkPTYSpec`, `tablePorts`). The only retry is a read-only one.

### Gaps that matter for Phase 2

- **Boundary parsers without round-trip / rejects-each-rule / fuzz tests:**
  - api: CreateTask (no accepted ⇒ valid, no fuzz), `validTopic`, `cleanAssetPath`, `splitHost`/`originAllowed` (a security boundary, with no fuzz), `after`.
  - db: `fmtTime`/`parseTime`, row → core mappers.
  - cmd: the seed path parser, `listenAddr`.
  - platform: `CheckPath`, `tablePorts`, `ShortDirName`, `batchCommandLine`, `LongPath` add/strip, `Size.validate`.
  - core: `Task.Validate` (incomplete).
- **Fakes without a contract suite:**
  - `agents/mock.Adapter` plus five ad-hoc adapters in api_test.go:562-823, with no `agents.Adapter` contract suite.
  - No `Start`/`Process` fake yet; builds need one.
  - A future `BuildStore` fake needs a contract suite against SQLite, covering conflict, not-found and duplicate-ID.
- **Decisions made inline in effectful code:**
  - store lifecycle timestamps and transitions (store.go:363, 687, 832).
  - seed find-or-create.
  - error classification by string match (store.go:312).
  - api create rules, run outcome mapping, `failTask`/`setStatus`, sub-agent caps.
  - platform: LongPath prefix logic, process-tree walk, COMSPEC fallback.
- **Property tests:** none yet, because there are no `step` functions. `core/builds` needs them from day one:
  - steps run in order;
  - a finished build has no running step;
  - a stale-attempt exit changes nothing;
  - cancel is terminal;
  - Maven ordering is a topological order, and a cycle is rejected.
- **Retries on non-idempotent operations:** none found.

### Fix now, in order

1. **platform:** route batch files in `StartPTY` through `batchCommandLine`, or refuse them (safety, invariant 5).
2. **platform:** add a child-env test for `Start` and `StartPTY` (safety, invariant 3).
3. **platform:** normalise `\\.\UNC\` in `CheckPath` and add a fuzz test (PL-04; WS-01 feeds user paths into it).
4. **db + api:** add the `Store.Commit` unit of work so state and audit commit in one transaction (5.11 step 3, invariant 8).
5. **db + core:** migration 00002 with a `version` column and step `Attempt`; `ErrConflict` on a version mismatch.
6. **db + api:** map SQLite errors to `ErrConflict`/`ErrNotFound`/`ErrSubTaskDepth`, and add api error classes.
7. **platform:** separate ctx cancel from task-job kill, and add an explicit `KillTask`.
8. **platform:** injectable `StartFunc` + `platformtest` fake + `ProcessContract` suite.
9. **platform:** export `ErrBatchArg`/`BatchArgsOK`.
10. **api:** atomic `hub.publishNext`, and a closed `Topic` type with parser tests.
11. **api:** set the handler → service-interface pattern for new endpoints (no `*db.Store`).
12. **core + db:** `Project.Validate`/`Repo.Validate` and a shared WS-01 path parser.
13. **Builds design doc** (`/functional-effect:design builds`): decide these there, before any code:
    - sealed `StepState`;
    - the store persists times it is given and never decides them;
    - outbox vs `ProcessLost` restart reconciliation;
    - argv-only steps vs `Spec.Shell` for BL-15;
    - tag-free `core/builds` types with DTOs in api.

## Pre-phase fixes (review items 1–13)

The human approved the recommendations above (2026-10-17). These fixes are Phase 2 foundations: they land on `main` before any Phase 2 feature work.

### Decisions

- **Step state:** a sealed `StepState` in `internal/core/builds`; build status is derived from the steps.
- **Timestamps:** `step` sets them from `Input.Now`. The store persists what it is given and never reads the clock for lifecycle fields.
- **Restart recovery:** a `ProcessLost{BuildID, Attempt, Now}` input at startup, not an outbox. The job object already kills every build process when ygg dies, so there is nothing to resume; a Maven or npm step can't continue halfway. The user re-runs the build. Revisit the outbox if a later feature needs to resume commands (e.g. proposal execution).
- **Custom commands (BL-15):** argv-only. Each command is one `StartStep`, with no shell entry point. `&&` and pipes are rejected when the preset is saved, with a message to split the command into steps.
- **Wire types:** `internal/core/builds` types carry no JSON tags. Build DTOs live in `internal/api` and are converted at the boundary.
- **Item 12, split:** `core.Project.Validate`/`core.Repo.Validate` land now. The shared path parser moves into the WS-01 owning package when that task is planned, because the package doesn't exist yet.
- **Item 11:** a convention, not code. The lead documents it in `internal/api/doc.go`, and the first Phase 2 endpoint follows it.

### Tasks

| # | Items | Owner | Owns | Must not touch | Depends on |
|---|---|---|---|---|---|
| F0 | 13 | lead | `docs/design/builds.md` (via `/functional-effect:design builds`) | code | — |
| F1 | 11 | lead | `internal/api/doc.go` | anything else | — |
| A | 1, 2, 3, 9, then 7, 8 | `platform-windows` | `internal/platform/**`, new `internal/platform/platformtest/` | `internal/api`, `internal/agents`, `cmd` | — |
| B | 4, 5, 6, 12 (Validate) | `go-backend` | `internal/db/**` (new migration `00002`, queries, gen), `internal/audit/**`, `internal/core/{model,enums,ids,core_test}.go`, `internal/api/{tasks,errors,server}.go`, `cmd/ygg/{app,seed}.go` and their tests | `internal/platform`, `internal/api/{hub,types}.go`, `internal/core/builds` | F0 (the attempt column) |
| C | 10 | `go-backend` | `internal/api/hub.go`, `internal/api/types.go` (topic part), new `internal/api/hub_test.go` | `internal/api/tasks.go`, `api_test.go`, other packages | — |
| I | — | lead | integration: wire `platform.KillTask` into task cancel, switch `tasks.go` to `hub.publishNext`/`Topic`, run all `task` checks | — | A, B, C |
| R | — | lead | `safety-reviewer` + `fe-reviewer` on `git diff <batch-start>..main`, then fix every Blocking/Must-fix finding | — | I |

**Parallel:** A, B and C run at once, with disjoint file ownership. B waits only for F0's migration decision (attempt on `build_steps`). C keeps the existing `TopicTasks`/`TopicTaskPrefix` string constants, so `tasks.go` still compiles until integration.

**Migration 00002 (B):** `version INTEGER NOT NULL DEFAULT 0` on `builds`, `build_steps`, `tasks` and `proposals`, plus `attempt INTEGER NOT NULL DEFAULT 0` on `build_steps`. Every status update becomes `… WHERE id = ? AND version = ?`, and 0 rows on an existing row returns `db.ErrConflict`.

**Store.Commit (B):** `Store.Commit(ctx, func(*db.Tx) error)` runs in one `BEGIN IMMEDIATE`. `Tx` exposes typed writes plus `AppendAudit(core.AuditEntry)`. `audit` becomes a pure `Prepare(entry, redactor, id, now) (core.AuditEntry, error)` that validates and redacts. Every existing state write plus audit pair in `api/tasks.go` and `cmd/ygg/seed.go` moves into a single `Commit`.

**Definition of done (each branch):** `task generate`, `task lint`, `task typecheck`, `task test` and `task test:web` all pass. Each branch ends with the report format from its agent file.

### Review gate 1 (2026-10-18)

`safety-reviewer` and `fe-reviewer` reviewed `1bd49f1..main`. There were no Blocking findings.

**Must-fix findings:**
- cmd.exe searches the worktree for commands run inside a batch file;
- the `Store` still exports write methods that skip the audit entry;
- migration 00002 can't store the `Cancelling` state.

**Decisions:**
- **Migration 00002 is amended in place, not followed by a 00003.** It has never been applied outside test databases (the local data dir is at version 1), and a 00003 would rebuild the same tables again. This is a one-time exception to "never edit an applied migration"; from the next tag on, 00002 is frozen.
- **Build steps carry no `TaskID`.** A build is stopped only by its own `Cancel`, and ending an agent run (`KillTask`) never kills a build. The design doc is updated to match.
- **`builds.worktree_id` is `ON DELETE RESTRICT` with an explicit `target` column.** A queued worktree build can never silently fall back to the main checkout.
- **`AppendAudit` redacts again as a backstop** (`NewStore` takes the redactor's function). It does this on top of `audit.Prepare` and the archtest rule.

**Fix batch R2:**

| Task | Owner | Owns |
|---|---|---|
| P2 | `platform-windows` | `internal/platform/**` |
| D2 | `go-backend` | `internal/db/**` (amend 00002), `internal/audit/**`, `internal/core/{model,enums,core_test}.go`, `internal/api/{tasks,errors,server}.go` and their tests, `internal/archtest/**`, `cmd/ygg/**` |

### Review gate 2 (2026-10-18) — passed

Both reviewers re-checked `4cb3b09..main`, and both passed with no Blocking or Must-fix findings. Every gate 1 finding is fixed.

The lead also fixed these Should-fix findings from gate 2:
- `Finished.CancelledAt` added to the design, with invariant I11.
- `build_steps.repo_id` and `build_artifacts.repo_id` are `ON DELETE RESTRICT`, with a new `db.ErrInUse`. Delete rules are in the design under "Persistence and delivery" → Deletes.
- The raw `DB` pools are unexported, and `Writer`/`Reader` are no longer exported.
- New archtest rules: `db/gen` may be imported only from `internal/db`; `dbtest` and `platformtest` only from tests.

**Pre-phase fixes: done.** Items 1–13 of the baseline review are on `main`.

### Open follow-ups (not blocking; fix when touched)

| From | Item | Where |
|---|---|---|
| gate 2 safety | Test that an output writer is never written after `Wait`: a grandchild writes again after about 3 s | `internal/platform/outputs.go` |
| gate 2 safety/fe | On Unix, `Kill` can't reach a grandchild left behind after the leader is reaped; send `kill(-pgid)` right after `Wait` | `start_unix.go`, `pty_unix.go` |
| gate 2 fe | A `Validate` failure after the request parser passed is a defect (500), not a 400 | `internal/api/tasks.go` |
| gate 2 fe | The store still derives `finished_at`/`decided_at` from the status (with the caller's time); move this into `step` when `core/tasks`/`core/proposals` exist | `internal/db/tx.go` |
| gate 2 fe | `Commit` doesn't force an `AppendAudit`; consider a required audit parameter or an archtest rule | `internal/db/tx.go`, archtest |
| gate 2 fe | `Clock`/`IDs` capabilities in `api.Deps` when builds land; `Deps.KillTask` typed as `platform.KillTaskFunc` | `internal/api/server.go` |
| gate 2 safety | Redact JSON string values after decoding, not the raw text (`\uXXXX` escapes) | `internal/db/tx.go`, `internal/audit/prepare.go` |
| gate 2 safety | `startBreakaway` doesn't use `withNoCurDir` (not exploitable; for consistency) | `internal/platform/open_windows.go` |
| gate 2 safety | The archtest read-method allowlist matches any `Get*`/`List*` name; use an explicit list | `internal/archtest/rules.go` |
| gate 2 fe | `Fake.tasks` never drops finished processes | `platformtest/fake.go` |
| baseline | Every remaining "fix when touched" row in the tables above | — |
| WS-01 | Untracking a repo: keep build history (a status) or delete its builds in one transaction. Plus the shared path parser, and the seed find-or-create moving into a pure `track` step | WS-01 owning package |

---

## Phase 2 work breakdown (approved 2026-10-18)

Scope: docs/PLAN.md Phase 2. Already on `main`: `internal/git` (status, branches, log, show, diff, fetch, ff-only pull, checkout, worktrees), `platform.OpenEditor`/`OpenFolder`, the widget interface and registry, the builds design and its foundations.

### Decisions (approved by the human 2026-10-18: every recommendation)

1. **User-initiated git actions on a main checkout** (ff-only pull, checkout, cross-repo branch switch; WS-06, WS-07). *Recommend:* they run directly after the user's click in the UI, with an audit entry. They are not proposals. Proposals (RV-02) cover commit, push, MR, install and publish. Agents only get fetch/pull through MCP (Phase 3). Extra guard: a pull or checkout is refused while a build runs in that repo's main checkout.
2. **GH-03, MR creation via push options:** this is a push, so it is a proposal (RV-02), and proposals arrive in Phase 3. *Recommend:* move GH-03 to Phase 3. Phase 2 delivers GH-01, GH-02 and GH-04 (read-only host data).
3. **Untracking a repo (WS-01):** *Recommend* a soft untrack (`repos.untracked_at`). The repo leaves every list and agents can't see it (SF-02), but its build history stays. Tracking the same path again restores it. Nothing on disk is touched.
4. **Package names:**
   - `internal/repos` (WS-01 to WS-08 shell), with core in `internal/core/repos`;
   - `internal/search` (XR-01);
   - `internal/hosts/{gitlab,github,gitea}`, with core in `internal/core/hosts`.

### Designs (`/functional-effect:design`, one per stateful feature; written before the batch that needs them, each approved by the human)

| Design | Covers | Why it needs one |
|---|---|---|
| `docs/design/builds.md` ✅ | BL-03, BL-04, BL-05–07, BL-09 | queue, step machine, processes |
| `docs/design/repo-tracking.md` | WS-01 (+ seed) | find-or-create decision, untrack/restore, path boundary |
| `docs/design/repo-status.md` | WS-02, WS-03, WS-04, the perf target | refresh scheduler, bounded concurrency, timeouts, cache, live counts |
| `docs/design/git-actions.md` | WS-06, WS-07 | per-repo exclusivity, bulk with partial results, skip-dirty, build guard |
| `docs/design/git-hosts.md` | GH-01, GH-02, GH-04 | external HTTP, optional tokens, polling with TTL, rate limits and backoff |
| `docs/design/code-search.md` | XR-01 | `git grep` fan-out, limits, cancellation, streaming |

No design needed:
- WS-05 repo page: read-only calls into `internal/git`, whose parsers are already tested.
- WS-08 open in editor: one platform call plus an editor setting.
- Toolchain detection (BL-10): reads files only. Its manifest parsers still get the boundary test set: round-trip, accepted ⇒ valid, one test per rejection rule, and fuzz.

### Shared contracts: batch F (committed to `main` before any fan-out)

Owned by one `go-backend` agent, after the designs it needs are approved:
- `internal/core/builds`, `internal/core/repos` and `internal/core/hosts`: the state, input and command types and the `Step` signatures from the designs. No logic yet beyond `Validate`.
- `internal/builds/toolchain.go`: the `Toolchain` interface (`Detect`, `Commands`, `Artifacts`, optional `Order`).
- Migration `00003`:
  - `repos.untracked_at`;
  - host instances and the repo → host mapping;
  - an editor setting;
  - whatever the repo-status and git-hosts designs need persisted.
- `internal/api`:
  - one file per area (`repos.go`, `builds.go`, `hosts.go`, `search.go`), each with stub handlers and its own `routesX(mux)`, called once from `server.go`;
  - one DTO file per area (`types_<area>.go`);
  - every new topic variant in `topic.go` (`repos`, `repo:<id>`, `builds`, `build:<id>`, `hosts`);
  - the service interfaces plus `Clock`/`IDs` in `Deps`.
- `task generate`, so the frontend gets every DTO type at once.

After batch F, no two parallel tasks touch the same file.

### Batches (at most 3 implementation agents at a time; review gate after each)

| Batch | Task | Owner | Owns | PRD |
|---|---|---|---|---|
| B1 | builds core | `go-backend` | `internal/core/builds` (`Step`, `Run`, `Next`, `OrderModules`) + property tests I1–I11, Q1, O1 | BL-03, BL-04 |
| B1 | repos: tracking + status | `go-backend` | `internal/core/repos`, `internal/repos` (tracking, status scheduler), `internal/api/repos.go` (tracking/status part), `cmd/ygg/seed.go` | WS-01–04 |
| B1 | home + themes | `frontend` | `web/src/routes/index.tsx` hero/composer/chips, theme store and CSS | UI-01, UI-05 |
| B2 | builds shell | `go-backend` | `internal/builds` (service, interpreter, queue worker, logs, artifacts, presets, `buildstest`), `internal/db` builds queries/`LoadBuild`/`SaveBuild`, `internal/api/builds.go`, `cmd/ygg` startup reconciliation | BL-03, BL-05–07, BL-09 |
| B2 | toolchains | `go-backend` | `internal/builds/toolchains/{node,maven,gradle,custom}` + detection fixtures, `docs/writing-a-toolchain.md` | BL-01, BL-02, BL-10, BL-11, BL-15 |
| B2 | projects + repo pages | `frontend` | `web/src/routes/projects*`, `web/src/routes/repos/*` | WS-02–05, WS-08 UI |
| B3 | git actions + open | `go-backend` | `internal/repos` actions part (`actions.go`), `internal/api/repos_actions.go` | WS-06–08 |
| B3 | git hosts | `go-backend` | `internal/core/hosts`, `internal/hosts/**`, `internal/api/hosts.go`, `.env.example` | GH-01, GH-02, GH-04 |
| B3 | builds UI + MR inbox | `frontend` | `web/src/routes/builds*`, `web/src/routes/mrs*` | BL-05–07 UI, GH-02 UI |
| B4 | code search | `go-backend` | `internal/search`, `internal/api/search.go` | XR-01 |
| B4 | widgets | `frontend` | `web/src/widgets/{git-activity,merge-requests,needs-attention,ci-failures,builds,recent-areas}` | UI-03 |
| B4 | tests + perf | `test-engineer` | test maps from each design, demo-project fixtures (multi-repo, Maven/Node/Gradle manifests, recorded host HTTP), the 50-repo perf check | all |

`test-engineer` also gets each designed feature's test map right after that feature merges, in the same batch slot as the feature or the next one.

Git actions (B3) land in their own files, `internal/repos/actions.go` and `internal/api/repos_actions.go`, so they never edit what the B1 repos task wrote.

### Exit criteria (from docs/PLAN.md)

- The demo project shows correct status, MRs, pipelines and builds.
- Daily workspace use is possible.
- Status refresh for 50 repos finishes in under 10 seconds (perf test).
- Review gates pass after every batch.

### Progress

- **Batch F** merged (`a9ceb56`). Deviations from the designs, accepted:
  - `Actor` is a local type in `core/builds` and `core/repos`;
  - the tracking outcome `Untracked` is renamed `UntrackDone`, plus a new `Renamed` outcome;
  - the counts function is named `CountEntries`;
  - `BuildPreview(path, scan, ws)` takes the path first;
  - tracking commands carry `ExpectVersion`;
  - the builds `Spec` has `PresetID`, and `StartStep` has `RepoID`;
  - refresher types are named `RefreshInput` and `RefreshCommand`.

  Migration 00003 rebuilds `projects` with `PRAGMA foreign_keys=OFF` outside goose's transaction (NO TRANSACTION + explicit BEGIN/COMMIT). This is to be checked at the next review gate.
- The status endpoints moved to `internal/api/repos_status.go` (`35945cd`), so tracking and status run in parallel.
- **B1 running:** builds core; repo tracking; repo status. The batch F review gate is folded into the B1 gate.
- **Schedule change:** home + themes (frontend) starts when the builds core task finishes, which keeps at most 3 agents running.

### Review gate B1 (2026-10-19): failed, fixes in progress

The gate covered batch F and the B1 backend (`79a8f67..main`).

**Safety reviewer:**
- Blocking:
  - `ygg.exe` committed with real machine paths;
  - a repo can be untracked while an agent task runs in it.
- Must-fix:
  - migration 00003's failure paths;
  - the derived `rel_path` can be a value the parser rejects;
  - no API tests for the untracked rules.

**fe-reviewer:**
- Must-fix: the status attempt guard (a late result from a removed and re-added repo is accepted), and R4/R5 tests that can't catch that.

**Decisions:**
- **`ygg.exe` removed from local history** (the human chose this). `git filter-branch` on the 86 unpushed commits (`origin/main..main`); `/ygg.exe`, `/ygg` and `*.exe` added to `.gitignore`. All commit hashes after `eb6feb8` changed. The old hashes quoted earlier in this file refer to the pre-rewrite history.
- **Migration 00003 is amended in place** (same one-time exception as 00002: it never ran outside test databases; the local data dir is at version 1). It becomes a Go migration that pins one connection and runs `foreign_key_check`.
- **`BusyTaskRunning` is brought forward from Phase 3.** Agents already run in Phase 1/2, so untracking a repo with a queued or running task is refused.

**Fix batch:**

| Task | Owner | Owns |
|---|---|---|
| R-status | `go-backend` | `internal/core/repos/refresher*.go`, `status*.go`; `internal/repos/status_*.go` + `repostest/status_contract.go`; `internal/api/repos_status.go` + its tests; `internal/core/builds` (small fixes) |
| R-tracking | `go-backend` | the remaining `internal/core/repos` files; tracking files in `internal/repos`; `internal/db/**`; `internal/platform` (path canonical form); `internal/api/{repos,tasks}.go` + their tests; `cmd/ygg/seed.go` |

### Review gate B1, round 2 (2026-10-19)

Both reviewers re-checked `a243708..main`. Every round-1 finding is fixed; fe-reviewer mutation-tested the fixes.

Remaining Must-fix:
- safety: a race in task creation (the tracked check runs outside the write transaction);
- fe: paths stored by v0.2.0 aren't canonical, so the case-sensitive core could track one folder twice.

Both go to a round-3 fix agent, with migration 00003 made safe to rerun, `CheckPath` run again on the canonical path, an R1 bound measured from the test model, `Restarted()` coverage, and design doc updates.

**Decisions:**
- **Tasks in `review` don't block untracking** (no agent runs then). The proposal executor (Phase 3) must refuse untracked repos. Project-scope tasks (Phase 3) must write one `task_repos` row per repo, or the busy check misses them.
- **Old paths are canonicalised when the workspace loads,** in the tracking shell, with no write. `db` can't import `platform`.

**Backlog (not blocking):**
- `Tx.SetSetting` must be audited by its first caller (WS-08 settings service).
- OneDrive Files-On-Demand folders are reparse points; refuse only symlink and mount-point tags (`internal/platform`).
- On Unix, `CanonicalPath` doesn't resolve symlinked ancestors.
- Status restart with a failing `loadActive` stays empty until the next reload; retry on Tick.
- `PublishRepoCounts` runs 1 + N store queries per publish; batch them.
- `Active{Since}` isn't updated on restore; there's no DirLister fake/contract; there's no `RelPath` on `core.Repo`.

**Local refs that still contain `ygg.exe`:** `refs/original/refs/heads/main` (the filter-branch backup) and the branches `worktree-agent-afc97b508ce9b4e75` and `worktree-agent-a3f343cfca2a5e30d`. None are pushed. Never `git push --all` before they are deleted; deleting them needs the human's approval.
