# Dev servers — Model Functional Effect design
Profile: service

Covers: DV-01 (dev servers per repo or worktree), DV-02 (port detection), DV-03 (live logs, cleanup, orphans), DV-05 (tracked folders only, clean env, killed with Yggdrasil), and the use of WE-02 port blocks (decision 2 in `docs/phases/phase-5.md`).

Core: `internal/core/devservers`. Shell: `internal/devservers` (service, interpreter, log ring, store adapter). Port blocks come from `docs/design/worktree-env.md`; `core.PortBlock{Base, Size}` lives in `internal/core` (core domains may not import each other).

One pure piece: `Step(Server, Input) (Server, []Command)`, one `Server` per key `(Target, CommandKey)`. `Target` is a repo (main checkout) or a worktree; `CommandKey` is the toolchain command name (`dev`, or a custom label). There is no cross-server scheduler: servers do not compete for a slot.

## State map

`Server` is a sealed interface; `Status()` gives a plain string for the API.

| State | Data | Meaning |
|---|---|---|
| `Stopped` | `Last` (Never / Exited{Code} / UserStopped / Interrupted), `At` | at rest; may start |
| `Starting` | `Attempt`, `Since`, `Block` (may be empty for a main checkout without a block), `Expanded` (argv + env as data) | process requested; not yet seen listening |
| `Running` | `Attempt`, `Since`, `Pid`, `Ports` (sorted, unique, observed from the OS), `Announced` (ports the output printed), `Warnings` | alive |
| `Stopping` | `Attempt`, `Cause` (UserStop / Restart / AppShutdown), `KillAttempt` (1 or 2), `Since` | kill requested |
| `Failed` | `Reason` (StartFailed / ExitedEarly{Code} / Crashed{Code} / StopFailed), `At` | at rest; may start again |
| `Orphaned` | `Pid`, `SeenAt` | non-Windows only: a process from a crashed run is still alive |

`Restart` is not a state: it is `Stopping{Cause: Restart}` followed by a `StartRequested` that the shell holds until `Stopped` (the shell feeds the second input; the core never queues it).

`Warnings` is derived by the pure `OutsideBlock(Ports, Block)`, not stored separately in the database.

## Inputs

| Input | Fields |
|---|---|
| `StartRequested` | `Attempt` (shell-generated, strictly increasing per server), `Now`, `Template` (parsed argv + env from the toolchain or the repo override), `Block`, `Shutdown` is separate |
| `Started` | `Attempt`, `Pid`, `Now` |
| `StartFailed` | `Attempt`, `Reason` (plain), `Now` |
| `PortsObserved` | `Attempt`, `Ports` (from `platform.ListeningPorts`), `Now` |
| `OutputAnnounced` | `Attempt`, `Ports` (parsed from output lines) |
| `Wake` | `Attempt`, `Kind` (Settle / KillGrace / Poll), `Now` |
| `Exited` | `Attempt`, `Code`, `Now` |
| `StopRequested` | `Cause`, `Now` |
| `ShutdownRequested` | `Now` |
| `Recovered` | `Liveness` (Dead / Alive{Pid}), `Now` (once per stored row at startup) |
| `OrphanStopped` | `Now` |

Clock and attempt numbers arrive as input fields.

## Transition table

| From | Input | Guard | To | Commands |
|---|---|---|---|---|
| `Stopped`/`Failed` | `StartRequested` | template expands within the block | `Starting{a}` | `StartProcess{a, argv, env}`, `ScheduleWake{Settle, +10 s}`, `Audit{devserver.start}`, `Publish` |
| `Stopped`/`Failed` | `StartRequested` | a placeholder is outside the block | unchanged | `Reject{BadTemplate}` |
| `Starting`/`Running`/`Stopping`/`Orphaned` | `StartRequested` | | unchanged | `Reject{AlreadyRunning}` |
| `Starting{a}` | `Started{a, pid}` | | `Starting` (pid kept) | `ScheduleWake{Poll, +1 s}` |
| `Starting{a}` | `StartFailed{a}` | | `Failed{StartFailed}` | `Audit`, `Publish` |
| `Starting{a}` | `PortsObserved{a, ports≠∅}` | | `Running{a, ports}` | `ScheduleWake{Poll, +2 s}`, `Audit{devserver.running}`, `Publish` |
| `Starting{a}` | `Wake{a, Settle}` | pid known | `Running{a, ports: ∅}` | `ScheduleWake{Poll, +2 s}`, `Publish` |
| `Starting{a}` | `Wake{a, Poll}` | | `Starting` | `PollPorts{a}`, `ScheduleWake{Poll, +1 s}` |
| `Starting{a}` | `Exited{a, code}` | | `Failed{ExitedEarly{code}}` | `Audit`, `Publish` |
| `Running{a}` | `Wake{a, Poll}` | | `Running` | `PollPorts{a}`, `ScheduleWake{Poll, +2 s}` |
| `Running{a}` | `PortsObserved{a}` | ports changed | `Running` with new ports | `Publish` |
| `Running{a}` | `OutputAnnounced{a}` | | `Running` with `Announced` set | `Publish` |
| `Running{a}` | `Exited{a, 0}` | | `Stopped{Exited{0}}` | `Audit`, `Publish` |
| `Running{a}` | `Exited{a, code≠0}` | | `Failed{Crashed{code}}` | `Audit`, `Publish` |
| `Starting`/`Running` | `StopRequested{c}` | | `Stopping{a, c, 1}` | `KillProcess{a}`, `ScheduleWake{KillGrace, +10 s}`, `Audit{devserver.stop}`, `Publish` |
| `Starting`/`Running` | `ShutdownRequested` | | `Stopping{a, AppShutdown, 1}` | `KillProcess{a}`, `ScheduleWake{KillGrace, +5 s}`, `Audit` |
| `Stopping{a}` | `Exited{a}` | | `Stopped{UserStopped}` (Cause AppShutdown: `Stopped{Interrupted}`) | `Audit`, `Publish` |
| `Stopping{a, c, 1}` | `Wake{a, KillGrace}` | | `Stopping{a, c, 2}` | `KillProcess{a}`, `ScheduleWake{KillGrace, +10 s}` |
| `Stopping{a, c, 2}` | `Wake{a, KillGrace}` | | `Failed{StopFailed}` | `Audit`, `Publish` (the job object still ends the tree when `ygg` ends) |
| `Stopped`/`Failed`/`Orphaned` | `ShutdownRequested` | | unchanged | none |
| `Starting`/`Running`/`Stopping` | `Recovered{Dead}` | | `Stopped{Interrupted}` | `Audit`, `Publish` |
| `Starting`/`Running`/`Stopping` | `Recovered{Alive{pid}}` | non-Windows (the shell never sends `Alive` on Windows) | `Orphaned{pid}` | `Publish` |
| `Orphaned` | `StopRequested` | | `Orphaned` | `StopOrphan{pid}`, `Audit` |
| `Orphaned` | `OrphanStopped` | | `Stopped{Interrupted}` | `Audit`, `Publish` |
| any | input with a stale `Attempt` | | unchanged | none |
| any other (state, input) pair | | | unchanged | none |

After `ShutdownRequested` the shell stops sending `StartRequested`; the core also refuses it through a `Closed` marker on the service (not on the server): the service checks before calling `Step`. Servers already in `Stopped`/`Failed` need nothing.

## Invariants

| # | Invariant | Property test |
|---|---|---|
| D1 | At most one live attempt per server: `Starting/Running/Stopping` carry exactly one `Attempt`; a second `StartRequested` in a live state changes nothing | `TestPropOneLiveAttempt` |
| D2 | `StartProcess` is emitted only on `Stopped/Failed → Starting`, once per `Attempt` | `TestPropStartOnce` |
| D3 | A stale `Attempt` changes nothing; `Attempt` never decreases | `TestPropStaleIgnored` |
| D4 | `Stopping` is entered together with `KillProcess` and leaves only to `Stopped` or `Failed` | `TestPropStoppingEnds` |
| D5 | No automatic restart: `StartProcess` appears only in the step of a `StartRequested` | `TestPropNoAutoRestart` |
| D6 | After `ShutdownRequested`, no server is in `Starting` or `Running`, and no step emits `StartProcess` | `TestPropShutdownDrains` |
| D7 | `Running.Ports` is sorted, unique, within 1..65535; `Warnings` is non-empty iff a port lies outside a non-empty `Block` | `TestPropPortsWellFormed`, `TestOutsideBlockTable` |
| D8 | Every `{port}` / `{port:n}` expansion lies inside the block; a template that cannot expand is rejected, never partly started | `TestPropExpansionInsideBlock` |
| D9 | `Recovered` leaves no server in `Starting/Running/Stopping`; `Orphaned` only comes from `Alive`; `Orphaned` never starts | `TestPropRecoveredNothingLive`, `TestPropOrphanNeverStarts` |
| D10 | Every change of status has an `Audit` (start, running, stop, exit, failure) and a `Publish` | `TestPropStatusChangeAudited` |
| D11 | `Running` implies a known `Pid`; the environment in `StartProcess` contains no key from a deny list (token-like names) because the core only builds `PORT`, `YGG_PORT_BASE`, `YGG_PORT_0..n` | `TestPropEnvOnlyPorts` |
| D12 | The core never produces a command that touches a path: `StartProcess` carries argv and env, the shell supplies the tracked directory | `TestCommandsCarryNoPaths` |

## Commands

| Command | Cardinality | Idempotent | Produces |
|---|---|---|---|
| `StartProcess{Attempt, Argv, Env}` | one | **no**, protected by D2 and `Attempt` | `Started` / `StartFailed`, then `Exited` |
| `KillProcess{Attempt}` | one | yes (`platform.KillTask`-style kill of the server's job) | `Exited` |
| `PollPorts{Attempt}` | one | yes | `PortsObserved` |
| `ScheduleWake{Attempt, Kind, At}` | one | yes with key `(Attempt, Kind)` | `Wake` |
| `StopOrphan{Pid}` | one | yes | `OrphanStopped` |
| `Publish{status}` | one | yes | none |
| `Audit{Entry}` | one | yes with key (subject, action, at) | none; committed with the state |
| `Reject{Reason}` | one | yes | HTTP response only |

## Call graph

```go
// Production: POST /api/devservers/{target}/{command}/start
api handler
  → devservers.ParseStart(body)                       // boundary: target is an ID, never a path
  → Service.Start(ctx, target, command)
      → Resolve target to a tracked repo/worktree dir (invariant 6, DV-05); trust gate as for builds
      → toolchain.Commands.DevServer (or repo override) → ParseTemplate
      → worktreeenv.BlockFor(worktree)                // may allocate (E1)
      → Store.Load(key) → Server (boundary)
      → Step(server, StartRequested)                  // core
      → Store.Commit { SaveServer(expectedVersion), AppendAudit }
      → interpret commands

interpret StartProcess                                 // goroutine owned by the service group
  → env := safety.EnvBuilder.Build + core env (PORT, YGG_PORT_*)
  → platform.Start(ctx, spec{Dir, Argv, Env}) with the task/server job
  → Step(Started) → output sink: redact → ring buffer + file + hub (topic devserver:{id}); each line also through ParseAnnouncement → Step(OutputAnnounced) (batched 250 ms)
  → process.Wait() → Step(Exited) → commit

interpret PollPorts      → platform.ListeningPorts(pid) → Step(PortsObserved)
interpret KillProcess    → platform.KillTask(serverJob)
interpret ScheduleWake   → timer owned by the group; fires Step(Wake)

// Startup
Store.LoadLive → for each: platform.ListOrphans (Unix) → Step(Recovered)
// Shutdown (before the store closes)
Service.Close → Step(ShutdownRequested) for every server → wait bounded (5 s) for Exited → job object ends the rest
```

Tests: `Run(initial, inputs)` for the core; the shell with `platformtest.Fake`, `dbtest` store and a fake clock.

## Failures

| Node | Failure | Class | Handling |
|---|---|---|---|
| `Store.Commit` | version conflict | retry | reload, rerun `Step`, bounded 3 times |
| `Store.Commit` | other | die | request fails with a plain message; nothing published, no process started yet |
| template expansion | bad placeholder | domain outcome | `Reject{BadTemplate}` with the plain message naming the placeholder |
| `platform.Start` | binary missing (`npm`, `dotnet`, `uv`) | domain outcome | `StartFailed{"Install X and try again"}` |
| `platform.Start` | other error | domain outcome | `StartFailed{plain}`; internal error logged |
| `ListeningPorts` | error | escape hatch | log, keep last ports, try next poll |
| process exits early (port in use, crash) | | domain outcome | `Failed{ExitedEarly/Crashed}`; last 50 log lines shown |
| port outside the block | | domain outcome | `Running` with a warning, never killed |
| `KillTask` | error or no exit within grace | escape hatch | second kill, then `Failed{StopFailed}`; job object at shutdown |
| log sink (file) | disk error | escape hatch | ring buffer keeps working, the file stops, one warning line |
| interpreter goroutine | panic | die | recovered, `Exited` fed for the attempt, stack logged |
| hub publish | slow client | escape hatch | hub drops for that client |
| process crash of `ygg` | | domain outcome | next start `Recovered`: Windows → `Stopped{Interrupted}` (job object killed them); Unix → `Orphaned` or `Stopped` |

## Requirements

| Capability | Live | Test | Contract |
|---|---|---|---|
| `DevServerStore` (`Load`, `LoadLive`, `Commit`) | `internal/db` | `dbtest` | row ⇄ `Server` round trip per state; inconsistent rows rejected (`Running` without pid); version conflict; `LoadLive` returns exactly Starting/Running/Stopping/Orphaned |
| `Processes` (`platform.Start`, `KillTask`, `ListeningPorts`, `ListOrphans`, `StopOrphan`) | `platform` | `platformtest.Fake` | existing `ProcessContract` plus: listener reported after it binds; grandchild listener counted; kill ends the tree; (Windows) tree dies when the host dies |
| `Wakes` (`ScheduleWake`) | timers in the service group | fake clock | fires once, cancelled when the context ends, ignored for a stale attempt |
| `Repos` (resolve target to a tracked directory) | `internal/repos`, `internal/worktrees` | fake | untracked or removed target is refused |
| `LogSink` | file + ring + hub | in-memory | ring keeps the last N lines in order; redaction applied; no write after close |
| `Clock`, `IDs` | real | fixed / counter | none |

## Boundary

| Source | Parser | Produces | Rejects |
|---|---|---|---|
| start request body | `devservers.ParseStart` | `{Target, CommandKey}` | unknown target type; command not offered by the toolchain; path-like input (targets are IDs) |
| toolchain/override argv | `devservers.ParseTemplate` | `Template` (argv with `{port}`/`{port:n}`) | empty argv; unknown placeholder; `{port:n}` with `n >= Block.Size`; `{port}` in `Argv[0]`; NUL or control characters; more than 64 args |
| process output line | `devservers.ParseAnnouncement` | `[]Port` | ports not in 1..65535; non-loopback host forms are ignored; only well-known patterns (`localhost:3000`, `127.0.0.1:8000`, `[::1]:5000`, `Now listening on: http://…`, `Uvicorn running on …`) |
| `dev_servers` row | `devservers.ServerFromRow` / `RowOf` | `Server` | unknown status; state with missing or extra fields; `Attempt < 1` live |
| `ListeningPorts` result | `devservers.NormalizePorts` | sorted unique ports | out of range (dropped and counted) |

Each gets: round trip (`ParseTemplate(Format(t)) == t`, `ServerFromRow(RowOf(s)) == s`), accepted ⇒ valid, one test per rejection rule, and a fuzz target for `ParseTemplate`, `ParseAnnouncement` and `ServerFromRow`.

## Persistence

Migration `00006` (F3) `dev_servers(id, repo_id, worktree_id nullable, command_key, status, attempt, pid, ports json, announced json, block_base, block_size, last_kind, last_code, since, version)` with a unique key on `(repo_id, worktree_id, command_key)`. State changes commit with their audit entry in one transaction. Logs are files under the data dir (rotated at 5 MiB, 3 kept) plus an in-memory ring (1000 lines); neither is in SQLite.

## Open questions

1. **Trust gate.** A dev server runs repo scripts (`npm run dev`). Recommendation: apply the same trust check as bootstrap/verify (a repo that ships its own agent config and is untrusted can be built by a human click, as today); no extra gate. Confirm there is no stricter rule you want.
2. **Main-checkout servers have no block.** A server started in a main checkout (not a worktree) gets no `PORT` injection and no warning; the framework default applies. Recommendation: yes (WE-02 is about worktrees).
3. **Restart policy.** No automatic restart, ever (D5). Recommendation: yes.
4. **Servers survive a browser tab close** and stop only on explicit stop or when `ygg` ends. Recommendation: yes.
