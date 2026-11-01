# Phase 4 — Complete the core loop → `v0.5.0`

> Status: **plan drafted 2026-11-01, waiting for approval. No agent spawned, no code written.**
> Source: docs/PLAN.md Phase 4; docs/PRD.md BL-08, BL-09, WE-05, VF-01–VF-03, RV-01, RV-03–RV-06, PL-03 (RV-02, BL-10 reused); decisions D8, D16; docs/AGENTS.md 5.2, 5.3, 5.7, 5.11, 6 (invariants 1, 2, 3, 8); docs/spikes.md B4/B5 (hook and repo-config facts only); docs/phases/phase-3.md (carry-ins).

**Exit criterion (PLAN.md):** a multi-repo task on the demo project runs end to end with automatic verification and a change set shipped as linked MRs against a test host.

---

## Starting state

- `main` clean at `be76e60`. Tags: `v0.1.0`, `v0.2.0`, `v0.4.0` (no `v0.3.0`; not my call).
- Already on `main`, reused as is:
  - `internal/builds`: queue, logs, presets, artifacts; `Spec.Target` already has `InWorktree{WorktreeID}` and `ParseRequest` accepts it (BL-09 is mostly there; needs verifying end to end, not building). Toolchains: node, maven, gradle, custom. `Commands` already has `Install`, `Verify`, `FullTests`; Node/Maven/Gradle/custom already fill `Verify`/`FullTests`.
  - `core.ChangeSet` (`MergeOrder []RepoID`), `change_sets` table, `proposals.change_set_id`; `core/proposals` has `TaskChangeSet`; `internal/proposals` executors for commit, push, MR (push options), install, agent_file, delete_files; `Orderer` (Maven) and `corebuilds.OrderModules`.
  - `internal/tasks`: `Idle{ReadyForReview}` outcome, board column Review, turns, `tasks.Service.Message`.
  - `internal/worktrees` (create only; "removes nothing" by design), `internal/mainlock`, `internal/git` (commit/push/status/diff/snapshot).
- **Gaps found while reading** (these shape the plan):
  1. Nothing runs verification. No `verifications` table, no service, no UI.
  2. `builds.Step` has no env, so Node's `CI=true` (`node.Env()`) is not applied by anything.
  3. `toolchain.go` says `Install` "is a proposal, never automatic". WE-05 needs an automatic bootstrap in a worktree. See decision 2.
  4. No per-repo store for "verify override" and "full tests opt-in" (BL-08). Only `custom` has its own config.
  5. `proposals.kind` has a SQL `CHECK` list (migration 00001). New kinds need a table rebuild in the migration.
  6. `internal/git` has no merge, no worktree removal, no diff against the worktree base commit, no line-ending (EOL) detection.
  7. No Review tab. `TaskPage` shows the transcript and proposals only.
  8. No "test host": MR creation (GH-03) is push options, and nothing can receive them in CI.
  9. PLAN Phase 3 still has the **OpenCode adapter unchecked** (waits for a real-CLI spike you postponed).

## Decisions I need from you (each has a recommendation)

1. **Worktree removal vs safety invariant 1 ("never delete files on disk").** RV-06 needs a way to remove a worktree, which deletes a folder. *Recommend:* allow exactly one deletion path: `git worktree remove` **without `--force`**, only for worktrees Yggdrasil created (under the worktree root, checked with `ResolveRoot`), only through a confirmed `remove_worktree` proposal, only when the tree is clean (nothing uncommitted) and the branch is merged or the user chose "discard branch work" for a clean tree. Never the branch (branch deletion stays banned), never a main checkout. A dirty worktree cannot be removed in Yggdrasil at v0.5 (you delete the folder yourself); this avoids `--force`. I add one line to AGENTS.md 6.1 and a test that fails if `--force` or a path outside the root is ever passed. **This weakens the wording of an invariant, so I need your yes.** Alternative: never delete, only `git worktree move` into a `_removed/` folder you clean up by hand (RV-06 becomes "retire worktree").
2. **Automatic toolchain bootstrap (WE-05) runs `npm ci`, `mvnw dependency:go-offline`, etc. unprompted.** That executes dependency lifecycle scripts from lockfiles the agent may have edited. *Recommend:* automatic, because it is the task's own worktree (not a main checkout, so invariant 2 does not apply), always through `platform.Start` with the clean env (no tokens), cwd = the worktree, and never for a repo that is not trusted (SF-09). Per-project switch "run verification automatically" default on, and a "bootstrap needs approval" option for people who want a proposal first. Plain `npm ci` (lifecycle scripts run); the threat model states that verification runs repo code, like any build. Say if you want `--ignore-scripts` as the default instead (some packages then fail to build).
3. **Local `--no-ff` merge (RV-05) is a mutation of a main checkout.** *Recommend:* a new proposal kind `merge_local` (confirm with the hash of the branch head, clean main checkout required, `mainlock` held, conflicts predicted with `git merge-tree --write-tree` first (git ≥ 2.38; yours is 2.44, `ygg doctor` gets a check) and `git merge --abort` only as the fallback if the real merge still conflicts). `merge --abort` is added to the argv allowlist and banned-word scan explicitly. Never pushed. The proposals SQL `CHECK` is rebuilt in migration 00005.
4. **Linked MRs (RV-03).** Each MR should name its siblings, but sibling URLs do not exist until all MRs are created, and GH-03 creates MRs via push options only (no API write). *Recommend:* two passes. Pass 1 creates MRs in merge order; each description lists siblings by repo and branch, plus URLs of those already created (the URL is read from the push output). Pass 2 re-pushes the earlier branches with `merge_request.description` to back-fill. **I do not know whether GitLab applies description push options to an already existing MR.** I will not claim it. Plan: design the pass 2 as best-effort, the change-set view in Yggdrasil always shows the full linked set, and you verify pass 2 on your real GitLab in the manual list. If it does not work, MRs link one way only (later → earlier) and the change-set page is the index.
5. **Where verification runs.** *Recommend:* through the existing builds service with `InWorktree` targets, so logs, history and the Builds page come for free. Cost: builds are one at a time (BL-03), so N repos verify serially and a long user build delays verification (known head-of-line limitation, phase-3 backlog). Alternative: a separate verify lane. I would only add it if you find the delay painful.
6. **When verification triggers.** *Recommend:* after every turn that ends `ReadyForReview` and changed a repo (non-empty diff against the worktree base commit), not only the first; a "Verify again" button; a task with no change verifies nothing and says so. Triggered by a **reconcile loop on stored state** (task idle in review with no verification row for that turn), not by an in-memory event, so a restart never loses it.
7. **Line endings (PL-03).** *Recommend:* detect and show, do not rewrite. Per file, flag "line endings changed" when `git diff --numstat` differs from the same diff with `--ignore-cr-at-eol`, hide EOL-only hunks by default in Review, warn on commit proposals. No config changes to the user's repos, no automatic normalisation.
8. **OpenCode adapter (Phase 3 leftover).** *Recommend:* stays out of Phase 4 (needs the real-CLI spike you postponed). It does not block the exit criterion. Tell me if you want the spike first.
9. **Parallel writers.** The Phase 3 log says several writing agents made GitKraken dismiss permission prompts and lost writes, and it was unresolved. The batches below are sized for 3, but *recommend* I run **one writing agent at a time** unless you tell me that problem is gone. It costs wall time, not tokens.

## Functional-effect designs (written and approved before the code that needs them)

Run with `/functional-effect:design <feature>`; each shown to you before foundations.

| Design | Covers | Why |
|---|---|---|
| `docs/design/verify.md` | VF-01–VF-03, WE-05, BL-08, BL-09: per (task, turn, repo) state `Pending → Bootstrapping → Verifying → Passed/Failed/Unverifiable/Skipped`, bootstrap failure vs verify failure, steps chain, cancel, supersede by a newer turn, retry, "send failure to agent", restart reconcile | states, retries, queue, external processes |
| `docs/design/changesets.md` | RV-03, RV-04, RV-01 notes: change set created with the first proposal of a task, status derived from member proposals, confirm all vs per repo, merge order (user + dependency analysis input), warning rule "dependent before dependency", linked-MR passes, review notes anchoring and staleness | grouping, ordering, partial failure |
| `docs/design/landing.md` | RV-05, RV-06, PL-03: local merge and worktree removal as proposals: preconditions, conflict prediction, abort, lock handling and retry classification, branch/clean checks, crash mid-merge | non-idempotent effects on a main checkout, Windows locks |

No design needed (stated in the brief): toolchain bootstrap steps and chain fill-in (data), EOL detection (pure parser, boundary tests), platform lock classification, frontend tasks, test host fixture.

## Foundations (done first, committed to `main` before any fan-out)

One `go-backend` agent, serial, after the three designs are approved (I review and merge):

- `internal/core/verify`, `internal/core/review`, `internal/core/changesets`, `internal/core/landing`: state, input, command types and `Step` signatures from the designs; `Validate`; no logic beyond that. Core packages cannot import each other, so facts one needs from another (repo dependency edges, diff hash, pending proposals) are fields of its inputs, as in `core/tasks.Admission`.
- `internal/builds/toolchain.go`: `Commands.Bootstrap []Step` (separate from `Install`, which stays a proposal) and `Step.Env []string` (extra env on top of the clean one; `CI=true`). Interface change, so it is here, not in a feature task.
- Migration `00005`: `verifications` + `verification_steps` (task, turn, repo, worktree, status, build id, summary, started/finished), `review_notes`, `repo_build_settings` (verify override, full-tests opt-in, bootstrap on/off), `change_sets` columns (status variants, `linked_at`), `worktrees` removal columns (`removal_state`, `removed_at`), `proposals.kind` `CHECK` rebuilt with `merge_local` and `remove_worktree`. Down migration included.
- `core.ProposalKind` + `core/proposals` action variants for `merge_local` and `remove_worktree` (types and validation only; executors come later).
- `internal/api`: DTO files and 501 stub handlers for verification, review notes, change sets, landing; hub topics `verifications` and `changesets`; service interfaces in `Deps`.
- `task generate` so the frontend gets every DTO at once.

Gate: `task generate`, `lint`, `typecheck`, `test`, `test:web` green on `main`.

## Batches

Every Go brief starts with `git merge --ff-only main` (worktrees branch from the default branch) and says "follow the functional-effect:model skill (docs/AGENTS.md 5.11)" plus the design doc path. No two parallel tasks own the same file or package. After each batch merges: `safety-reviewer` + `functional-effect:fe-reviewer` on `<batch-start>..main`; fix Blocking and Must-fix, re-run.

### Batch 1: pure cores and git operations

| ID | Task | Agent | Owns | PRD |
|---|---|---|---|---|
| V1 | `core/verify` and `core/review`: verification `Step`, chain stop-at-first-failure, supersede/cancel/retry rules, "send failure" message composer (log tail capped), notes anchoring/staleness and the notes-to-prompt composer (caps, no tokens/paths outside the worktree); property tests per invariant, parser tests for notes | go-backend | `internal/core/verify/**`, `internal/core/review/**` | VF-01–VF-03, RV-01, BL-08 |
| CS1 | `core/changesets` and `core/landing`: change-set status from members, confirm-all / per-repo rule, merge-order validation (acyclic, covers exactly the repos), dependency-warning decision, link-pass plan; landing preconditions for merge and removal (clean, merged, under root, lock held), conflict-abort decision, lock-error retry policy; property tests | go-backend | `internal/core/changesets/**`, `internal/core/landing/**` | RV-03–RV-06, PL-03 |
| GIT1 | `internal/git`: `MergeNoFF` (predict with `merge-tree --write-tree`, then merge, abort fallback), `MergeAbort`, `WorktreeRemove` (no force, ever), `DiffAgainstBase` (worktree vs `base_commit`, committed + uncommitted + untracked), EOL detection (`EOLFlipped` per file); argv allowlist and invariants scan updated narrowly (`merge --abort`, `worktree remove` without force); `gittest` fake and `GitWriterContract` clauses for each; real-git tests with CRLF, spaces in paths, a conflict | go-backend | `internal/git/**` | RV-05, RV-06, PL-03, RV-01 |

Must not touch: each other's packages, `internal/api`, `internal/proposals`, `internal/builds`.
Invariants (full list in the designs): at most one live verification per (task, repo); a newer turn supersedes an older one and a stale result changes nothing; a chain never runs a step after a failure; terminal results never change except by an explicit retry; a merge order always covers the set's repos exactly once; confirm-all never confirms a proposal whose repo is not in the set; a worktree outside the root or with uncommitted changes is never removable; an unmerged branch is never described as merged; usage of `--force`/`reset --hard`/`branch -D` never appears in any argv.

### Batch 2: toolchains, platform locks, test host

| ID | Task | Agent | Owns | PRD |
|---|---|---|---|---|
| T1 | Toolchain fill-in: `Bootstrap` for Node (npm/pnpm/yarn by lockfile), Maven, Gradle; Gradle Kotlin compile tasks; apply `Env` (`CI=true`) through `Step.Env`; full-tests opt-in selection; per-repo override reader on top of `repo_build_settings` (service in `internal/builds`, store in `internal/db`); `docs/writing-a-toolchain.md` updated | go-backend | `internal/builds/toolchains/**`, `internal/builds/{settings,toolchain}*.go`, `internal/db/queries/repo_build_settings.sql` | BL-08, BL-09, WE-05, D8 |
| P1 | Windows file-lock handling: classify sharing violation / access denied / path-too-long from a removal failure into a typed error, name the holding process via Restart Manager (`RmGetList`) when available, never kill it; Unix stub; contract test with a held handle | platform-windows | `internal/platform/**` (new `lockinfo*.go`) | RV-06, PL-03 |
| TH1 | **Test host** and fixtures: local bare remote with `receive.advertisePushOptions` and a `post-receive` hook that records `GIT_PUSH_OPTION_*` and prints a fake `View merge request` URL, plus a tiny GitLab-shaped API stub for MR/pipeline reads; multi-repo demo scenario in `fixtures/demo-gen` (a Maven repo depending on another, a Node repo, a CRLF file) | test-engineer | `internal/hosts/hoststest/**`, `fixtures/demo-gen/**` | exit criterion, RV-03, RV-04 |

Must not touch: `internal/git`, `internal/core/**`, `internal/proposals`.

### Gate 1 (V1, CS1, GIT1, T1, P1, TH1)

Both reviewers on `<phase-start>..main`. The safety reviewer gets the invariant 1 and 2 cases written out (decisions 1 and 3). Then I stop and show you the status.

### Batch 3: verify shell, change-set shell, Review UI

| ID | Task | Agent | Owns | PRD |
|---|---|---|---|---|
| VS1 | `internal/verify` shell: parse inputs at the boundary, load state, `Step`, persist state + audit in one `Commit`, interpret commands (bootstrap and verify steps as `InWorktree` builds via the builds service, reconcile loop on stored state, wake on turn end through the hub, "send failure to agent" through `tasks.Service.Message` with the log tail, redaction before storing summaries); `cmd/ygg/verify.go`; startup and shutdown wiring | go-backend | `internal/verify/**`, `internal/db/queries/verifications.sql`, `internal/db/store_verify.go`, `cmd/ygg/verify.go` | VF-01–VF-03, WE-05, BL-09 |
| CSS1 | `internal/changesets` shell: create/attach on first proposal of a task (hooks into proposals through an interface it defines), status from members, merge order store (user edit + Maven `Orderer` suggestion), dependency warnings, confirm-all fan-out through the proposals service one proposal at a time with partial-failure result, linked-MR description builder, review-notes store and "send notes to agent"; `cmd/ygg/changesets.go` | go-backend | `internal/changesets/**`, `internal/db/queries/change_sets*.sql` (extend), `internal/db/store_changesets.go`, `cmd/ygg/changesets.go` | RV-01, RV-03, RV-04 |
| FE1 | Review tab on the task page: per-repo verification badges first, then worktree diffs (EOL-only hunks hidden, "line endings changed" chip), inline notes with staleness, "Send notes to agent", "Send failure to agent" with log tail, "Verify again"; transcript untouched | frontend | `web/src/features/review/**`, `web/src/routes/tasks/**` | VF-02, VF-03, RV-01, PL-03 |

`internal/proposals` and `internal/api` are not touched in this batch (the interfaces CSS1 and VS1 need are defined in their own packages and implemented in batch 4). If one needs an edit elsewhere, it stops and reports.

### Gate 2 (VS1, CSS1, FE1)

### Batch 4: executors, API wiring, change-set UI

| ID | Task | Agent | Owns | PRD |
|---|---|---|---|---|
| PX1 | Proposals shell: `merge_local` and `remove_worktree` executors (precondition re-check at execute time, `mainlock`, reconcile after crash mid-merge, lock errors → typed retryable failure with holder name), change-set aware `Create`, linked-MR pass 1 and best-effort pass 2 (result recorded, never fatal), refuses untracked repos; break-once checks | go-backend | `internal/proposals/**` (not `exec_commit/push` unless needed), `internal/api/proposals.go` | RV-02, RV-03, RV-05, RV-06, PL-03, invariants 1, 2, 8 |
| AP1 | API layer: handlers for verification list/retry/send-failure, review notes CRUD/send, change sets (get, order, confirm-all, per-repo), landing (merge, remove) as proposal creation only, board card verification summary and task view fields, hub topics, Host/Origin/session rules, `ygg doctor` git-version check | go-backend | `internal/api/{verify,changesets,review,landing}*.go`, `internal/api/{board,tasks_service}.go`, `cmd/ygg/doctor*.go` | VF-02, RV-03, RV-04 |
| FE2 | Change-set UI: grouped proposals per task, confirm all / per repo, merge-order editor with dependency warnings, linked-MR list, local-merge and remove-worktree dialogs (clean/merged status, lock message with holder and Retry), verification badges on the board card and task card | frontend | `web/src/features/changesets/**`, `web/src/features/proposals/**`, `web/src/features/board/**` | RV-03–RV-06, VF-02 |

PX1 owns `internal/proposals` and its one handler file; AP1 owns the rest of `internal/api` for the area; they meet at service interfaces defined in foundations.

### Gate 3 (PX1, AP1, FE2)

### Batch 5: test maps, end to end, invariant updates

| ID | Task | Agent | Owns | PRD |
|---|---|---|---|---|
| T2 | Test maps from the three designs: invariants → property tests, boundary parsers (notes, EOL, merge-tree output, push-output URL) → round-trip / accepted ⇒ valid / one test per rejection rule / fuzz, capabilities with fakes (verify runner, git writer merge/remove, test host) → contract suites; "show each test can fail" evidence; safety matrix updates for invariants 1, 2, 3, 8 (worktree removal path, `merge_local` needs a confirmed proposal, bootstrap env has no tokens, every new state change audited) | test-engineer | test files only, `internal/*/..._test.go`, `internal/*/*test/**` | all |
| E2E | Multi-repo end to end on the demo project with the mock adapter and the test host: task edits two repos, verification passes in one and fails in the other, "send failure" starts a round, second round passes, change set confirmed, two MRs created in merge order and linked, local merge, worktree removal; Windows variants (locked file during removal, paths with spaces, CRLF) | test-engineer | `cmd/ygg/e2e*_test.go`, `scripts/smoke*.ps1` additions | exit criterion |

T2 and E2E run one after the other (both touch test helpers).

### Gate 4 and phase close

- Final review of `<phase-start>..main` by both reviewers; `/functional-effect:review` over `internal/core/{verify,review,changesets,landing}` and the new shells.
- Bookkeeping: tick PLAN, CHANGELOG, `docs/threat-model.md` (verification and bootstrap run repo code; the one deletion path; `merge_local`; test host is test-only), AGENTS.md 6.1 wording if decision 1 is accepted, PRD if a requirement changes, deviations recorded here.
- Phase close report as CLAUDE.md step 7. I do not tag or push.

## Exit criteria → tasks

| Exit criterion (PLAN.md) | Contributing tasks | How verified |
|---|---|---|
| Fast verify chains with per-repo override and full-test opt-in | T1, VS1 | T1 table tests per toolchain; T2 contract; manual with real toolchains |
| Builds inside worktrees | T1, VS1 | E2E; existing `InWorktree` build tests |
| Toolchain bootstrap for new worktrees | T1, VS1 | E2E (fake runner); manual `npm ci` / Maven / Gradle |
| Automatic verification, pass/fail on card and in Review, "Send failure to agent" | V1, VS1, AP1, FE1, FE2 | E2E; FE tests |
| Review tab with diffs and inline notes back to the agent | V1, CS1, GIT1, CSS1, FE1 | E2E; FE tests |
| Change sets: grouped proposals, confirm all or per repo, linked MRs | CS1, CSS1, PX1, AP1, FE2, TH1 | E2E against the test host |
| Merge order with dependency warnings | CS1, CSS1, FE2 | property + E2E (Maven dependency) |
| Local `--no-ff` merge, aborted on conflict | CS1, GIT1, PX1 | real-git conflict test; manual |
| Safe worktree removal with Windows file-lock handling | CS1, GIT1, P1, PX1, FE2 | locked-file test on Windows CI; manual |
| CRLF handling in agent diffs | GIT1, FE1 | CRLF fixtures; manual |
| **Multi-repo task end to end, linked MRs against a test host** | all | E2E on the test host |

## What only you can verify on Windows (manual checklist, run at phase close)

Real agent CLIs run here only with your agreement in that session.

1. Multi-repo task on the demo project (real Claude Code, Haiku): both worktrees verify automatically, one failure shows its log, "Send failure to agent" starts a new round.
2. Real bootstrap: `npm ci`, Maven and Gradle wrappers in a fresh worktree (first run, antivirus on, long `node_modules` paths under the data dir).
3. Locked file during removal: keep a terminal cwd or an open file inside a worktree, ask to remove it; the message names the holder, Retry works after you close it, nothing is killed.
4. Local merge: a clean merge lands as `--no-ff` in the main checkout with nothing pushed; a conflicting one is refused without touching the tree; a dirty main checkout is refused.
5. Change set against a real GitLab (needs your tokenless push access): MRs created in merge order, **whether pass 2 back-fills the description on an existing MR (decision 4)**, the linked list in Yggdrasil's change-set page.
6. CRLF repo: an agent edit that flips line endings shows the chip, EOL-only hunks are hidden, a commit proposal warns.
7. Merge-order warning: reorder so a dependent repo comes first; the warning shows and the audit log records the override.
8. Restart `ygg` mid-verification: the run is marked interrupted, then re-verified on startup without a duplicate.
9. Kill `ygg` from Task Manager during `npm ci`: no node/java/git process left.
10. Verification of 3 repos in a row while a user build is queued: ordering and status make sense.

## Risks

- **Size.** About 15 tasks plus foundations; VS1, PX1 and GIT1 are the largest. If a usage limit hits I write each in-flight task's state under Progress.
- **Decision 4 is unverified** (GitLab push options on an existing MR). The design has a fallback, but the "linked MRs" wording of the exit criterion rests on it for the real host.
- **Single builds queue** delays verification when a long user build is running (decision 5).
- **Windows file locks** are only partly detectable (Restart Manager can miss handles); the message must stay honest ("something may be using it").
- **Shared contracts** (SQL `CHECK` rebuild, new proposal kinds) sit in foundations so no parallel task edits them.
- **vitest worker flake** under parallel agents (known; `maxWorkers: 2`). Reruns pass.

## Progress

Plan drafted 2026-11-01. **Approved 2026-11-01: decisions 1–9 accepted as recommended, batch layout accepted.** Next: designs `verify`, `changesets`, `landing` (one at a time, each shown for approval), then foundations.
- `verify` design approved 2026-11-01.
- `changesets` design written. **Deviation from decision 4:** the pass-2 back-fill cannot work (re-pushing an unchanged ref sends no push options; a changed one needs a new commit or a force). Replaced by a confirmed description template with a link marker that the MR executor fills with URLs of earlier MRs in the set; links point backwards only. `changesets` approved 2026-11-01. `landing` design written: **changes decision 1's mechanics** (rename into a staging folder, then delete only that folder with a new `platform.RemoveStaged`, then prune; same scope and guards), pending approval; P1 and GIT1 gain the matching helpers. Also: change sets are already created lazily by `proposals.Create`, so CSS1 does not create them; `core/review` moves from V1 to CS1.

### Progress log

- **Foundations merged** (6 commits): core contracts for verify/review/changesets/landing, proposal kinds `merge_local`/`remove_worktree`, `Step.Env` + `Commands.Bootstrap` + `builds.Service.EnqueueWithID`, migration 00005 (a Go migration embedding `internal/db/sqlcschema/00005_*.sql`; down path deletes landing proposals and turns `removing` worktrees back into `merged`), API DTOs and 501 stubs, topics. `InWorktree` builds take no mainlock (verify.md Q6 closed). `StagingPath` returns `StagePath{Root, Leaf}` (the shell joins). `merge_local`/`remove_worktree` pass `core/proposals` `supported()`: the MCP tool list must never offer them.
- **V1 + V1b merged** (`core/verify`): turn order is by `TurnSeq` (from `task_turns.seq`), not by ID (IDs are random hex). `Cancelled` and `Unverifiable` carry `Attempt`. Shell carry-ins: map `TurnSeq` and attempt into the `verifications` row; shell issues two different build IDs per `TurnEnded`.
- **Split:** CS1 became CS1a (`core/changesets` + `core/review`, merged) and CS1b (`core/landing`).
- **CS1a merged.** Shell carry-ins for CSS1: persist `Confirming{Scope, Queue(ID+hash), Current}` (or store a restarted run as halted); build `Links` only from executed `merge_request` members; `ComposeNotes(batch, notes)` before `StepBatch` (use `StepBatch` for several notes: one message); call `Recover{Startup:true}` at startup and a timed `Recover` in reconcile; skip `DiffObserved` when the diff read failed (an empty map marks every Open note Outdated); `exec_mr` calls `ValidateLinkMarker` in action validation and `RenderLinks` with the remote host. `Plan` also excludes `publish` and `subtasks` from runs (confirmed one at a time). Closed is sticky until a pending member appears.
- Next: CS1b (`core/landing`), GIT1 (`internal/git`), then batch 2.
- **CS1b merged** (`core/landing`; 43 mutations killed, 1 equivalent). Shell (PX1) must supply: `RemoveStart.StageID` 1-64 of `[A-Za-z0-9_-]`; `Observed.MergeHead` whenever a merge is in progress; `Clean` counts untracked as dirty; fresh `Observed` after merge/abort (settling); `RemoveObserved.Staged` = real `StagePath{Root, Leaf}` under `<root>/.removing`; execute `MoveToStaging.To` / `DeleteStaged.Staged` as `filepath.Join(Root, StagingDir, Leaf)` and re-check in `RemoveStaged`; `OtherPrunable` = any prunable entry other than ours. Choices accepted: recovery needs `MergeHead == BranchHead == ExpectBranchHead`; `AlreadyMerged` also marks the worktree merged; a `removing` row with nothing on disk ends `Removed`; all core `MergeFailed` are retryable.
- Next: GIT1 (`internal/git`), then batch 2 (T1, P1, TH1), then gate 1.
