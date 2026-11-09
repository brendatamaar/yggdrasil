# Worktree environment — Model Functional Effect design
Profile: service

Covers: WE-01 (bootstrap profile: files copied into new worktrees), WE-02 (port blocks), WE-03 (optional docker compose per worktree), and decisions 1–3 in `docs/phases/phase-5.md`. WE-04 (short paths) is done by the `Ensurer` extraction (T3); WE-05 (toolchain bootstrap) is `internal/verify` and needs no new design.

Core: `internal/core/worktreeenv`, three pure pieces, no shared state between them. Shell: `internal/worktreeenv` (allocator service, profile interpreter, compose runner), plus one hook call from `worktrees.Ensurer`. `core.PortBlock{Base, Size}` lives in `internal/core` and is also used by `core/devservers`.

| Piece | What it decides |
|---|---|
| `Allocate(Table, Input) (Table, []Command)` | which block of 20 ports a worktree owns |
| `Profile(Job, Input) (Job, []Command)` | which profile files are copied into one new worktree |
| `Compose(Service, Input) (Service, []Command)` | lifecycle of one worktree's compose project |

Block size is 20, range default 20000–29999 (decision 2).

## 1. Port blocks

### State

`Table`: `Range{Lo, Hi}`, `Size` (20), `Live map[WorktreeID]Block`. Blocks are aligned: `Base = Lo + k*Size`, so live blocks cannot overlap by construction. Released blocks are kept as history rows in the database (`released_at`), not rows deleted.

### Inputs

`AllocateRequested{Worktree, Busy []Port, Now}` (the shell probes the machine and passes ports that are already listening), `Released{Worktree, Now}`, `RangeChanged{Lo, Hi}`, `Loaded{Blocks}` (startup).

### Transitions

| From | Input | Guard | To | Commands |
|---|---|---|---|---|
| worktree has a block | `AllocateRequested` | | unchanged | `Reply{Block}` (idempotent) |
| no block | `AllocateRequested` | a block index in range is free and none of its ports is in `Busy` (lowest first) | `Live[w] = block` | `PersistBlock{w, block}`, `Audit{worktreeenv.ports}`, `Reply{Block}` |
| no block | `AllocateRequested` | none free | unchanged | `Reply{Refused{RangeFull}}` |
| has block | `Released` | | block removed from `Live` | `ReleaseBlock{w}` (sets `released_at`), `Audit` |
| any | `RangeChanged` | valid range | `Range` replaced; existing blocks are **never moved** | `Audit` |
| any | `Loaded` | blocks pairwise disjoint, aligned or legacy | `Live` set | none; overlapping rows are dropped from `Live` with `Audit{worktreeenv.conflict}` |

### Invariants

| # | Invariant | Property test |
|---|---|---|
| E1 | Live blocks are pairwise disjoint after every step | `TestPropBlocksDisjoint` |
| E2 | Every newly allocated block lies inside the current range and avoids `Busy` | `TestPropAllocInsideRangeAndFree` |
| E3 | Allocation is idempotent: the same worktree gets the same block while it lives | `TestPropAllocIdempotent` |
| E4 | `RangeFull` only when no free, non-busy aligned block exists (no false refusal) | `TestPropRangeFullExact` |
| E5 | `RangeChanged` never changes `Live` | `TestPropRangeChangeKeepsBlocks` |
| E6 | `Released` frees a block for later reuse; a second `Released` does nothing | `TestPropReleaseIdempotent` |

## 2. Bootstrap profile (WE-01)

### State (one job per new worktree)

`Job`: `Idle` → `Observing{Entries, Pending}` → `Copying{Plan, Pending}` → `Done{Results}`. Results per entry: `Copied`, `Skipped{Reason}` (SourceMissing / NotRegularFile / DestExists / TooLarge / BudgetExceeded / AgentTasksOff), `Failed{Reason}`.

### Inputs

`CopyRequested{Worktree, Trigger, Entries, AgentCopy, Now}` where `Trigger` is a closed set: `UserStarted`, `UserConfirmed`, `AgentTask`. `Observed{Entry, Source, Dest}` where `Source` is `File{Size}` / `Missing` / `Directory` / `Link` / `Unreadable` and `Dest` is `Absent` / `Exists` (from `Lstat`, never following links). `Copied{Entry}`, `CopyFailed{Entry, Reason}`.

### Transitions

| From | Input | Guard | To | Commands |
|---|---|---|---|---|
| `Idle` | `CopyRequested` | `Trigger = AgentTask` and `AgentCopy` off | `Done{all Skipped(AgentTasksOff)}` | `Audit{profile.skipped}`, `Publish` |
| `Idle` | `CopyRequested` | no entries | `Done{}` | none |
| `Idle` | `CopyRequested` | otherwise | `Observing` | `Observe{e}` for each entry |
| `Observing` | `Observed{e}` | `Source = File`, `Dest = Absent`, size within the per-file cap and the total budget | entry planned | when all observed: `Copying` + `CopyFile{e}` for each planned entry |
| `Observing` | `Observed{e}` | any other combination | entry `Skipped{reason}` | none |
| `Copying` | `Copied{e}` / `CopyFailed{e}` | | result recorded | when none pending: `Done`, `Audit{profile.copied: names and counts}`, `Publish` |
| any | stale job or unknown entry | | unchanged | none |

Caps: 50 entries, 1 MiB per file, 5 MiB total; the core enforces them on observed sizes.

### Invariants

| # | Invariant | Property test |
|---|---|---|
| P1 | `CopyFile{e}` is emitted only for an entry observed as `File` with `Dest = Absent` in the same job (never overwrite, never a link or directory) | `TestPropCopyOnlyObservedFiles` |
| P2 | `Trigger = AgentTask` with the toggle off emits no `Observe` and no `CopyFile` | `TestPropAgentTasksOffCopiesNothing` |
| P3 | Every entry ends with exactly one result; `Done` accounts for all entries | `TestPropEveryEntryResolved` |
| P4 | Total copied size never exceeds the budget; no entry beyond the cap is copied | `TestPropBudgetRespected` |
| P5 | Audit entries carry counts and relative names only, never file contents | `TestAuditHasNoContents` |
| P6 | A stale job's inputs change nothing | `TestPropStaleJobIgnored` |

### Interpreter rules (shell, tested in T-maps)

`Observe` uses `Lstat` on every path component of the source (reuse the existing reparse checks in `platform`), refuses a link or reparse point at any level, and requires the cleaned path to stay inside the repo and outside `.git`. `CopyFile` creates the destination with exclusive-create (`O_EXCL`), so an existing file is never overwritten even in a race, copies bytes as they are (CRLF preserved), and removes nothing; on a failed copy it leaves the partial file and reports it (no deletion, invariant 1). A failed copy is therefore visible in two places: the entry is `Failed` in the result and counted in the `profile_copied` audit entry (`failed` count), and a partial file may remain in the worktree, which the user can delete by hand; a retry never overwrites it (`DestExists`).

Known window: the destination's parent folders are checked with `Lstat` right before the file is created, but another process could replace a parent with a link between that check and the open. The worktree is new, owned by Yggdrasil and normally not written by anything else at that moment; the residual window is listed in `docs/threat-model.md` by the lead. The source side re-checks with `os.SameFile` after opening.

Port probing goes through `platform.BusyPorts(lo, hi)`: one read of the operating system's listener table (`/proc/net/tcp*` on Linux, `GetExtendedTcpTable` on Windows), so a listener on any address counts as busy and no port is bound. A failed probe is logged as a warning (no error text, it can hold file names) and the block is treated as free. A conflict between stored blocks (`worktreeenv.conflict`) is audited once per process per distinct conflict, not on every allocation. `Prepare` runs inline after the worktree is recorded, and its failure is logged by default and never fails `Ensure`. Profile settings are written with one statement that touches only the profile columns, so a concurrent compose change is not lost.

## 3. Compose (WE-03, task E2, droppable)

### State (one per worktree, in memory; reconciled with `ps` at startup)

| State | Data |
|---|---|
| `Off` | compose is not enabled for the repo |
| `NeedsConfirm` | `FileHash` (the compose file changed since the user last confirmed it, or was never confirmed) |
| `Down` | `Confirmed` hash |
| `Starting` | `Op`, `Attempt`, `Since` |
| `Up` | `Attempt`, `Since` |
| `Stopping` | `Op` (Stop / Down), `Attempt` |
| `Failed` | `Reason` (DockerMissing / DaemonDown / ComposeError{summary}), `At` |

### Inputs

`Enabled{File, Hash}` / `Disabled`, `Confirmed{Hash}` (user clicked, UI), `FileChanged{Hash}`, `UpRequested`, `StopRequested`, `DownRequested`, `OpFinished{Attempt, Result}`, `Recovered{Ps}` (`Running` / `NotRunning` / `Unavailable`), `Now`.

### Transitions (summary)

| From | Input | Guard | To | Commands |
|---|---|---|---|---|
| `Off` | `Enabled{hash}` | | `NeedsConfirm{hash}` | `Publish` |
| `NeedsConfirm{h}` | `Confirmed{h}` | hash matches the current file | `Down{h}` | `Audit{compose.confirmed}` |
| `Down{h}` / `Failed` | `UpRequested` | | `Starting{Up}` | `RunCompose{Up}`, `Audit` |
| `Starting` | `OpFinished{ok}` | | `Up` | `Audit`, `Publish` |
| `Starting`/`Stopping` | `OpFinished{err}` | | `Failed{reason}` | `Audit`, `Publish` |
| `Up` | `StopRequested` | | `Stopping{Stop}` | `RunCompose{Stop}`, `Audit` |
| `Up` / `Down` | `DownRequested` | | `Stopping{Down}` | `RunCompose{Down}`, `Audit` |
| `Stopping` | `OpFinished{ok}` | | `Down{h}` | `Audit`, `Publish` |
| `Down`/`Up`/`Starting` | `FileChanged{h' ≠ h}` | | `NeedsConfirm{h'}` for `Down`; `Up` stays up and gets a `Publish` warning; `Starting` unchanged | `Publish` |
| `NeedsConfirm` | `UpRequested` | | unchanged | `Reject{ConfirmFirst}` |
| any | `Disabled` | | `Off` (no command run; containers stay as they are) | `Audit` |
| startup | `Recovered{Running}` | | `Up` | none |
| startup | `Recovered{NotRunning/Unavailable}` | | `Down` / `Failed{DockerMissing}` | none |

`RunCompose{Op}` has a closed `Op` set: `Up`, `Stop`, `Down`, `Ps`. The command has **no field for extra arguments**, so `-v`, `--volumes`, `--rmi`, `--file` outside the worktree and `--remove-orphans` cannot be expressed.

### Invariants

| # | Invariant | Property test |
|---|---|---|
| C1 | `RunCompose{Up}` is emitted only from `Down` or `Failed` with a confirmed hash equal to the file's current hash | `TestPropUpNeedsConfirmedHash` |
| C2 | The only ops ever emitted are `Up`, `Stop`, `Down`, `Ps`; a table test pins the exact argv per op (`docker compose -p ygg-<name> --file - [--project-directory <dir>] [--env-file <dir>/.env] up -d`, `stop`, `down`, `ps --format json`; the compose file comes on standard input) | `TestOpsAreClosedSet`, `TestArgvTable` |
| C3 | At most one live operation per worktree | `TestPropOneOperation` |
| C4 | `Off` emits no `RunCompose` | `TestPropOffRunsNothing` |
| C5 | A changed file never lets `Up` run until reconfirmed | `TestPropChangedFileNeedsConfirm` |
| C6 | Every status change is audited and published | `TestPropStatusChangeAudited` |
| C7 | Nothing is executed at app shutdown: compose containers are daemon-owned and outlive `ygg` on purpose (they hold user data). The UI shows them again after restart through `Recovered` | `TestShutdownRunsNothing` |

## Call graph

```go
// Worktree creation (T3 hook, E1)
worktrees.Ensurer.Ensure
  → worktreeenv.Prepare(ctx, worktree, trigger)
      → Allocate: probe ports (platform) → Step(AllocateRequested) → commit PersistBlock + audit
      → Profile: settings.Load(repo) → Step(CopyRequested) → Observe/CopyFile via FileSystem → Step(...) → commit audit

// Dev servers ask for a block
devservers.Service.Start → worktreeenv.BlockFor(worktree)

// Compose (E2): POST /api/worktrees/{id}/compose/{up|stop|down}
api → ParseOp → Service.Compose → hash(file) → Step(...) → interpret RunCompose
interpret RunCompose
  → env := safety.EnvBuilder.Build + YGG_PORT_BASE, YGG_PORT_0..n   // the compose file references ${YGG_PORT_0}
  → platform.Start(ctx, docker compose argv fixed by Op, Dir = worktree)
  → Step(OpFinished)
```

### 3.1 What the confirmation covers (round 2 of the final gate)

- **The hash covers the compose file and the sibling `.env`** in the same folder (`core.ComposeHash`: both parts length-prefixed; an absent `.env` hashes like an empty one). Both are read with the same link and reparse-point refusal and 1 MB cap as the profile copy (`ComposeFiles.Read` / `ReadOptional`). `--env-file <dir>/.env` is passed only when that file exists, so docker reads no other environment file; `--project-directory <dir>` is passed when the compose file is in a folder, so relative paths mean the same as before.
- **A compose file that reads other files is refused**, with a plain message and no run: top-level `include:`, and `extends:` or `env_file:` anywhere (`core.ScanComposeFile`, a line-level scan that over-refuses on purpose; table tests and a fuzz target). The shell treats such a file as having no hash, so it can be neither confirmed nor started.
- **Executed bytes equal hashed bytes.** The shell reads and hashes the file again in the goroutine that runs docker, after any waiting; for `up`, a hash different from the confirmed one means nothing runs, the operation ends `ComposeFailed`, and `FileChanged` moves the state to `NeedsConfirm`. The compose file is passed on standard input (`--file -`), so it cannot change between that hash and docker reading it. Residual window: the `.env` is read by docker from disk, so it can still change between the re-hash and that read. `stop` and `down` use the file as it is now (they only act on what ygg started, and a changed file must not stop the user from stopping it); they refuse an unreadable or indirect file, so a project whose compose file was deleted must be stopped with docker directly.
- **Bind mounts to host paths are the user's call.** The file can mount any host folder; ygg shows the file and the user decides. The confirmation stops a file the user has not seen, not a file the user has approved.
- **The confirmed hash is per repo.** Another worktree of the repo with the same content (same compose file and `.env`) is confirmed too. The audit entry of that automatic confirmation (`compose.confirmed` or `compose.enabled`) carries `autoConfirmed: true` and `sourceWorktree`, the worktree where the user last confirmed (kept in memory, so it is empty after a restart; the hash in the entry still identifies the content).
- **The API requires the hash** of the file the user was shown to confirm (400 without it), and the service says so when an action does nothing in the current state (409), instead of answering success.
- **The service never holds its lock while docker runs.** Startup recovery (`ps`) runs outside the lock and is bound to the service context; operations end in exactly one `OpFinished` even when the port-block lookup or the audit write fails (audit retried once, then the transition is made in memory), so a state is never left in Starting or Stopping without a goroutine. Disabling compose forgets that recovery ran, so enabling it again while containers still run shows them as up.

Ports are "offset into the block" by environment, not by rewriting the compose file; if the file never mentions `YGG_PORT`, the UI shows a warning.

## Failures

| Node | Failure | Class | Handling |
|---|---|---|---|
| `Store.Commit` (blocks) | unique violation / version conflict | retry | reload, re-run `Allocate`, bounded 3 times |
| allocation | range full | domain outcome | `Refused{RangeFull}`: plain message "no free port block, widen the range in settings"; the worktree still works, dev servers start without injection and a warning |
| port probe | error | escape hatch | treat the block as free, log |
| profile `Observe` | path escapes the repo, link, reparse, `.git` | domain outcome | `Skipped{NotRegularFile}` (never copied) |
| profile `CopyFile` | exists (race), locked, no space | domain outcome | `Failed{reason}` for that entry; others continue |
| compose | docker not installed / daemon down | domain outcome | `Failed{DockerMissing / DaemonDown}` with the plain "install or start Docker Desktop" message |
| compose | non-zero exit | domain outcome | `Failed{ComposeError}` with the last lines (redacted) |
| compose file | unreadable | domain outcome | `NeedsConfirm` cannot be computed → `Failed{ComposeError}` |
| interpreter goroutine | panic | die | recovered, `OpFinished{err}` fed |

## Requirements

| Capability | Live | Test | Contract |
|---|---|---|---|
| `PortStore` (`LoadBlocks`, `SaveBlock`, `ReleaseBlock`) | `internal/db` | `dbtest` | round trip; unique `Base` among live rows; release then reuse; version conflict |
| `SettingsStore` (profile paths, agent-copy toggle, compose opt-in and confirmed hash) | `internal/db` | `dbtest` | round trip; defaults (toggle off, compose off); unknown repo errors |
| `PortProber` (`Busy(range) []Port`) | `platform.BusyPorts` | fixed list | a real listener on the wildcard address is reported; a free port is not |
| `FileSystem` (`Observe`, `CopyExclusive`) | `os` + `platform` reparse checks | in-memory map | `O_EXCL` never overwrites (including concurrent writers); links and reparse points refused at any path level (skipped without privilege on Windows); CRLF bytes preserved; size reported correctly; nothing is removed |
| `ComposeRunner` (`Run(Op, project, file, env) Result`) | `platform.Start` + docker | recorded results | argv shape per op; missing docker maps to `DockerMissing`; real runs skip when docker is absent |
| `Clock`, `IDs` | real | fixed | none |

## Boundary

| Source | Parser | Produces | Rejects |
|---|---|---|---|
| port range setting | `worktreeenv.ParseRange` | `Range` | not `lo-hi`; `lo < 1024`; `hi > 65535`; fewer than 20 ports; non-numeric |
| profile paths (API, settings) | `worktreeenv.ParseProfile` | `[]RelPath` | empty; absolute, drive-letter or UNC; any `..` after cleaning; any `.git` component (case-insensitive); NUL or control characters; glob characters `* ? [`; Windows reserved names (`CON`, `NUL`, …) and trailing dot or space; over 260 characters; more than 50 entries; duplicates after case folding (merged); backslashes normalised to `/` |
| compose file setting | `worktreeenv.ParseComposeFile` | `RelPath` | same rules as above plus an extension other than `.yml`/`.yaml` |
| compose project name | `worktreeenv.ParseProjectName` | `ygg-<name>` | characters outside `[a-z0-9-]`; longer than 40; empty |
| `worktree_ports` rows | `worktreeenv.BlockFromRow` / `RowOf` | `Block` | misaligned base, size ≠ 20 for new rows, out of 1..65535 |
| `docker compose ps` JSON | `worktreeenv.ParsePs` | `Running` / `NotRunning` | undecodable (treated as `Unavailable`) |

Each gets: round trip, accepted ⇒ valid, one test per rejection rule, and a fuzz target for `ParseProfile`, `ParseRange` and `ParsePs`.

## Persistence

Migration `00006` (F3): `worktree_ports(worktree_id unique among live, base, size, allocated_at, released_at)`, `repo_env_settings(repo_id, profile_paths json, agent_copy, compose_enabled, compose_file, compose_confirmed_hash)`. Defaults: agent copy off, compose off. Compose runtime state and profile jobs are not stored (they are derived or one-shot). Every change to settings and every allocation commits with its audit entry.

## Open questions

1. **Compose file trust (new, added by the design).** A compose file lives in the worktree, which an agent can edit; a malicious file could mount host folders. Recommendation: hash-confirm (state `NeedsConfirm`): the user confirms the file once, any later change requires a new confirmation, and the UI shows the file before confirming. Not required by the PRD; say if you want it dropped.
2. **Containers outlive `ygg` (C7).** Recommendation: yes, because they hold data and are not "processes of ours"; the manual checklist notes it. Alternative: `stop` (never `down`) at shutdown.
3. **Ports busy in the range.** `Busy` probing skips blocks that contain a port already listening. Recommendation: yes. Alternative: no probe (simpler, may hand out a block with a foreign listener).
4. **Range change with live blocks.** Existing blocks stay where they are (E5). Recommendation: yes.
5. **Profile copy on re-ensure.** Copy runs only when a worktree is created or when the user presses "Copy profile files" on an existing worktree (`UserConfirmed`); it never re-runs by itself. Recommendation: yes.
