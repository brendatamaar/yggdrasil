# Repo status — Model Functional Effect design

Covers:

- WS-02: branch, uncommitted changes, ahead/behind, last commit, health flags;
- WS-03: filters All, Uncommitted, Behind, Ahead, Failed pipeline, Problems;
- WS-04: live counts on the Projects page tabs;
- the Phase 2 performance target: status refresh for 50 repos in under 10 seconds.

Core: `internal/core/repos` (`status.go`, `refresher.go`). Shell: `internal/repos` (`status_service.go`).

Status is a **read model**. It changes no user data, so it is held in memory, is not persisted and is not audited. After a restart the first refresh rebuilds it.

## State map

```go
type Refresher struct {
    Entries  map[core.RepoID]Entry
    Queue    []core.RepoID   // waiting to be read; each ID at most once
    InFlight map[core.RepoID]int // repo → attempt being read
    Orphans  map[core.RepoID]int // repo → read still running whose result is no longer wanted
    Seq      int             // last attempt number handed out (monotonic, never reset)
    Max      int             // concurrent reads (default 8)
}
type Entry struct {
    Path    CleanPath
    Attempt int             // last attempt started (a Seq value)
    View    View            // sealed, below
}
```

`View` (sealed) is what the UI shows for one repo:

- `Unknown{}`: tracked but never read (just started, or just tracked).
- `Fresh{Snap Snapshot, At time.Time}`: last read succeeded.
- `Problem{Kind ProblemKind, At time.Time, Last *Snapshot}`: last read failed. `Last` is the previous good snapshot, kept so the row doesn't go blank.

A repo that is being read keeps its current `View`. "Refreshing" is `InFlight[id]`, not a separate view, so the UI shows the last data plus a spinner. A repo can be in `InFlight` and also in `Queue`: a request arrived during the read. It waits (it is never started beside its own read) and is read again once that read ends. Attempts come from `Seq`, so they are unique across IDs and across remove and re-add. When a repo is removed or moved while its read runs, the read becomes an `Orphans` entry: it keeps counting toward `Max` (the shell worker still runs) until its result arrives, which frees the slot and is otherwise ignored. A repo with an orphan is not started again until the orphan ends. After a panic the shell rebuilds the state with `Restarted()`, which keeps `Seq` and turns every running read into an orphan.

```go
type Snapshot struct {
    Branch      string        // "" when detached
    Detached    bool
    Upstream    string
    HasUpstream bool
    Ahead, Behind int
    Staged, Unstaged, Untracked, Conflicted int
    LastCommit  *CommitRef    // nil before the first commit
}
type CommitRef struct{ OID, Subject, Author string; At time.Time }
type ProblemKind string // "missing" | "not_a_repo" | "timed_out" | "git_failed"
```

Health flags are derived by `Flags(View) []Flag`, which is pure:

- `missing`, `not_a_repo`, `timed_out`, `git_failed`: from a `Problem` view;
- from a snapshot: `detached`, `no_upstream`, `conflicts` (Conflicted > 0), `diverged` (Ahead > 0 and Behind > 0).

"Problems" (WS-03) means a repo with any of these flags.

## Inputs

| Input | From | Fields |
|---|---|---|
| `ReposChanged` | repo-tracking after commit; startup | `Active []RepoRef{ID, Path}` (the full active set) |
| `RefreshRequested` | user ("refresh"), window focus (UI), after a git action, after a build finishes, after tracking | `IDs []core.RepoID` (empty = all), `Reason`, `Now` |
| `Tick` | shell ticker (every 30 s) | `Now` |
| `StatusRead` | interpreter | `ID, Attempt, Snap Snapshot, Now` |
| `StatusFailed` | interpreter | `ID, Attempt, Kind ProblemKind, Now` |

`Reason` is one of `user`, `focus`, `git_action`, `build`, `tracking` or `tick`. `focus` and `tick` requests skip repos read less than `MinAge` ago. `MinAge` is 15 s for `focus` and 2 min for `tick`, against the `At` of the current view. All other reasons always enqueue.

## Transition table

`start(id)` takes the first queued `id` that is not being read (not in `InFlight` or `Orphans`), moves it into `InFlight` with attempt `Seq+1` (incrementing `Seq`) and emits `ReadStatus{id, path, attempt}`. After every input, the core calls `start` while `len(InFlight)+len(Orphans) < Max` and a startable ID is queued.

| From | Input | Guard | To | Commands |
|---|---|---|---|---|
| any | ReposChanged | — | add an `Unknown` entry for each new ID and enqueue it; drop entries for IDs not in `Active` (also from `Queue`; their read in flight becomes an orphan; a changed path does the same and starts over) | `ReadStatus` for started reads, `Publish(StatusChanged{removed})`, `PublishCounts` |
| any | RefreshRequested | per ID: known; not already in `Queue`; age ≥ MinAge(reason) | enqueue the ID at the back (`user` and `git_action` go to the front) | `ReadStatus` for started reads |
| any | Tick | — | as `RefreshRequested{all, tick}` | as above |
| entry in flight with attempt `a` | StatusRead | `Attempt == a` | `View = Fresh{Snap, Now}`, removed from `InFlight` | `Publish(StatusChanged{id})` if the view differs; `PublishCounts` if the counts changed; `ReadStatus` for started reads |
| entry in flight with attempt `a` | StatusFailed | `Attempt == a` | `View = Problem{Kind, Now, Last: previous snapshot}`, removed from `InFlight` | same as StatusRead |
| any | StatusRead / StatusFailed | the attempt is an orphan's | remove the orphan (frees its slot); views unchanged | `ReadStatus` for started reads |
| any | StatusRead / StatusFailed | unknown ID, or stale attempt | no change | none |

**Any other (state, input) pair: no change, no commands.**

Counts are pure: `Counts(entries, failedPipelines set[RepoID]) Counts`, with one number per WS-03 filter, overall and per project. `failedPipelines` comes from git-hosts, and the shell passes it in when it builds the read model. The status core does not know about hosts.

## Invariants

| # | Invariant | Property test |
|---|---|---|
| R1 | `len(InFlight) + len(Orphans) ≤ Max` after every step. | `TestPropInFlightBounded` |
| R2 | Each ID appears at most once in `Queue`. Every ID in `Queue` or `InFlight` has an entry. An ID is never in `InFlight` and `Orphans` at once; attempts never exceed `Seq`. | `TestPropQueueSetLike` |
| R3 | Work-conserving: after every step, if `len(InFlight) + len(Orphans) < Max` then every ID in `Queue` is itself being read (in `InFlight` or `Orphans`) and waits for that read to end. | `TestPropNoIdleWhileQueued` |
| R4 | A result is fresh only if it answers the latest `ReadStatus` emitted for its ID and no `ReposChanged` removed or moved the ID since (judged from the test's own history, not from `InFlight`). A fresh result is applied; a stale one publishes nothing, changes no view, and changes nothing at all unless it is an orphan's. The generator removes and re-adds IDs while their reads are in flight and replays late results of any read ever emitted. | `TestPropStaleResultIgnored` |
| R5 | A `ReadStatus` for `(id, a)` is emitted at most once over the whole run, across IDs and across remove and re-add; every `a` is greater than the previous `Seq`. | `TestPropAttemptsMonotonic` |
| R6 | `Problem.Last` is the most recent `Fresh` snapshot of that repo, or nil if it never had one. | `TestPropProblemKeepsLast` |
| R7 | `Counts` equal a brute-force recount from `Flags` and `Snapshot` for every filter. | `TestPropCountsMatchFlags` |
| R8 | Liveness: with `Max ≥ 1`, feeding every emitted `ReadStatus` back as a result eventually leaves `Queue` and `InFlight` empty, and every requested ID has a view at or after the request time. | `TestPropEveryRequestServed` |
| R9 | `PublishCounts` is emitted only when the counts changed. | `TestPropCountsPublishedOnChange` |

## Commands

| Command | Cardinality | Idempotent | Produces |
|---|---|---|---|
| `ReadStatus{ID, Path, Attempt}` | one | yes (read-only git; deduplicated by `(ID, Attempt)`) | `StatusRead` or `StatusFailed` |
| `Publish(StatusChanged{ID})` | one | yes | — |
| `PublishCounts` | one | yes | — |

## Call graph

```go
// Production
statusService (internal/repos) — one goroutine owns the Refresher; inputs arrive on a channel
  startup: store.ListActiveRepos → ReposChanged → Tick ticker (30 s, owned by the service errgroup)
  input → repos.Refresh(state, input) [core] → state2, cmds
  ReadStatus → worker goroutine (errgroup, bounded by Max via the core):
      ctx, cancel := context.WithTimeout(svcCtx, 15 s)
      os.Stat(path) → missing → StatusFailed{missing}
      git.Status(ctx, path)                  // porcelain v2, parsed by internal/git (tested)
      git.Log(ctx, path, 1)                  // last commit
      → repos.SnapshotFrom(status, commit)   [boundary: git types → core Snapshot]
      → error classes: ctx deadline → timed_out; git "not a git repository" → not_a_repo; other → git_failed
      → send StatusRead / StatusFailed to the service channel
  Publish → hub.publishNext(RepoTopic{ID}, StatusDTO) and hub.publishNext(ReposTopic{}, CountsDTO)

HTTP GET  /api/repos?project=&filter=   → service.Snapshot() (copy under the owner goroutine) + hosts.FailedPipelines() → repos.Filter/Counts [core] → DTOs
HTTP POST /api/repos/refresh {ids}      → service.Request(RefreshRequested{user})
UI window focus                          → POST /api/repos/refresh {reason: focus}
git actions / builds / tracking (after commit) → service.Request(…)
```

```go
// Tests
repos.RunRefresh(initial, inputs) (core) → R1–R9
statusService with fake StatusReader (scripted delays, failures) and fixed clock
perf test (B4): 50 generated repos in t.TempDir (fixtures generator), real git, Max 8 → full refresh < 10 s
```

## Failures

| Node | Failure | Class | Handling |
|---|---|---|---|
| os.Stat | folder gone | domain outcome | `StatusFailed{missing}` → "Missing" flag; rescan offers to stop tracking |
| git.Status | not a repo | domain outcome | `StatusFailed{not_a_repo}` |
| git.Status / Log | timeout (15 s, e.g. antivirus, network drive) | domain outcome | `StatusFailed{timed_out}`, process killed by ctx; the next tick retries (MinAge applies) |
| git.Status / Log | other git error (lock file, corrupt index) | domain outcome | `StatusFailed{git_failed}`; the user message comes from `git.UserMessage` and is kept in the DTO |
| git output | unparseable | die (for that read) | `internal/git`'s parser returns an error → `git_failed`, logged with the raw text truncated |
| core | invariant broken | die | recovered in the owner goroutine, logged; the service restarts its state from `ReposChanged` |
| publish | slow client | escape hatch | hub drops it; the client refetches |

There are no retries inside one read. Tick and the next request are the retry schedule, which is safe because reads are idempotent.

## Requirements

| Capability | Live | Test | Contract |
|---|---|---|---|
| `StatusReader` (`Read(ctx, path) (Snapshot, ProblemKind, error)`) | `internal/git` Status + Log + os.Stat | scripted fake (delay, result per path) | `StatusReaderContract`: clean repo; dirty counts; ahead/behind with an upstream; detached; no upstream; no commits; missing folder → missing; plain folder → not_a_repo; ctx deadline → timed_out. Runs against the fake and against real git on `t.TempDir` repos (git is required in CI) |
| `ActiveRepos` (`ListActiveRepos`) | `internal/db` | SQLite in a temp dir | covered by `WorkspaceStoreContract` (repo-tracking) |
| `FailedPipelines` (`set[RepoID]`) | git-hosts service | static set | — |
| `Publisher` | hub | recorder | — |
| `Clock` | shell | fixed | — |

## Persistence and delivery

- **Nothing persisted.** The status lives in memory. On startup the service receives `ReposChanged` with every active repo, and they are all queued.
- **Timers:** the 30 s `Tick` ticker, owned by the service. A late tick only causes extra reads that `MinAge` filters out.
- **Delivery:** every input goes through one channel to the owner goroutine, so there is no lock around state. Worker goroutines only send results back. When the service context is cancelled, workers stop (git processes are killed through ctx and `platform.Start`) and the owner returns.
- **Perf target:** `Max = 8`. Each read is one `git status --porcelain=v2 --branch` plus one `git log -1`. On a typical laptop that is about 0.2–0.6 s each, so 50 repos take about 2–4 s. The B4 perf test checks it on 50 generated repos and fails above 10 s.
- **Shell invariants (chaos):**
  - S1: never more than `Max` git status processes at once;
  - S2: no git process outlives the service (`platform.Start` + ctx);
  - S3: `git status` is called with `--no-optional-locks`, so a read never takes `index.lock` and can't block a user's git command or an agent's.

## Boundary

| Source | Parser | Produces | Rejects |
|---|---|---|---|
| `git status --porcelain=v2 --branch` | `internal/git.parseStatus` (exists, tested with malformed input) | `git.Status` | malformed headers or lines → error |
| `git log -1` | `internal/git.parseCommit` (exists) | `git.Commit` | malformed record |
| `git.Status` + `git.Commit` | `repos.SnapshotFrom` | `repos.Snapshot` | negative counts, or Detached together with a Branch name (die) |
| HTTP `POST /api/repos/refresh` | `repos.ParseRefresh` | `RefreshRequested` | unknown reason, more than 1,000 IDs, invalid IDs |
| HTTP `GET /api/repos?filter=` | `repos.ParseFilter` | `Filter` (closed set: all, uncommitted, behind, ahead, failed_pipeline, problems) | unknown filter |

Each parser gets round-trip, accepted ⇒ valid, one rejection test per rule, and fuzz: `FuzzSnapshotFrom`, `FuzzParseRefresh`, `FuzzParseFilter`. `internal/git` already has a status fuzz target; check it, and add one if it is missing.

**`--no-optional-locks`:** this is a change to `internal/git.Status`. Pass it as `git --no-optional-locks status …`, and add a test that `index.lock` is never created during a read.

## Files

- **Core, `internal/core/repos`:**
  - `status.go` (`Snapshot`, `View`, `Flags`, `Counts`, `Filter`);
  - `refresher.go` (`Refresh`, `RunRefresh`);
  - `refresher_prop_test.go` (R1–R9), written first.
- **Shell, `internal/repos`:**
  - `status_service.go`;
  - `status_reader.go`, plus `StatusReaderContract` in `internal/repos/repostest`;
  - `parse_status.go` (`ParseRefresh`, `ParseFilter`, `SnapshotFrom`) and its fuzz tests.
- **`internal/git`:** `--no-optional-locks` on Status, plus its test.
- **`internal/api`:** the status part of `repos.go`, the status DTOs in `types_repos.go`, and the `ReposTopic`/`RepoTopic` variants in `topic.go`.

## Open questions

Approved by the human on 2026-10-18 with every recommendation below.

1. **Background fetch:** ahead/behind is relative to the last fetch. Should ygg fetch in the background? *Recommend no* in Phase 2: fetching is an explicit WS-06 action ("fetch", "fetch all"), and the UI shows "last fetched" per repo. Add an opt-in auto-fetch later.
2. **File watching:** use no filesystem watcher. Refresh on focus, on tick, after actions and on request. *Recommend as stated*: watchers on Windows are noisy with antivirus and big repos.
3. **Tick interval and MinAge:** 30 s tick, 2 min `MinAge` for tick, 15 s for focus. *Recommend as stated.*
