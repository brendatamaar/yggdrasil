# Change sets and review notes — Model Functional Effect design
Profile: service

Covers RV-03 (proposals of one multi-repo task grouped; confirm all or per repo; linked MRs), RV-04 (merge order, dependency warnings) and RV-01 (inline review notes sent back to the agent). Local merge and worktree removal are in `docs/design/landing.md`; verification is in `docs/design/verify.md`.

Core packages: `internal/core/changesets` (set state, order, warnings, link rendering) and `internal/core/review` (notes). Shell: `internal/changesets` (service, interpreter), `internal/db`, `internal/api`. Core packages cannot import each other, so facts one needs from another (repo dependency edges, proposal statuses, current diff hashes) are fields of its inputs.

## What already exists (read, not designed here)

- `change_sets` table and `core.ChangeSet{ID, TaskID, Status open|shipped|closed, MergeOrder}`.
- `proposals.Service.Create` already calls `tx.EnsureChangeSet`: **a change set is created lazily with the first proposal of a task, one per task.** This design does not create sets.
- Proposals are hash-confirmed: the user confirms `ActionHash`, and a different hash is refused (`Confirm(id, hash, by)`).
- `ParsePushOutput` reads the MR URL from the push output (`proposals.Result.MRURL`).

## Decision recorded: linked MRs without a second push (deviates from phase-4.md decision 4)

Decision 4 planned a pass 2 that re-pushes earlier branches with a new description. That cannot work: `git push` of a ref the remote already has says "Everything up-to-date" and never contacts `receive-pack`, so push options are never sent, and a changed ref needs a new commit or a forced push (banned). So:

- Each MR proposal's `Description` may contain one marker line, `<!-- ygg:links -->`. The **template** is what the user confirms and what `ActionHash` covers; `Describe` says "links to related merge requests are added here when it is created".
- At execution time the MR executor replaces the marker with the rendered link block (`RenderLinks`, pure): the set's repos in merge order, each as `repo (branch)` and, **for MRs already created by earlier members of the set, their URL** (checked `SafeWebURL`, same host as the remote). Later MRs are listed by repo and branch only.
- Links therefore point backwards (later MR → earlier MR). The change-set page in Yggdrasil always shows the full linked set. Two-way links need a host API write (token with write scope), which is out of scope for v0.5; recorded as a limitation.

This keeps "what you confirm is what runs": only URLs the user could not have known are filled in, from a closed, validated source.

## State map

### Change set

One aggregate per change set. Members are **not** copied: they are loaded from the `proposals` rows of the set (single source of truth) and passed in as part of the state.

```go
type State interface{ isState() }

type Open struct {
    Set      Header                  // ID, Task, Repos (task scope), Order []RepoID
    Members  []Member                // proposal ID, repo, kind, status, action hash
    Links    map[core.RepoID]string  // MR URL by repo, from executed merge_request members
    LastRun  RunResult               // Idle | Halted{At ProposalID, Reason} | Completed
}
type Confirming struct {             // a confirm-all or per-repo run is in progress
    Open
    Scope Scope                      // All | Repo(r)
    Queue []Planned                  // proposal IDs still to confirm, with the hash the user saw
    Current core.ProposalID          // confirmed, waiting to reach a terminal status
}
type Shipped struct{ Open }          // every member terminal, at least one executed, none failed
type Closed struct{ Open }           // every member rejected, or closed by the user

type Member struct {
    ID     core.ProposalID
    Repo   core.RepoID
    Kind   core.ProposalKind
    Status core.ProposalStatus      // pending | confirmed | rejected | executed | failed ...
    Hash   string
}
```

`Scope`: `All` or `Repo{ID}`. `RunResult` (sealed): `RunIdle` · `RunHalted{At, Reason}` (a refused or failed member stopped the run; the rest stay pending) · `RunCompleted`.

"Retrying" is not a state: a failed member is retried through the proposals service (`Retry`), and the next `ProposalChanged` moves the set on. "Partial" is `Open` with a mix of member statuses, which is normal.

Derived status for the DB column: Open/Confirming → `open`, Shipped → `shipped`, Closed → `closed`.

### Review note

One aggregate per note: `Note{ID, Task, Repo, Anchor, Text, DiffHash, Created}` plus sealed state:

```go
type NoteState interface{ isNoteState() }
type NoteOpen struct{}                         // written, not sent
type NoteSending struct{ Batch BatchID; At time.Time }  // composed into a message, waiting for the task to accept it
type NoteSent struct{ Turn core.TurnID; At time.Time }
type NoteOutdated struct{ At time.Time }       // the file changed after the note was written, before it was sent
```

`Anchor{Path RelPath, Line int, Side old|new}`; `DiffHash` is the hash of that file's diff when the note was written.

## Inputs

All carry `Now`. IDs are generated by the shell.

**Change set**

| Input | From | Fields |
|---|---|---|
| `Loaded` | not an input: the shell builds the state from rows (`StateFromRows`, rejects inconsistent rows) | — |
| `MembersChanged` | proposals topic or reconcile: any member's status/hash/kind set changed, or a member was added | `Members, Links, Now` |
| `SetOrder` | user | `Order []RepoID, Deps []Edge, Now` |
| `ConfirmRun` | user ("Confirm all" / "Confirm repo") | `Scope, Seen map[ProposalID]hash, Ack []WarningCode, Deps []Edge, Now` |
| `StopRun` | user | `Now` |
| `ConfirmRefused` | interpreter: `proposals.Confirm` refused (stale hash, not pending, untracked repo) | `ID, Reason, Now` |
| `Close` | user | `Now` |

`Edge{Dependent, Dependency core.RepoID}` is read by the shell from the toolchains' `Orderer` (Maven at v1; empty for other toolchains). Warning codes: `dependent_before_dependency{Dependent, Dependency}`.

**Review note**

| Input | From | Fields |
|---|---|---|
| `AddNote` | user | `Note (parsed), Now` |
| `EditNote` | user | `ID, Text, Now` |
| `DeleteNote` | user | `ID, Now` |
| `SendNotes` | user | `Notes []NoteID, Batch, Now` |
| `SendAccepted` | interpreter: `tasks.Message` accepted | `Batch, Turn, Now` |
| `SendRefused` | interpreter: `tasks.Message` refused (task running, queue full) | `Batch, Reason, Now` |
| `DiffObserved` | reconcile: current per-file diff hashes | `Hashes map[RelPath]hash, Now` |
| `Recover` | startup: notes found in `NoteSending` | `Now` |

## Transition table

### Change set (`StepSet`)

Every changing transition emits `WriteAudit(changeset.<action>)` and `Publish(ChangeSetChanged)`.

| From | Input | Guard | To | Extra commands |
|---|---|---|---|---|
| Open, Shipped, Closed | MembersChanged | — | recompute: Open if any member is pending/confirmed or failed, or no member is executed; Shipped if all terminal ∧ ≥1 executed ∧ none failed; Closed if all rejected | — |
| Confirming | MembersChanged | `Current` reached `executed` | Confirming with the next planned (or Open{Completed} when the queue is empty) | `ConfirmProposal(next)` |
| Confirming | MembersChanged | `Current` reached `failed` or `rejected` | Open{LastRun: Halted{Current}} | — |
| Open | SetOrder | order is a permutation of the set's repos | Open{Order} | — |
| Open | ConfirmRun | `Scope` valid; `Seen` covers exactly the planned members and each hash equals the member's current hash; `Preflight(...) ⊆ Ack`; plan non-empty | Confirming{Queue = plan, Current = first} | `ConfirmProposal(first, seen hash)` |
| Open | ConfirmRun | any guard above fails | no change | — |
| Confirming | StopRun | — | Open{Halted{Current, "stopped"}} (the member being executed finishes by itself) | — |
| Confirming | ConfirmRefused | `ID == Current` | Open{Halted{ID, reason}} | — |
| Open | Close | no member pending or confirmed | Closed | — |
| Shipped, Closed | MembersChanged | a new pending member appeared | Open | — |

The **plan** for a run is pure: members in scope with status `pending`, ordered by the repo's position in `Order` (repos missing from the order go last, by ID), then by kind rank `install`, `agent_file`, `delete_files`, `commit`, `push`, `merge_request` (an install or file change must land before the commit that includes it; a push before its MR). `merge_local` and `remove_worktree` (landing) are never in a run: they are confirmed one at a time.

`Preflight(state, scope, deps) []Warning` is pure and also exposed to the UI: it returns `dependent_before_dependency` when, in the plan, a member of repo R is ordered before a member of a repo D that R depends on (`Edge{R, D}`), or D has pending members of the same kind that are not in the plan and R's would run first. It never blocks; the user acknowledges by code, and the acknowledgement is audited.

**Any other (state, input) pair: no change, no commands.** This includes: `ConfirmRun` while Confirming, `SetOrder` while Confirming (the order is frozen during a run), `Close` with pending members, and `MembersChanged` for members that are not in the set.

### Review note (`StepNote`)

| From | Input | Guard | To | Commands |
|---|---|---|---|---|
| (none) | AddNote | parsed note valid; ≤ `MaxNotesPerTask` open+outdated | NoteOpen | `WriteAudit(note.add)` |
| NoteOpen | EditNote | text valid | NoteOpen | `WriteAudit(note.edit)` |
| NoteOpen, NoteOutdated | DeleteNote | — | (deleted) | `WriteAudit(note.delete)` |
| NoteOpen | SendNotes | batch non-empty, all selected notes Open, composed size ≤ `MaxMessageBytes` | NoteSending{Batch} | `WriteAudit(note.send)`, `SendMessage{Batch, text}` |
| NoteSending | SendAccepted | `Batch` matches | NoteSent{Turn} | `WriteAudit(note.sent)` |
| NoteSending | SendRefused | `Batch` matches | NoteOpen | `WriteAudit(note.send_refused)` |
| NoteSending | Recover | older than `SendTimeout` | NoteOpen | `WriteAudit(note.recovered)` |
| NoteOpen | DiffObserved | `Hashes[path]` ≠ note `DiffHash` (or the file left the diff) | NoteOutdated | `WriteAudit(note.outdated)` |

**Any other pair: no change.** `NoteSent` is final (delete is allowed only for Open/Outdated; a sent note is history). A note is never edited after sending; an outdated note can be deleted or re-added by the user at the new line.

Why `NoteSending` exists: `tasks.Message` is not idempotent. Marking the notes before the call means a crash can only lose a send (recovered to Open, user resends), never duplicate one silently. The message text lists the note IDs, so a human sees a double send.

## Invariants

| # | Invariant | Property test |
|---|---|---|
| C1 | A run's plan contains only `pending` members of repos in scope; it never contains a landing kind (`merge_local`, `remove_worktree`). | `TestPropPlanOnlyPendingInScope` |
| C2 | A run confirms a member only with the hash in `Seen`, and `Seen` equals the member's current hash at `ConfirmRun` (the user confirms what they saw); a differing hash emits no `ConfirmProposal`. | `TestPropConfirmOnlySeenHash` |
| C3 | At most one `ConfirmProposal` is outstanding: the next one is emitted only after `Current` reached a terminal status. | `TestPropOneOutstandingConfirm` |
| C4 | A run stops at the first failed, rejected or refused member; nothing after it is confirmed by that run. | `TestPropRunStopsAtFirstProblem` |
| C5 | The plan order follows `Order` (repo position), then kind rank; within a repo `commit` precedes `push` precedes `merge_request`. | `TestPropPlanOrder` |
| C6 | `Order` is always a permutation of the set's repos with no duplicates; `SetOrder` with anything else changes nothing; a repo appearing later in the set is appended. | `TestPropOrderPermutation` |
| C7 | `ConfirmRun` with an un-acknowledged warning changes nothing; `Ack` of a code that is not raised is ignored. | `TestPropWarningsNeedAck` |
| C8 | `Shipped` ⇒ every member terminal ∧ ≥1 executed ∧ none failed; `Closed` ⇒ every member rejected or the user closed it with none pending. | `TestPropShippedClosedMeaning` |
| C9 | `SetOrder` and `Close` never change while Confirming (the order is frozen during a run). | `TestPropFrozenWhileConfirming` |
| C10 | `RenderLinks` output contains only repo names, branch names and URLs that pass `SafeWebURL` and are on the remote's host; a URL from a non-executed member never appears; the marker is replaced exactly once. | `TestPropRenderLinksSafe`, `FuzzRenderLinks` |
| C11 | Every state change emits exactly one `WriteAudit` and one `Publish`; a no-change step emits no command. | `TestPropAuditPerChange` |
| N1 | A note is sent at most once: `NoteSent` and `NoteSending` accept no `SendNotes`. | `TestPropNoteSentOnce` |
| N2 | Composed text never exceeds `MaxMessageBytes`, contains no path outside the note's repo-relative anchors, and each note's text appears verbatim, escaped so it cannot close the wrapper. | `TestPropComposeBounded`, `FuzzComposeNotes` |
| N3 | `NoteSent` is final; `DeleteNote` on it changes nothing. | `TestPropSentIsFinal` |
| N4 | `DiffObserved` never changes a Sent or Sending note. | `TestPropOutdatedOnlyFromOpen` |
| N5 | Every `SendMessage` command is paired with exactly one transition into `NoteSending` for the same batch. | `TestPropSendCommandMatchesState` |

## Commands

| Command | Cardinality | Idempotent | Produces |
|---|---|---|---|
| `ConfirmProposal{ID, Hash}` | one per step | yes: proposals refuses a second confirm of a non-pending proposal | `MembersChanged` (via the proposals topic/reconcile), or `ConfirmRefused` |
| `SendMessage{Batch, Text}` | one | no (guarded by `NoteSending`, see above) | `SendAccepted` / `SendRefused` |
| `WriteAudit` | one | same transaction as the state write | — |
| `Publish{ChangeSetChanged}` / `Publish{NotesChanged}` | one | yes | — |

## Call graph

```go
// Production — members change
proposals service commit → hub topic "proposals" (also reconcile on start and every 30 s)
  → changesets.Service.onProposal(id)
      → db.LoadChangeSet(set of that proposal)     // members from proposals rows
      → Step(MembersChanged) → Commit(state, audit, expected version)
      → interpret: ConfirmProposal(next) → proposals.Service.Confirm(id, hash, "user:confirm-all")

// Production — confirm all
POST /api/changesets/{id}/confirm {scope, seen, ack}
  → parse (boundary) → deps = toolchains Orderer over the set's repos (files only)
  → Step(ConfirmRun) → Commit → interpret ConfirmProposal(first)
POST /api/changesets/{id}/preflight → Preflight (pure) → warnings

// Production — merge order
GET  /api/changesets/{id}            → state + suggested order (SuggestOrder over deps) + warnings
PUT  /api/changesets/{id}/order      → parse → Step(SetOrder)

// Production — executing a merge request (PX1, uses the pure part)
exec_mr: template = action.Description
  → db: executed merge_request results in the same set → Links
  → core/changesets.RenderLinks(template, order, branches, links) → text for push option

// Production — notes
POST /api/tasks/{id}/notes            → parse → Step(AddNote)
POST /api/tasks/{id}/notes/send {ids} → Step(SendNotes) → Commit(NoteSending) → interpret SendMessage
      → tasks.Service.Message(task, text, mode of last turn) → SendAccepted | SendRefused → Step → Commit
reconcile (30 s and on tasks topic) → git.DiffAgainstBase per-file hashes → Step(DiffObserved)
startup → notes in NoteSending → Step(Recover)

// Tests
core: run(state, inputs)                              // no shell
shell: Service with fake ProposalsAPI, TaskMessenger, DiffSource, Store, Clock, IDs
```

Cardinality: `MembersChanged` is a stream with at-least-once delivery; the recompute is idempotent because it is derived from rows. `ConfirmRun` is a one-shot user action.

## Failures

| Node | Failure | Class | Handling |
|---|---|---|---|
| Load set / members | db error | retry | next reconcile; nothing committed |
| Load set | rows inconsistent (member of a repo not in the set, status unknown) | die | `StateFromRows` rejects; logged; the set is shown read-only with "could not be read" |
| Read deps (Orderer) | manifest unreadable or cyclic | domain outcome | no edges for that repo → no warning for it; cycle → `Order` suggestion falls back to the task's repo order, UI says "dependency cycle between a and b" |
| `ConfirmProposal` | stale hash, not pending, repo untracked | domain outcome | `ConfirmRefused` → run Halted with a plain reason |
| `ConfirmProposal` | proposals service closing / db error | retry | one retry on the next reconcile; then Halted{"could not confirm"} |
| Member execution fails | executor failed (any `FailKind`) | domain outcome | `MembersChanged` shows `failed` → Halted; user retries the member in the proposals UI; the run does not resume by itself (the user starts a new run) |
| `SendMessage` | task is running or queue full | domain outcome | `SendRefused` → notes back to Open; message "The agent is busy. Send the notes when it finishes." |
| `SendMessage` | crash after the call | domain outcome | `Recover` returns notes to Open after `SendTimeout` (5 min) or on startup; possible double send is visible in the transcript |
| `RenderLinks` | marker missing | domain outcome | description sent unchanged (no links) |
| `RenderLinks` | URL fails `SafeWebURL` or other host | domain outcome | that entry rendered as repo and branch only |
| Commit | version conflict | retry | reload and re-evaluate |
| Panic in an interpreter step | | die | recovered per set/note, logged |

Errors are translated at each layer: callers get `core/changesets` and `core/review` types; the API maps to `apperr` classes.

## Requirements

| Capability | Live | Test | Contract |
|---|---|---|---|
| `ProposalsAPI` (`Confirm`, `List` members) | `proposals.Service` | fake with scripted statuses | `ProposalsAPIContract`: confirm with a wrong hash is refused; confirm of a non-pending is refused; a confirmed proposal reaches a terminal status; list returns only the set's members. Fake and real service (real with the fake git writer). |
| `ChangeSetStore` (load set + members, commit with expected version + audit, notes CRUD) | `internal/db` | in-memory | `ChangeSetStoreContract`: version conflict, audit in the same transaction, notes in `NoteSending` listed for recover. Fake and SQLite. |
| `DepSource` (`Edges(repos)`) | builds `Orderer` over the repos' manifests | map | clause: Maven dependency, cycle, non-Maven repo → none |
| `DiffSource` (`FileHashes(worktree)`) | `git.DiffAgainstBase` + hash | map | GIT1 clauses |
| `TaskMessenger` | `tasks.Service.Message` | fake | busy refusal (shared with verify) |
| `Clock`, `IDs` | system | fixed | — |

## Boundary

| Source | Parser | Produces | Rejects |
|---|---|---|---|
| `PUT order` body | `ParseOrder` | `[]RepoID` | unknown fields, trailing data, duplicates, repos not in the set (checked against the set), empty, more than `MaxRepos` |
| `POST confirm` body | `ParseConfirmRun` | `Scope, Seen, Ack` | unknown scope, `Seen` hash not 64 hex, `Seen` ids not in the set, unknown warning code, trailing data |
| `POST notes` body | `ParseNote` | `Note` | path absolute, with `..`, a drive, a stream (`:`), a device name or `\\?\` (reuse `platform`/`HasShortNameSegment` rules); line < 1 or > `MaxLine`; text empty, over `MaxNoteRunes`, control characters other than newline/tab; side unknown |
| `proposals` + `change_sets` rows | `StateFromRows` | `State` | member repo outside the set, status/kind unknown, `merge_order` JSON not a permutation |
| MR description template | existing action validation + `ValidateLinkMarker` | `Description` | more than one marker, marker not on its own line, description over the cap |
| Push output | existing `ParsePushOutput` | `MRURL` | unsafe or foreign-host URL |

Each parser gets: round trip where a formatter exists, accepted ⇒ valid, one test per rejection rule, and a fuzz target.

## Files

Core: `internal/core/changesets/{types,state,input,command,step,plan,warnings,order,links}.go` and tests (C1–C11, `FuzzRenderLinks`, order parser tests); `internal/core/review/{types,state,input,command,step,compose,parse}.go` and tests (N1–N5, `FuzzComposeNotes`, `FuzzParseNote`).
Shell: `internal/changesets/{service,reconcile,interpret,deps,parse,store}.go`, tests, `changesetstest/` (`ProposalsAPIContract`, `ChangeSetStoreContract`); `internal/db/store_changesets.go`, queries `change_sets_members.sql`, `review_notes.sql`; `cmd/ygg/changesets.go`; DTOs and handlers in `internal/api` (AP1).
Executor side (PX1): `internal/proposals/exec_mr.go` calls `RenderLinks`; `ValidateLinkMarker` is used by the MR action validation.

Ownership correction to phase-4.md: CSS1 no longer creates change sets (already done by `proposals.Create`); it owns grouping state, confirm runs, order, warnings, notes. `core/review` (notes) moves from V1 to CS1's scope together with `core/changesets`; `core/landing` has its own design and task (CS2).

## Open questions

1. **Warnings are advisory, not a gate.** The plain `/api/proposals/{id}/confirm` route stays and does not check the order. The warning is UX (RV-04 says "UI warns"). Recommend yes. A gate would need the proposals service to know about sets.
2. **Kind rank for a run** is install → agent_file → delete_files → commit → push → merge_request. Confirm the order of the first three is acceptable (they are rare in a run).
3. **A run halts on the first problem and does not resume by itself.** The user fixes or retries the member and starts a new run. Recommend yes (simplest, safest).
4. **Notes are per task and repo, not per turn;** sent notes stay as history. Outdated detection compares the per-file diff hash, so a note on a file the agent has changed since is flagged, not deleted.
5. **Limitation to accept:** MR links point backwards only (later → earlier) plus the change-set page. Two-way links need a write-scoped host token.
