# Phase 6 — execution log

Moved out of `phase-6.md` so the lead re-reads a short plan and status table on resume. Append new entries at the bottom. Plan, ownership and exit criteria stay in `phase-6.md`.

- Plan prepared against `2f96629`; final foundation source SHA: `8b0da11`; verified main snapshot: `3341505`.
- Design approvals, batch start/merge SHAs, review results, test maps, decisions/deviations and any in-flight agent state: record here during execution.

- Plan validation: `git diff --check` passed. Required `task generate`, `task lint`, `task typecheck`, `task test`, and `task test:web` could not run because `task` is not installed in this environment. No implementation behavior is claimed verified.

- Execution started after human approval of this plan. F0 produced seven concrete
  designs in `docs/design/phase-6-*.md`. No subagent was launched, no production
  code/schema was changed, and no shared contract was committed before approval.
- F0 proposed decisions: noise threshold 100 total/50 sampled/half-sample generated
  directory; server-side in-process HTTP Git transport; reserved repo readiness
  filtered in SQL across every consumer; setup draft owns init proposal; durable
  RequestID/digest and stage reconciliation; per-tab live notification dedup with
  gap recovery in-app only. See designs for limits, invariants and tests.
- Environment still lacks `task` and `pnpm` on PATH. `go`, Node and golangci-lint
  are present. Provider capabilities include Claude Sonnet 5.5 for future named
  implementation roles; no provider fallback or model invocation occurred.
- F0 design approval gate remains pending. Per the approved plan and the design
  skill's explicit “Show and stop” instruction, do not implement F1–F3 or dispatch
  Batch 1 until the human approves these concrete designs (or explicitly waives
  that separate gate). This is not a request to reapprove the work breakdown.

- Human approved all F0 designs and instructed execution to proceed. The separate
  F0 gate is satisfied; it must not be asked again.
- Execution adjustment: use one isolated go-backend/Sonnet foundation agent for
  F1–F3, with the lead integrating and verifying its single branch on main. This
  uses the original request and CLAUDE.md step 2's explicit single-agent option;
  it replaces the table's lead-only implementation assignment. No fan-out until
  every shared contract, migration, core skeleton, Taskfile and generated output
  is committed on main and the five mandatory checks pass. No parallel feature
  implementers will run during foundations.

- F1–F3 foundation branch `t3/phase-6-shared-foundations` merged with `--no-ff`
  at `09bc226` (source commits dc5db87, 1127cad, 842b569, 897c877, 67ac4e8).
  No feature batch dispatched. Migration 00007 uses the existing Go rebuildTx
  plus SQL schema mirror because the proposal-kind CHECK requires FK-safe rebuild.
  Publish action is `PublishRepo` to avoid the existing Publish hub command.
- Foundation safety and functional reviews both found Must-fix issues: stale
  same-worker outbox claims lacked version fencing; receipts/stage prefixes and
  reservation scope were not fully validated; Playwright could reuse a real server.
  Lead repairs are in progress, with tests mapped in
  [phase-6-foundation-test-map.md](phase-6-foundation-test-map.md). New review
  invocations are required after the regression tests land.
- Tools now available: Task 3.54.0, Node 22 and pnpm (project pnpm 11.4.0
  resolved by wrapper). First concurrent full checks exhausted disk; do not count
  those runs as validation. Low-concurrency generation now passes; repaired
  core/newrepos, archtest and DB tests pass. Full Go test reproduced inherited
  path/shim/author fixture failures. Required checks remain an open foundation
  gate. No PLAN feature checkbox or phase-exit criterion is marked complete.
- Disk cleanup permission requested only for the foundation agent's accidental
  902 MiB scratch home, per CLAUDE.md's explicit no-file-deletion instruction;
  no cleanup has been performed. Avoid HOME overrides and parallel heavy checks.

- T3 server restart interrupted the foundation regression assignment and running
  shell checks. The resumed Sonnet call failed with an API rate limit. Its seven
  partial fixture edits were reviewed, committed as 5f13a00 on
  t3/foundation-regression-tests and merged on main as da626b7; no regression
  coverage completion is claimed. Main remains before Batch 1.
- Human explicitly authorized deleting the accidental scratch home. The lead
  removed only that exact directory (making its read-only module-cache directories
  owner-writable within the authorized target so deletion could finish). About
  900 MiB is now free. No other cache, worktree, file or branch cleanup authorized.
- Restart validation with sequential low-concurrency checks. Generation had passed
  before restart; previous full test/lint runs that exhausted disk are failures,
  not evidence of a clean foundation gate. Retry the same Sonnet test-engineer
  assignment for the remaining mapped regressions; do not change provider/model
  without human direction if its quota remains unavailable.

- Same-provider Sonnet retry also failed before any edits with the 5-hour API
  quota limit. The human explicitly instructed: "Use Codex agents to continue."
  This replaces Sonnet as the session preference for remaining implementation
  and test assignments; retain each named role's ownership and report contract.
  Do not retry Sonnet or ask again about this provider choice.
- Recovered main checks: task generate, task lint and task typecheck now pass.
  task test and task test:web are running sequentially with GOMAXPROCS=2,
  GOFLAGS=-p=1 and Go scratch in an executable task directory under /dev/shm.
  The pinned linter's Go1.27 formatter differs from Go1.26 gofmt for multiline
  returns; format changed files with golangci-lint fmt to match the required gate.

- Recovered main baseline 77f4793/f98a8c2: all five required checks passed
  sequentially (task generate, task lint, task typecheck, task test, task test:web),
  with exit codes recorded in /tmp/ygg-phase6-check-results.json. No disk-full
  or quota-failed run is counted as a pass. Foundation regression coverage is
  still in progress under the approved Codex assignment; dual review rechecks
  and post-merge checks remain required before Batch 1.

- Foundation safety recheck (Codex, through 3650531): PASS with no Blocking or
  Must-fix findings. Prior claim-version, receipt/evidence/stage/scope and
  browser-sandbox defects are verified fixed in code. Regression tests and the
  functional recheck remain pending, so this is not a foundation gate closure.
- Safety follow-ups before effects are enabled: persist/restore full immutable
  action input (including Agent/Model) or use an explicit validated replay
  projection before Batch A; reject state/resume detail fields belonging to a
  different variant (even zero-valued fields) before Batch N recovery workers.
  These are lead-owned shared-contract repairs, not permission for downstream
  agents to change frozen DB contracts. Apply after the current DB test ownership
  handoff, with regression coverage and required checks.

- Functional foundation recheck: FAIL. Additional Must-fix findings are an
  Uncertain state accepted for persistence but rejected on decode; EffectRecord
  insert/settle/load bypassing receipt validation; and receipt evidence not
  agreeing with immutable file contents or the preceding remote URL. The test
  map was extended and current test-engineer steered to capture these as failing
  regressions and hand off ownership before lead production edits. Safety PASS
  does not override this functional gate failure. Batch 1 remains unstarted.

- Codex foundation test branch t3/phase6-foundation-tests (f52de6c) was reviewed
  and merged at 4d5811b. It adds tests only, with seven controlled mutation
  failures demonstrated and restored. The four reported defect groups initially
  failed deliberately; lead production repairs now make all four pass. DB,
  core/newrepos, core/proposals and architecture tests pass.
- Repairs: Uncertain requires a real stage and matching init/publish resume;
  persisted effects validate proof shape/kind/stage on insertion, settlement
  and load, with a separate typed CommentReceipt; terminal effects cannot be
  resettled; init file receipts bind to confirmed file-content hashes; remote
  and origin receipts must agree; state/resume parsers reject foreign fields
  even when explicitly zero or null. Full checks and new dual reviews pending.
- Human separately authorized deleting /tmp/go-build2074738072 (abandoned
  399 MiB build scratch); that exact directory was removed. This grants no
  broader cache, file or branch deletion authorization.

- Post-repair generation, lint and typecheck pass again; the sequential full Go
  and frontend checks are still running. Request a fresh pair of reviews on
  f64ed1e repairs and test-engineer coverage for the new typed comment-proof
  boundary before foundation gate closure. No feature batch has started.

- Focused safety recheck found one new Must-fix in the newly added comment
  receipt: Head used abbreviated-hash validation (7–64) instead of the exact
  40/64-character rule already used by ReviewComment.ExpectedHead. Current test
  agent was steered to reject abbreviated/invalid lengths and hand off ownership;
  lead will use checkFullHead after that handoff. No concurrent production edit
  to its owned package, no new feature work, no weakening of the identity rule.

- Functional recheck also found that equal remote/origin URLs could name a
  different destination from PublishSpec. Lead fixed core/newrepos in 2aa194c
  (a package not owned by the active DB/core-proposals test task); the test agent
  was steered to correct legal destination fixtures and reject wrong host/owner/
  repo in active/resumed states. The full-head receipt fix waits for its package
  handoff. Batch 1 briefs are prepared in
  [phase-6-batch-1-briefs.md](phase-6-batch-1-briefs.md), not dispatched.

- Comment-proof test branch t3/comment-receipt-effect-tests (50f8dea) merged
  as e812f7d; it adds independent receipt validity/fuzz seeds, SQLite settlement
  matrix and wrong-destination active/resume regressions. Fixture URLs were
  corrected to the confirmed destination without weakening assertions. Lead
  applied checkFullHead in 8b0da11 after package handoff. The full targeted
  DB/core/proposals/newrepos/architecture suite now passes, including all
  previously intentional failure groups.
- The human granted routine disposal of generated Go build/lint caches and
  completed Phase-6-owned build/test scratch for this phase. This excludes source
  files, worktrees and branches; no further repeated directory approvals are
  needed within that generated-artifact scope. Cleared only those disposable
  caches and completed owned RAM scratch before final foundation checks.

- Final safety recheck at 54bcf64: PASS, no Blocking/Must-fix or new Should-fix
  findings. Full-head and confirmed-destination repairs and tests are verified.
  Functional recheck and final mandatory checks are still pending.
- Prepared a host-local validation helper (/tmp/ygg-phase6-validate.py) to run
  the five actual Task commands under one shared file lock, with low Go
  concurrency and owned RAM scratch. Batch 1 agents will use distinct labels;
  this avoids competing full builds on the constrained machine without changing
  product code, public contracts or the supported Windows Task commands.

- Final functional recheck at 54bcf64: PASS, no Must-fix or Should-fix in the
  reviewed diff. Both review gates now pass for the complete foundation repair
  set. The documented Agent/Model replay representation correction remains a
  required lead-owned follow-up before Batch A, with dispatch still disabled.
  Final post-cleanup five-command verification is running; no Batch 1 dispatch
  until its results are all zero and generated output is unchanged.

- Foundation gate CLOSED: all five actual Task commands passed on main at
  3341505, with source through 8b0da11 and no generated-file changes. Results
  are recorded in /tmp/ygg-phase6-freeze-check-results.json: generate, lint,
  typecheck, test and test:web each exit 0 (528 frontend tests). Both final
  independent reviews PASS with no Blocking/Must-fix findings. F0–F3 are done.
- Batch 1 dispatch authorized now: XH, XT and XR are independent, behavior-
  preserving extraction assignments under the approved designs. Their exact
  batch-start/foundation dispatch commit is the commit containing this entry;
  each brief receives its resolved SHA. No Phase 6 feature checkbox is complete
  yet. Agent/Model immutable replay repair remains required before Batch A.

- Batch 1 started from 1d33a88. All three Codex assignments were interrupted
  by the provider usage limit before committing production extraction changes.
  Preserved branches: XH t3/extract-host-context (one host characterization
  test file); XT t3/extract-task-routing-choices (one submission test file);
  XR t3/extract-repo-status-decisions (one repo facts characterization file).
  No Batch 1 branch has been merged and no feature acceptance is claimed.
- Human instructed continuation on November 13. Resume the same three isolated
  implementation threads/worktrees with their existing briefs and ownership.
  Re-establish characterization results before production edits; serialize
  scoped Go compilations as well as final five-command validation to avoid
  competing SQLite builds. Main remains at the verified foundation baseline
  plus documentation only. No provider/model switch or scope change.

- XH completed at a23bbbc on t3/extract-host-context; lead reviewed all four
  changed files and merged with --no-ff as 4c5de80. Authority fallback, target
  eligibility and poll result identity mapping are now pure helpers; existing
  host reads/ContextStep activation remain deferred. Characterization passed
  before/after, scoped/race suites and three restored controlled mutations
  passed. Agent's final five checks pass (528 frontend tests). First full test
  attempt reproduced the known timestamp-tie DB ordering fixture flake; the
  unchanged retry passed, with the failed log preserved.
- Post-XH integration checks are queued/running under label B1-main-XH. With
  approximately 150 MiB free, approved disposable Go build/lint cache cleanup
  runs under the same validation lock before those checks. No second branch
  may merge until all five integration checks pass. XT/XR retain ownership;
  batch review starts only after all three sequential merges and checks.

- XR completed at ae638e8 on t3/extract-repo-status-decisions; lead reviewed
  all four changed files. PullCompletion preserves unchanged/changed HEAD and
  post-read-failure outcomes; existing status/refresher/action logic stays pure
  and unchanged. Characterization before/after, scoped/architecture/race,
  131,678 fuzz executions, restored negative control and both opt-in performance
  checks pass. All five mandatory tasks pass (528 frontend tests); Windows-only
  checks remain skipped on Linux. Repo/core-repo ownership is released.
  XR is ready but NOT merged: post-XH integration validation B1-main-XH still
  waits for the shared validation slot. Approved cache cleanup completed and
  restored approximately 2 GiB headroom. No second merge before XH checks pass.

- Post-XH integration B1-main-XH passed all five actual Task commands,
  each exit 0, with generated output unchanged. Lead then merged reviewed XR
  with --no-ff as 91d0048 (source ae638e8). Post-XR integration validation
  B1-main-XR is queued/running through the shared lock; no XT merge until its
  five results pass. Batch-start remains 1d33a88 for the eventual dual review.

- XT completed at 5050201 on t3/extract-task-routing-choices; lead reviewed
  all four changed files. Stateless submission normalization, deployment/scope
  refusals and new-task defaults move into core; ordinary guard admission,
  trust, modes, queue and locks stay unchanged. Characterization before/after,
  three restored mutation controls, scoped/race checks and all five mandatory
  tasks pass (528 frontend tests). Task/core-task ownership is released.
  Known resumed-scope membership and creation/reload ordering behavior is
  preserved, not silently repaired in extraction. XT is reviewed but NOT merged
  until B1-main-XR completes all five integration checks.

- Post-XR integration B1-main-XR passed all five actual Task commands, each
  exit 0, generated output unchanged. Lead then merged reviewed XT (5050201)
  with --no-ff; its merge SHA is recorded by the preceding merge commit.
  B1-main-XT is the final sequential integration check set now running. All
  three extraction assignments have landed; Batch 1 is NOT closed until these
  five checks and fresh safety/functional reviews of 1d33a88..main pass.

- Batch 1 safety review of 1d33a88..9112850: PASS, no introduced Blocking,
  Must-fix or Should-fix findings. All 11 safety invariants and architecture/
  privacy checked read-only; all 12 changed source/test files read. Existing
  task membership/order findings remain inherited; no feature completion claimed.
  Independent functional review remains pending. Final B1-main-XT generation,
  lint and typecheck pass; Go/frontend suites remain pending, so Batch 2 stays
  gated. No new implementation agent has been dispatched.

- Batch 1 independent functional review of 1d33a88..9112850: PASS, no
  Must-fix, Should-fix or required Consider findings. All 12 Go files read;
  explicit-diff purity scan reports: 'Model Functional Effect: 3 core file(s)
  checked, no effects found.' Tests and restored negative controls inspected,
  not rerun by reviewer. Both review gates now pass. B1-main-XT final Go and
  frontend results remain pending; no Batch 2 dispatch before all five pass.

- Final B1-main-XT task test FAILED (exit 201): inherited DB fixture
  TestListActiveTaskStates again received identical creation timestamps and
  asserted insertion order although SQL uses created_at,id. Preserve this
  failed log; do not rerun until green or weaken ordering assertions. Both
  extraction reviews pass, but the batch remains open. Assign one Codex
  test-engineer to make this fixture deterministic, demonstrate ordering/
  filtering faults are caught, and commit tests only before integration.

- Active-task fixture test-engineer reported repeated deterministic cases and
  six restored negative controls passing, plus DB/race suite passes. Its final
  validation hit ENOSPC at lint (only 8 MiB free), not a product failure. Lead
  acquired the shared validation lock and performed the already-authorized
  disposable Go build/lint cache cleanup; disk headroom restored to 2 GiB.
  Source/worktrees/branches untouched. Agent told to preserve failed logs and
  retry the five tasks with a distinct label, without repeating passing scoped
  race checks unnecessarily. No new cleanup permission was requested.

- Test-engineer fixture repair 0c744cc on test/phase6-active-task-fixture
  reviewed and merged with --no-ff (preceding merge commit). Only
  internal/db/store_tasks_test.go changed: fixed IDs/times exercise timestamp
  order and equal-timestamp ID order against reverse insertion, preserving
  exact active filtering/order assertions. Both cases pass 100 repetitions;
  DB/race suites pass; six negative controls fail as intended and are restored.
  Agent's five-task retry passes, including 528 frontend tests; failed ENOSPC
  and initial integration logs remain preserved. Ownership released.
- B1-final now runs all five post-merge integration tasks; fresh safety and
  functional rechecks will cover the complete batch plus deterministic fixture
  change before Batch 1 closure. No Batch 2 implementation has started.

- Final Batch 1 safety recheck of 1d33a88..7bf567f: PASS, no introduced
  Blocking/Must-fix/Should-fix. The repaired fixture faithfully asserts SQL
  created_at,id ordering at persisted millisecond precision, cardinality and
  active-state membership against reverse insertion; it masks no production
  failure. All 11 invariants rechecked. Final functional recheck remains
  pending; B1-final generation and lint pass, other results still pending.

- Final Batch 1 functional recheck at 7bf567f: PASS; no Must-fix, Should-fix
  or required Consider findings. Explicit-path purity scan again checks all
  three new core implementation files with no effects. Fixture oracle and
  exact ordering/filtering assertions verified against SQL, store precision
  and test helper behavior. Both final reviews pass; B1-final generation,
  lint and typecheck pass, while Go/frontend results remain pending.

- Batch 1 CLOSED: final B1-final integration runs all five actual Task commands
  with exit 0, including full Go/root/demo suites and 528 frontend tests.
  Generated output unchanged. Final source merge dfa56b8; both safety and
  functional rechecks PASS at 7bf567f with no Blocking/Must-fix findings.
  No failed, interrupted or ENOSPC run is counted as a pass. XH/XT/XR and
  deterministic fixture repair are complete; Phase 6 feature boxes stay open.
- Batch 2 briefs prepared in phase-6-batch-2-briefs.md. H/T/R own disjoint
  host/task/repo package trees and use the committed main dispatch SHA supplied
  by the lead. All shared contracts remain frozen. Missing persistence/port
  needs must be reported for a serial lead correction, never approximated by
  in-memory deduplication or edits outside ownership. Agent/Model replay fix
  remains required before Batch A. Batch 2 starts at the commit of this entry.

- All three Batch 2 Codex runs hit the provider usage limit shortly after
  dispatch. H t3/host-context-adapters, T t3/task-draft-admission and
  R t3/repo-noise-history remain clean at f217877; no feature commit or merge.
  Human instructed continuation; provider/model preference remains Codex.
- T's initial capability audit identified missing shared persistence ports:
  transaction-scoped tracked/READY project-repo lookup and a durable immutable
  RequestID/digest/task ledger, atomically committed with task/turn/audit.
  Existing workspace_action_requests cannot substitute for task submission.
  Dependent implementation remains stopped as required by ownership rules.
- Serial contract completion F4: one go-backend agent owns internal/db/** only
  (schema/query/store/tests/generated Go) before any Batch 2 resume. Add a new
  migration (never edit applied 00007), narrow Tx/Store submission lookup/link
  and tracked-ready repo methods; enforce duplicate equality, digest mismatch,
  foreign keys and rollback. Also repair the accepted full immutable action
  request replay representation including Agent/Model before dispatch is ever
  enabled. Existing incomplete legacy payloads must fail closed, never invent
  identity or silently bypass digest validation. PRD AC-01/05, NR-02, SF-05.
  DoD: restart/upgrade/fresh/rollback/concurrency boundary regressions, all five
  Task commands and relevant race checks, committed/merged on main, fresh
  safety/functional review gate clear before resuming H/T/R. No feature workers
  enabled by this contract work. Source/public API/core types remain frozen.

- F4 branch t3/task-submission-action-replay (cb82250, fe7f8c7) reviewed and
  merged with --no-ff; source merge and whitespace-only schema cleanup are the
  preceding two commits. Migration 00008 adds permanent task submission links
  and nullable Agent/Model columns; legacy incomplete action rows explicitly
  refuse replay with ErrLegacyActionRequest. Applied 00007 is unchanged.
  Full immutable requests/digests validate on load/update. No worker enabled.
- Frozen F4 methods on Tx: GetTaskSubmission(ctx,RequestID)(TaskID,string,error),
  LinkTaskSubmission(ctx,RequestID,string,TaskID)error and
  GetTrackedReadyRepo(ctx,ProjectID,RepoID)(core.Repo,error); Store also exposes
  GetTaskSubmission for reconciliation. Task/turn/link/audit must share Commit;
  any losing transaction rolls back and reloads the winning link afterward.
- Agent reports all five actual tasks, full DB/race, migration/restart/rollback
  and contender tests passing, three restored mutation controls, 6,788 final
  decoder fuzz executions. Earlier ENOSPC/lint failures are retained, not passes.
  Environment restart expanded disk to 69 GiB (31 GiB free), and old temporary
  Node tooling disappeared. Agent restored Node 22/pnpm 11.4 under task-owned
  temporary tooling; lead uses it on PATH for post-merge F4-main checks.
- Fresh safety/functional F4 reviews and all five main integration checks remain
  gates before H/T/R resume. Batch 2 feature work is still stopped.

- F4-main post-merge integration PASSED all five actual Task commands, each
  exit 0, with no generated drift. Source through 459a059, main snapshot
  2738660. Both initial F4 reviewers hit the Codex usage limit before verdict;
  partial inspection is NOT a review pass. Human instructed continuation.
  Launch fresh safety/functional review invocations over the same source diff;
  do not resume prior review turns or switch provider/model. Batch 2 remains
  stopped until both independent review gates are clear.

- Fresh F4 safety review of 4070f3e..f2a5180: PASS, no Blocking/Must-fix/
  Should-fix findings. Transactional contender rollback, complete immutable
  action replay, legacy refusal, SQL project/readiness scope, generated SQL
  agreement and migration/FK/index semantics checked. All 11 safety invariants
  reviewed. All five main integration checks already pass; independent
  functional review remains the only outstanding F4 gate. H/T/R still stopped.

- F4 functional review FAIL: BeginActionRequest could save a non-refused row
  with a refusal or a refused row with an unknown reason, poisoning subsequent
  loads/recovery. Must-fix is shared status/refusal/key validation before begin,
  load and save. Should-fix: decoder fuzz reused Request.Validate as its oracle.
  Lead applies the small production repair, then hands DB tests to test-engineer
  for regression and independent accepted-implies-valid coverage. Safety PASS
  does not override this functional gate. Batch 2 remains stopped.

- F4 regression branch t3/phase6-f4-db-tests (47b7634) reviewed and merged
  with --no-ff. Only two test files changed; no schema/production edits. The
  status/refusal/key matrix covers 192 begin/save combinations with rollback,
  reload/duplicate/unsettled consistency. New decoder oracle independently
  checks request, status and digest rules including nullable identity. Existing
  migration/restart/contender tests retained. All 19 compilable mutation controls
  fail assertions and are restored; 29,010 fuzz executions, scoped race, full DB
  and five mandatory tasks pass. Earlier test-lint failure preserved separately.
- Lead's earlier scoped shell session is no longer accessible after environment
  changes; no result is claimed for it. Agent's recorded tests verify the repair.
  F4-final now runs all five post-merge integration checks. Both fresh reviews
  must verify ee41fd4 production repair plus 47b7634 regression coverage before
  Batch 2 resumes.

- Repaired F4 safety recheck at 4341a6b: PASS, no Blocking/Must-fix/
  Should-fix. Shared insert/load/save validation and independent test predicates
  resolve both findings. Reviewer inspected all 19 mutation logs and found
  assertion failures, not compilation-only failures; transaction/readiness/
  migration/legacy contracts and all safety invariants rechecked. Final
  functional review and F4-final integration remain pending (generate/lint
  pass so far). Batch 2 remains stopped.

- Repaired F4 functional recheck at 4341a6b: PASS, no Blocking/Must-fix,
  Should-fix or Consider findings. Insert/load/save parity, independent oracle
  and all 19 assertion-failing/restored mutation controls verified; durable
  ledger, legacy replay and scoped lookup contracts remain intact. Both final
  review gates now clear. F4-final generate/lint/typecheck pass; Go/frontend
  results remain pending. Do not resume Batch 2 before both finish successfully.

- F4 CLOSED: repaired-source final safety and functional reviews both PASS at
  4341a6b with no remaining findings. Lead F4-final runs all five actual Task
  commands with exit 0, including full Go/demo suites and 528 frontend tests,
  generated output unchanged. The accepted Agent/Model replay follow-up is
  RESOLVED, not deferred before Batch A any longer. Migration 00008 and all
  added DB consumer contracts are frozen on main.
- Resume existing H/T/R worktrees only after fast-forwarding to the committed
  main resume SHA supplied by the lead. They were verified clean. Original
  Batch 2 dispatch f217877 contained no feature implementation; effective
  Batch 2 review base is this closure/resume commit, excluding serial F4 from
  the feature diff. Ownership and approved feature scope remain unchanged.
  All future missing shared ports still require serial lead changes. No
  Phase 6 feature checkbox is marked complete by these persistence contracts.

- Batch 2's resumed H/T/R runs hit quota again. Uncommitted feature work is
  preserved only in their owned package trees; no feature branch merged. T
  scoped/race/mutation/fuzz work passes, final UTF-8 boundary repair/checks
  remain; H final scoped/HTTP/race/fuzz checks remain; R noise scoped/race/fuzz
  work passes but final five-task retry and history collection remain. Prior
  failed compile/lint attempts remain distinct from passes.
- H identified a real frozen authorization-port gap: the callback-only method
  could not inspect destination. Lead changes PushAuthorizer.Authorize to take
  the instance and *http.Request, aligning the existing git.Authorizer adapter;
  implementations may set only Authorization after validating the configured
  HTTPS clone origin, never a foreign destination/credential URL/redirect. H's
  port file was unchanged and H is stopped; no concurrent ownership edit.
- R needs a read-only Git.LogRange(ctx,dir,from,to,max)([]Commit,bool,error)
  capability. Serial shared task F5 owns internal/git/** only before resuming
  features. Keep existing Commit.Date (author timestamp) semantics, half-open
  UTC range, deterministic newest-first output, bounded bytes/count/runtime,
  cancellation and honest truncation; never sample the latest max commits and
  claim that an older date range is complete. No new git mutation. New port and
  tests must be committed/checked/reviewed on main before dependent R resumes.
- Routine integration choices resolve T's remaining presentation questions:
  import-selected Agent/Model stay immutable; matching/omitted StartDraft choices
  use ordinary admission, differing choices refuse clearly. No new DB replacement
  port is needed. W/U will show imported drafts in a separate draft list/section
  on the board using the existing project task-list status=draft endpoint, with
  explicit Start; preserve AG-12's four running-task columns. Internal repo-setup
  drafts remain hidden by SQL. No fifth column, false completed task, automatic
  start or changed safety policy is authorized. API/UI completion stays in W/U.

- F5 t3/git-log-range (a506456) reviewed and merged with --no-ff. Only five
  internal/git files changed. LogRange preserves author-date [from,to), UTC
  precision, Date-desc/hash-asc ordering, <=31 days and <=200 records. Its
  read captures <=1 MiB stdout/16 KiB stderr within five seconds; cutoff yields
  complete observed records with explicit partial/truncated status, never a
  false-complete latest-only sample. Existing Log/LogRef remain unchanged.
  Shared bounded capture reports pipe cutoff and keeps context/wait errors.
- Agent reports all five tasks PASS (528 frontend tests), full package race,
  64,914 fuzz executions and four restored assertion-failing mutations for
  upper bound/completeness/old-range/date-source rules. Earlier fixture/lint
  failures retained. Linux validation only; Windows checks remain manual.
- F5-main runs the five post-merge tasks. Fresh safety/functional review must
  cover the full serial shared delta since 22e6100, including 68f9d66's
  destination-aware host authorization declaration, before H/T/R resume. Their
  uncommitted feature work remains preserved and stopped; no feature merge yet.

- F5 safety review at 35cb7dd: PASS, no Blocking/Must-fix. Author-date
  boundaries/truncation, clean-env/read-only runner, cancellation/error
  precedence and destination visibility checked. Consider-only follow-up for
  TM1: direct bounded/redacted stderr, platform pipe-cutoff and genuine Wait
  error tests; no demonstrated defect or blocker. Windows remains manual.
  F5-main generate/lint/typecheck pass; Go/frontend and independent functional
  review remain pending. H/T/R feature work remains stopped and preserved.

- F5 functional review FAIL despite green integration: byte-bound fixture uses
  a 100 KiB argv message exceeding Windows' limit, and capped stdout could
  conceal an ordinary nonzero exit while pipes drain. Should-fix: independent
  parser identity oracle/round-trip/rejection coverage. No feature resume.
- Lead source repair adds optional platform-owned actual-termination evidence
  (no Process interface break): WasTerminated distinguishes natural exits from
  successful Kill. Unix requires a sent kill plus SIGKILL outcome, synchronized
  with reaping; Windows uses a private process-job termination exit marker and
  existing lock ownership, leaving task-wide termination unchanged. Git accepts
  capped nonzero output only with that evidence; ordinary failures remain errors.
  A private injected starter supports deterministic runner contract tests;
  production still starts through platform.Start with the same clean policy.
  No OS-specific logic moved outside platform. Tests are handed to one
  test-engineer after this source commit; no concurrent lead package edits.

- F5 test branch t3/git-range-test-repairs (10e87b3) reviewed and merged
  with --no-ff. Seven test files only: portable message-file fixture, injected
  capture error/metadata/privacy contracts, independent parser oracle/full-field
  round trips, and real platform termination vs late-Kill proofs with Windows
  handshake/cross-compile coverage. Existing source invariant test follows the
  default platform starter seam without weakening process/env assertions.
  All 25 controlled faults fail assertions and are restored; 65,302 fuzz
  executions, full owned tests, scoped race, Windows/amd64 cross-compilation
  and all five actual Task commands pass (528 frontend tests). Windows runtime
  remains manual; no remaining production defect exposed. Ownership released.
- Lead F5-final now runs the five post-merge checks. Fresh safety/functional
  reviews must verify 9c4147c source repair and 10e87b3 regressions before
  H/T/R resume. Pre-repair F5-main passes remain historical only.

- Repaired F5 safety review FAIL: inherited Unix negative-PID signaling can race
  reaping/PID reuse. The new killMu guarded bookkeeping only; Kill can pass its
  exited check, pause while cmd.Wait reaps, then signal an unrelated reused
  group. Existing late-Kill tests exclude this interleaving. Prior argv/capture/
  oracle findings are verified fixed. F5-final remains unclaimed while running.
- Serial platform-owner correction is required in internal/platform/** only:
  observe exit without releasing identity, exclude in-flight/new group signals,
  then finalize reaping under a proven lifecycle protocol. Do not hold a mutex
  across a blocking wait on a live process (cancellation deadlock), merely move
  the exited store after Wait, or replace it with a racy liveness probe. Preserve
  actual termination evidence, clean env, group/job ownership and bounded drains.
  Linux/Darwin remain supported; Windows semantics stay intact.
- Protocol reference: Go's wait-before-reap pattern
  https://go.dev/src/os/wait_waitid.go and POSIX/Linux waitid WNOWAIT
  https://man7.org/linux/man-pages/man2/waitpid.2.html . Darwin needs an actual
  supported non-reaping observer (Go documents its historical waitid stop-event
  issue); a Linux-only assumption is insufficient. The owner must document
  the exact supported-OS protocol and failure behavior, not weaken scope safety.
- Add deterministic regression for Kill paused AFTER liveness check but BEFORE
  group signal while exit observation advances. Prove reaping cannot release
  identity until that signal completes, and no signal occurs after release.
  Demonstrate the old ordering fails, restore, then run package/race/repetition,
  Linux/Darwin/Windows compile+vet and all five tasks before review. H/T/R remain
  stopped with their uncommitted features preserved.

- F5 repaired-capture functional review also FAILS on the same Unix lifetime
  race already assigned to the platform owner. No additional Should-fix or
  Consider findings; portable argv, error precedence, Windows evidence and
  independent parser tests are verified fixed. No duplicate repair assignment
  or production edits while platform ownership is active. Both reviews require
  new invocations after the lifecycle correction and final integration pass.

- Platform lifecycle branch t3/fix-unix-process-lifecycle (a7b1558) reviewed
  and merged with --no-ff. Six platform files only. Linux non-reaping waitid
  and Darwin kqueue NOTE_EXIT retain the child identity; run observes exit,
  joins pending signals under killMu, closes the signal gate, then reaps
  outside that mutex. Setup/observation failures fail closed; no live-process
  blocking wait holds the cancellation mutex. Other unsupported Unix targets
  return a clear unsupported-observer error. Windows API/behavior unchanged.
- Deterministic native regression pauses Kill after liveness check, observes
  exit without reaping, and proves identity cannot release before signal
  completion. Old-order mutation fails assertions without issuing a post-reap
  syscall. Repaired 100-repeat/race checks, full platform/race, Linux/Darwin
  amd64+arm64/Windows amd64 compile+vet and all five tasks pass (528 frontend
  tests). Initial lint failure and a 527/528 board-timeout attempt are retained
  separately; final corrected-source pass is the evidence. Native Windows and
  Darwin runtime checks remain manual, including stopped-child/rapid-exit and
  job sibling/detached-editor behavior. Platform ownership released.
- F5-lifecycle-main runs all five post-merge tasks; fresh safety/functional
  review of the full shared delta and lifecycle repair is required before
  H/T/R resume. Their feature work remains preserved/stopped.

- F5 lifecycle safety recheck at 824d4f5: PASS, no Blocking/Must-fix in
  reviewed delta. Prior Start signal/reap race closed; Linux WNOWAIT and Darwin
  attachment/reference/NOTE_EXIT ordering verified against native contracts.
  Deterministic native regression covers the previously missed interleaving.
  No competing child reaper found. Final integration/functional gate pending.
- Explicit inherited Should-fix carry-forward (outside current diff):
  internal/platform/pty_unix.go has analogous signal/reap race and unconditional
  post-reap group SIGHUP. Do not claim platform-wide lifecycle closure. Schedule
  a sequential platform-owner handoff with deterministic regression before phase
  close (may use N's allowed sequential specialist handoff; no overlapping
  platform ownership). This is not a current Batch 2 review blocker, and must
  not be lost during later bookkeeping or baseline review.

- F5 lifecycle functional recheck at 824d4f5: PASS, no Blocking/Must-fix,
  Should-fix or Consider in reviewed delta. Native retained-identity protocol,
  Darwin attachment proof, error cleanup, actual missed-interleaving regression
  and existing capture/parser/authorization contracts verified. Both final
  reviews clear current shared changes; inherited PTY follow-up remains tracked
  outside the delta. F5-lifecycle-main Go/frontend checks still pending.

- F5 CLOSED: full shared delta safety and functional reviews both PASS at
  824d4f5; no Blocking/Must-fix remains in reviewed changes. Lead
  F5-lifecycle-main actual generate/lint/typecheck/test/test:web each exit 0;
  full Go/demo suite and 528 frontend tests pass, generated output unchanged.
  Native Windows/Darwin runtime checks remain manual. The inherited PTY
  Should-fix remains explicitly scheduled before close, not silently discarded.
- Resume original H/T/R dirty worktrees without discarding feature edits;
  fast-forward to the resolved main SHA of this closure commit. Both missing
  shared capabilities are now committed/verified/reviewed: destination-aware
  PushAuthorizer and bounded author-date LogRange. Current effective Batch 2
  review base is this closure commit so serial F5 changes are excluded from
  the future feature diff. T's immutable Agent/Model and separate draft-list
  presentation choices are documented in the handoff. Final feature checks
  must follow source adaptation and preserve all earlier failed logs.

- Latest H/T/R continuation again stopped at the Codex usage limit, now before
  final feature validation/commit. All owned edits are preserved: H includes
  the new request-aware authorizer; R includes history collection; T still
  needs its final UTF-8/audit-boundary repair and current-source validation.
  No feature branch has merged and main remains verified shared source plus
  bookkeeping. Human instructed Continue; resume the same three threads and
  worktrees with original ownership, no provider switch or checkout replacement.
  Final validation must cover current adapted source and preserve old failed/
  interrupted logs. Effective Batch 2 review base remains 55d2c4e.

- T branch t3/task-draft-admission committed dc0d9f8 and released ownership.
  Backend draft/request/admission scope and scoped race/architecture/fuzz/
  restored mutations pass; generate/lint/typecheck/test:web pass. Final task
  test FAILS in inherited cmd/ygg invariant8_phase4 test, repeated 2/10 in
  isolation. DoD remains open; no T merge or feature completion claimed.
- Lead traced the failing close scenario: rejecting its sole proposal permits
  the existing changeset reconciler to auto-close it (approved changesets design
  table: all rejected => Closed) before the test enables the audit fault. The
  test then incorrectly assumes Open. Prepare a deterministic closable Open
  fixture/precondition instead of disabling reconciliation, weakening audit
  assertions or relying on timing. One test-engineer owns only the cmd/ygg
  fixture file, parallel with H/R's disjoint package ownership (three total).
  Fix/verify the fixture on main before integrating T; preserve T's failure logs.

- R branch t3/repo-noise-history committed 2d0cdc1 and released both repo
  package trees. Lead inspected all changed production/test files and handoff:
  bounded cached noise facts, pure conservative classifier/selection, tracked
  history range/count/parser contracts and partial-zero handling match R scope.
  No lead integration finding identified. All five agent Task commands pass,
  with 528 frontend tests; owned race/fuzz and restored assertion-failing
  threshold/partial controls recorded in /tmp/ygg-B2-R-handoff.json. This is
  branch evidence only: no merge, independent review or feature completion yet.
- H and the audit-fixture test-engineer both quota-stopped with owned edits
  preserved and no commits. Human instructed Continue; resume those same
  Codex threads/worktrees with original disjoint ownership. The fixture repair
  remains first in the integration sequence, followed by feature branches one
  at a time with all five main checks. Effective Batch 2 base stays 55d2c4e.

- H committed 63c14d9 on t3/host-context-adapters, all host package ownership
  released. Scope/race/property/HTTP contracts and 36,251 fuzz executions pass;
  restored head-guard mutation fails its assertion. Fresh generate/lint/typecheck
  pass, but test fails inherited audit-close fixture and test:web fails untouched
  BoardPage.test first case while still Loading (Working region missing), 527/528.
  Earlier green runs are historical, not substitutes for this failed gate.
  DoD remains open and H is unmerged. Diagnose frontend fixture before integration;
  no retry-until-pass or assertion weakening. Audit-fixture repair remains first.

- Audit-fixture branch 075c4a1 merged --no-ff as d7176ab. Lead reviewed
  the complete diff: separate empty Open set removes the legitimate auto-close
  race; unchanged stored/API/audit state under HTTP 500, identical successful
  control and exact version/audit assertions strengthen invariant 8. Original
  propose/note/confirm checks retained. Agent all five tasks pass, 30 repeats,
  five race runs, architecture and two restored assertion-failing controls pass.
  Lead B2-audit-main five-task integration is queued/running, not yet claimed.
- Lead reviewed T production/admission/dedup tests and H production reducers,
  readers, HTTP boundaries, redaction and confirmed capabilities before merge.
  Found H job log reads used remaining aggregate budget instead of 256 KiB
  per-job cap. Small lead repair bda193d on released host branch takes the
  smaller limit; an oversized log stops with explicit partial/truncated output.
  Earlier branch passes do not verify this repair. Fresh combined-main checks
  and post-land test map must cover the exact per-job cutoff and aggregate
  failed-read accounting; no host feature merge or final review claimed yet.

- B2-audit-main CLOSED: actual generate/lint/typecheck/test/test:web each
  exit 0 after audit-fixture merge, frontend 528/528. No feature branch merged.
  Board fixture investigation remains in its isolated test-only worktree.

### F6 — serial Unix PTY identity repair (inherited review follow-up)

One platform-windows agent (Codex), before N takes platform ownership. Only
`internal/platform/**`, including tests; no concurrent platform owner. Supports
PL-02, SF-04 and safety invariants 5/11; contributes reliable terminal/process
capabilities to Phase 6, without claiming E6 feature completion. Reuse F5's
`exitObserver` retained-identity protocol and existing PTY/Process interfaces;
no shared interface, dependency, schema or generated changes. The remaining
`pty_unix.go` paths signal after liveness checks or unconditionally after reap.
Both cancellation SIGKILL and natural-exit SIGHUP must finish while identity is
retained; exclude future signals before actual reaping without blocking live
process cancellation. Preserve scoped children/hangup, Close/Wait/error/registry
semantics and Windows behavior. Use the lifecycle proof in lifecycle_unix.go.

DoD: deterministic native-observer regressions for both missed interleavings,
restored safe assertion-failing controls, full platform/race/repetitions, all
five actual Task commands, CGO-disabled Linux/Darwin/Windows compile/vet. Lead
merges sequentially with five main checks and fresh safety/functional reviews
before platform ownership passes to N. Windows ConPTY/tree cancellation and
Darwin native kqueue/stopped-child/rapid-exit behavior remain manual; report
exact steps and expected outcomes. No real host or agent execution.

- Board fixture branch b59b30d merged --no-ff after lead diff review. Only
  BoardPage.test.tsx changes: promise-returning React act around initial real
  App render, awaited by every caller; existing 25 query/assertion lines retained.
  Probe established old helper returned before API requests began; awaited
  boundary completes initial router/query lifecycle. Exact historical timeout
  scheduling remains uncertain; no production defect claimed. Agent 20/20
  scheduled runs (260 tests), nine restored assertion-failing controls, 528/528
  frontend and all five final commands pass; initial lint failure retained.
  Lead B2-board-main five-task integration is running/unclaimed. T/R/H remain
  unmerged until it passes; F6 alone owns platform in its separate worktree.

- B2-board-main: generate/lint/typecheck/test PASS, test:web FAIL (527/528).
  Board tests pass; untouched WidgetGrid.test remove/persist/reset case times
  out at 5 seconds (5114 ms), no assertion failure shown. Preserve original
  /tmp/ygg-phase6-B2-board-main-test-web.log. The green branch run does not
  close main gate. Same test-engineer gets separate ownership ONLY of widget
  grid test file to diagnose lifecycle/query costs with controlled evidence;
  no blanket timeout/retry or assertion relaxation. F6 platform remains disjoint.
  All feature branches remain held until this integration failure is resolved.

- Widget-grid fixture c5c3432 merged --no-ff after full lead diff review.
  Awaited real App render and Widgets-scoped positive control queries retain
  all 23 original assertions, global absence checks and portaled keyboard menu
  queries. Probes select identical controls with 11 fewer computed-style calls
  per query; no claim to reconstruct historical timeout scheduling. Agent
  20/20 scheduled runs (100 tests), 22 restored assertion-failing controls,
  full frontend 528/528 and all five Task checks pass with no skip/timeout
  changes. Exact report /tmp/ygg-B2-grid-handoff.json. B2-grid-main integration
  is queued/running; no pass claimed until completion. Feature merges still held.

- B2-grid-main CLOSED: actual generate/lint/typecheck/test/test:web all exit 0,
  frontend 528/528. Both inherited integration fixture failures are repaired
  without production changes, skipped assertions, timeout changes or lucky retries.
- T source dc0d9f8 merged --no-ff at 97a7b63 after lead source/test diff review.
  Lead B2-T-main five-task gate is queued/running, not yet a pass. R/H remain
  unmerged. Derived the T post-merge invariant/parser/capability map and prepared
  TM1 brief in [phase-6-batch-2-test-map.md](phase-6-batch-2-test-map.md).
  TM1 implements it at the approved Batch 4 handoff, with controlled failure
  evidence; no premature parallel ownership of platform or later feature owners.

- F6 platform branch t3/f5-pty-exit-signal-race committed 6e5d525 and released
  ownership. Lead read all three changed files: native non-reaping observation,
  joined cancellation signals and final hangup precede gate closure/reap; setup/
  observer errors preserve failure and cleanup, Close timeout closes the master.
  No lead finding identified. Agent all five tasks pass (528 frontend), full
  platform/race, 100 critical/race repetitions and Linux/Darwin/Windows compile/
  vet pass; both safe restored bad-order controls fail assertions. Native foreign
  runtime remains manual; exact report /tmp/ygg-f6-pty-report.txt. Unmerged: T
  main integration must finish first. Fresh F6 main checks and both independent
  reviews remain required before N receives platform ownership.

- B2-T-main CLOSED: actual generate/lint/typecheck/test/test:web each exit 0,
  frontend 528/528. T backend integration gate passes; feature wiring/UI and
  independent Batch 2 reviews remain downstream. No full PRD completion claimed.
- R source 2d0cdc1 merged --no-ff after prior full lead diff review. Lead
  B2-R-main five-task integration queued/running. H/F6 remain unmerged. Derived
  R post-merge invariants, parser and fake/live capability map in
  [phase-6-batch-2-test-map.md](phase-6-batch-2-test-map.md), prepared for the
  approved Batch 4 TM1 test-engineer handoff; actual ignore writes/standup
  aggregation remain with their assigned owners.

- B2-R-main CLOSED: actual generate/lint/typecheck/test/test:web all exit 0,
  frontend 528/528. H source 63c14d9 plus lead per-job repair bda193d merged
  --no-ff at cc626a7 after lead production/test review; B2-H-main gate running.
  All three Batch 2 feature branches are now merged; feature completion still
  depends on application wiring/UI and independent batch review gates.
- H post-land invariant/parser/fake-recorded capability map appended to
  [phase-6-batch-2-test-map.md](phase-6-batch-2-test-map.md), including exact
  consumed-byte regression for the lead's per-job cap repair, queued for TM1.
  F6 remains separate and unmerged. Fresh safety and functional reviewers inspect
  55d2c4e..current main; B2-H-main result remains pending, never inferred green
  from earlier H branch runs. No new implementation batch dispatched.

- B2-H-main CLOSED at 6eb753e: actual generate/lint/typecheck/test/test:web all
  exit 0, frontend 528/528, worktree clean. Batch 2 backend integration gate
  passes for T, R and H; wiring/UI and full PRD completion still not claimed.
  Environment note: the first attempt failed only because the user-level
  ~/.local/bin node and pnpm wrappers pointed into wiped /tmp scratchpads. This
  run used Node 22.23.5 + pnpm 11.4.0 installed in the session scratchpad via
  PATH; nothing under ~/.local/bin was changed. Logs: /tmp/ygg-B2-H-main-gate.log
  (failed env attempt), /tmp/ygg-B2-H-main-gotest.log, /tmp/ygg-B2-H-main-web.log.
- Batch 2 review gate NOT RUN: safety-reviewer and functional-effect:fe-reviewer
  (diff 55d2c4e..6eb753e) both died with a 429 usage/spend limit before reading
  anything; limit resets 21:00 Asia/Jakarta. Rerun both, fix every Blocking and
  Must-fix, then re-run until clean. F6 platform branch and TM1 stay unmerged/
  undispatched until the gate closes.

- Batch 2 review gate (55d2c4e..6eb753e), run after the usage limit reset:
  safety-reviewer PASS (no Blocking/Must-fix). fe-reviewer: no Blocking, one
  Must-fix M1 (MR-diff context cache not head-bound: empty key head was a
  wildcard). M1 fixed in this commit: Validate requires a full head for MR
  diffs, ContextPayloadMatches requires v.Head == k.Head, ConfirmedHosts
  commentTarget shape-checks the scope-only headless lookup MRHead needs.
  Regression tests: TestContextCacheIsHeadBound (headless key matches no
  payload; fails without the fix), "mr diff no head" in TestContextKeyValidate
  (fails without the fix), fuzz invariant + empty-head seed. fe-reviewer
  re-check: M1 resolved, no new Blocking/Must-fix. Five-task gate on the fix:
  generate/lint/typecheck/test/test:web all exit 0 (log /tmp/ygg-B2-M1-gate.log).
- Deferred Should-fix/Consider items (none Blocking/Must-fix), to be handled by
  the owner of the wiring step or queued, not forgotten:
  - wiring/H: archtest rule keeping hosts.ConfirmedHosts/CommentHost/RepoHost out
    of api/mcp/agents (safety S1); push Authorize bound to the repo path prefix
    (S2); map 401/403/422 POST replies to a definite error instead of Unknown
    (S3, FE N2); resolver errors reported as "not tracked" (note); context
    issue provenance must come from a ContextResolver-validated read (S4);
    NewGetterWithLogOrigins must be wired with an explicit allowlist.
  - core/hosts shell (FE S1-S3): shell ignores most ContextCommand values and
    pre-checks no-client/no-token itself; ContextReject is outside the sealed
    input set and a bad payload leaves Loading; deterministic provider errors
    are retried as transient. ConfigChanged goes to Empty, not Loading: update
    the design table (N1).
  - tasks (FE S4, N4): ImportIssueDraft hardcodes repo-tracked/ready facts, so
    DraftRepoUntracked/NotReady are unreachable; message() fails for a started
    task with turns counted but no turn rows (fall back to Row.Mode).
  - hosts (FE S5): LookupRepo empty-refs probe against GitHub is unverified on a
    real empty repo; added to the manual checklist, needs a test account.
  - Consider: retained cache dropped on error, real timers in the retry loop,
    three rate-limit authorities, noise snapshot size/sample bias/overlapping
    candidates, unused names, zero Now/ID validation.
- Batch 2 CLOSED for its backend scope. F6 platform branch
  t3/f5-pty-exit-signal-race is next to merge (fresh main checks and both
  reviews required before N receives platform ownership), then TM1 tests. Not
  dispatched: awaiting the human's go-ahead.

## Batch 2 follow-up: deferred review findings (human approved fixing them)

F6 platform branch merged --no-ff at 1db2d46 after lead read of the three changed
files (Unix-only; Windows path untouched). Main gate for it: /tmp/ygg-F6-main-gate.log.
Independent reviews of F6 still run after the gate; Windows PTY remains manual.

Parallel batch F1 (disjoint packages; all `go-backend`, worktree, `sonnet`):

| Task | Owns (only) | Items |
|---|---|---|
| H-A hosts writes | internal/hosts/{confirmed.go,push_authorizer.go,getter.go} and their tests, internal/archtest | safety S1 archtest rule keeping ConfirmedHosts/CommentHost/RepoHost/NewConfirmedHosts out of api/mcp/agents; S2 push Authorize/transport bound to the repo path prefix; S3 + FE N2 definite failure for 400/401/403/422 POST replies, Unknown stays for 3xx/5xx/transport/other; FE S5 LookupRepo empty-ref proof (matching-refs 200 [], or on 409 fall back to branches+tags both empty) and drop the single-element loop; NewGetter http only for loopback |
| T tasks | internal/tasks, internal/core/tasks | FE S4 ImportIssueDraft reads repo inside the tx and feeds real facts to PlanDraft, DraftRefuse -> RejectedError, GetDraftByDedupKey inside the same tx; N4 message() falls back to Row.Mode when turns are counted but no rows; zero Now/TaskID validated; DraftPending dead name |
| R repos | internal/repos, internal/core/repos | noise: keep the sample only when Untracked >= NoiseMinUntracked, drop candidates nested under another candidate, unused Now in validNoiseSelection, document sample bias in the fact |

Batch F2 (after F1 merges, because it shares internal/hosts with H-A):

| Task | Owns | Items |
|---|---|---|
| H-B context | internal/hosts/context_*.go (not confirmed.go/push_authorizer.go/getter.go), internal/core/hosts, docs/design/phase-6-host-context.md | FE S1 shell interprets ContextCommand values and the no-client/no-token pre-checks go through the core; S2 ContextRejected in the sealed input set and a bad current-attempt payload leaves Loading no more; S3 deterministic provider errors (scope, decode, invalid identity) are not retried, MR with no version yet is retryable not unsupported; retained cache kept on refresh error; injected sleeper instead of real timers; one rate-limit authority documented/reconciled; ErrContextScope only for real scope errors, other resolver errors keep their kind; shadowed `e` renamed; negative cache for Blocked keys; zero Now validation; unused ScopeChanged/ConfigChanged decided; N1 design table updated |

Cannot be closed by code now, kept as explicit contracts for the wiring work:
safety S4 (issue provenance must come from a ContextResolver-validated read, test
at wiring), NewGetterWithLogOrigins explicit allowlist at wiring, noise shell
(NoiseStep has no consumer until the proposals executor), live GitHub proof of
the empty-repo probe (manual, needs a test account), and the TM1 named test
TestInvariant_4_HostContextRedacted (test-engineer batch).
Each task: follow functional-effect:model; add a regression test per fix that
fails without it; all five task commands pass; commit on its branch with the
agent report format. No safety invariant is weakened by any item.

## Batch 2 follow-up: result (human approved fixing the deferred items)

Merged in order, each followed by the five-task gate (all exit 0, frontend 528/528):
F6 PTY exit-signal race 1db2d46 (Unix-only; Windows PTY still manual); H-A host
writes 5d3ec46; T import transaction d8fb82c; R noise 76b1f2d; H-B context shell
988f298 plus lead resolver-kind fix in confirmed.go; review-found follow-up
(H-B 608e39d: atomic publish answer, ErrContextSuperseded, slot cap 256, closed
ContextRefusal set; lead c8b3cf2: FindReviewComment scope recheck after reading,
archtest dot-import and PushAuthorizer/NewPushTransport). `-race` clean on hosts,
core/hosts, tasks, repos, archtest. Gate logs /tmp/ygg-{HA,T,R,HB,HB2}-main-gate.log.

Review gate on 91b0666..main: safety PASS (no Blocking/Must-fix). fe-reviewer found
one new Must-fix (empty success when a publish raced a scope change): fixed with a
deterministic race test. Re-check by both reviewers on ecd14d6..main: no Blocking
or Must-fix.

Closed (each with a regression test shown to fail without the fix): archtest keeps
host writers out of api/mcp/agents (also dot-import and push names); push credential
bound to the repo path; 400/422 -> ErrWriteRejected, 401/403 -> unauthorized, rest
Unknown; empty-ref proof (200 [] or 409 + empty branches/tags); http only to loopback;
shell interprets every ContextCommand and the core decides no-client/no-token;
ContextRejected in the sealed set with a closed refusal type; deterministic provider
errors not retried; GitLab MR without a diff version is retryable; resolver errors keep
their kind (reader and confirmed writes); failed read keeps its slot; injected sleeper;
rate-limit authority decided in the design; Blocked negative cache (5 s); zero-time
inputs ignored; ScopeChanged/ConfigChanged produced by the shell; ImportIssueDraft reads
scope and dedup in the transaction, scope before dedup; PreviousMode fallback; zero
Now/TaskID refused; noise sample bounded, nested candidates dropped, partial marker.

Still open, none Blocking/Must-fix, none a code gap that can be closed before wiring:
- Wiring contracts: API shows ErrContextSuperseded as "try again" and maps Blocked.Until;
  the real ContextResolver wraps ErrContextScope for not-tracked/not-ready/foreign;
  issue provenance must come from a ContextResolver-validated read (test at wiring);
  NewGetterWithLogOrigins needs an explicit allowlist; wiring uses NewPushTransport
  (Authorize alone checks only the origin); executor treats ErrWriteRejected and
  unauthorized FetchError as definite failures; noise shell consumes NoiseStep.
- Needs a human or another owner: live GitHub proof that an empty repo answers 409 on
  matching-refs (manual checklist, test account); internal/db tx-level
  GetDraftByDedupKey/GetReservedRepo so the in-transaction read holds by construction.
- Deferred Should-fix: per-log transient failure cached as retry_exhausted
  (context_api.go); shell decides "wait does not fit the deadline" (should be a core
  input); log/audit when PreviousMode falls back; DraftBadFacts shown as recoverable;
  NoiseFacts.Truncated now means "partial" (rename); redactContext runs twice
  (harmless); Referer on the log redirect hop (no token).
- Leftover worktrees and branches (worktree-agent-*, t3/*, plus a temporary detached
  worktree in the session scratchpad) are not deleted: that needs the human's say-so.

## Batch 2 test handoff TM1 (2026-11-16)

TM1-T (tasks), TM1-R (repos), TM1-H (hosts) executed the test map. Test files only; every
test shown to fail by controlled mutation and restored exactly (reports list each
mutation). Merged one at a time, five-task gate green after each (frontend 528/528).
- TM1-R: no defect. Three surviving mutants were redundant code, not gaps.
- TM1-H found two real defects, both fixed in `internal/hosts/context_redact.go`
  (1918c01): (1) invariant 4: invalid UTF-8 was stripped after redaction, so a credential
  split by an invalid byte was reassembled; now stripped first. (2) boundContext halved
  every Unavailable label ("external checks" -> "externa"); now cuts only labels over 256 B.
- TM1-T found one defect, fixed in `internal/tasks/draft.go` (fd8c144, 2ce20e9): a replay
  of an import after the repo left scope returned the original draft; scope now precedes
  the replay and the digest check.
- Review gate on 3ab08d2..main: safety-reviewer PASS, fe-reviewer no Blocking/Must-fix.
  Should-fix follow-ups (test-only) dispatched: model property reaches replay-after-untrack
  and changed-payload-out-of-scope; redaction generator splits the token with an invalid
  byte; label-preservation property; no wall-clock branches in repos contracts; no fixed
  sleeps for negative assertions. Done and merged (8966339), gate green; every new test shown to fail by mutation, no new defect.
- Not done / observations: no ContextService-level random-sequence property; github/gitlab
  wrappers not run through the contract (import cycle); "tokens never reach child env" and
  audit clauses have no hosts test (no process, no audit entry there); one failed-jobs log
  over 256 KiB ends the read without listing the remaining jobs (design doc says "labelled
  partial": decide); MR.State and other closed-set fields not redacted (built from a closed
  set today); core.checkID accepts \v, \f and NUL; title/identity checks reject only C0 and
  DEL (C1/bidi pass); replay decision still half in the shell (PlanDraft used only to
  extract refusals; consider a Replay fact in DraftFacts); `TestUnknownCommandFailsTheTask`
  and settings.test.tsx "saves maxAgents" are timing-sensitive under machine load.

## Human decisions before Batch 5 (2026-11-16)

- Failed-jobs log over 256 KiB: label the job partial AND keep listing the remaining failed
  jobs ("omitted: size limit"), as the design says. Owner: Batch 5 task W (or a hosts follow-up).
- Replay decision moves into the core: add a Replay fact to `DraftFacts` so `core/tasks` decides
  replay / new / refuse; the shell only supplies facts. Small serial task before Batch 5 wiring.

## Batch 3 breakdown (2026-11-16, 2 agents at once; deviation: N split into four tasks of ~40 turns)

Base: main 2b73dbf. No two parallel tasks own the same package.
Order: A = N1 + N2; B = N3a + P; C = N3b + Q; then test-engineer passes and the review gate.

| Task | Agent | Owns | PRD |
|---|---|---|---|
| N1 safe FS | platform-windows | internal/platform (SafeInitFS, IgnoreWriter, platformtest fakes+contracts) | NR-01, WS-09 |
| N2 git | go-backend | internal/git (RepoInitializer, OriginManager, go-git PublishTransport, gittest fakes+contracts) | NR-01/03/04 |
| N3a core | go-backend | internal/core/newrepos (pure Step, properties) | NR-01-04 |
| P proposals | go-backend | internal/proposals, internal/core/proposals (executors, Enabled) | AC-02/03/04, WS-09, NR-01/03/04, RV-02, MCP-05 |
| N3b shell | go-backend | internal/newrepos (worker, recovery, reconcile) | NR-01-04 |
| Q notify | go-backend | internal/notify, internal/core/notify | UI-06, D11 |

Backend agents run Go checks only (go build/vet, golangci-lint on their packages, CGO_ENABLED=0 go test);
the lead runs the full gate with Node 22 in the scratchpad.

### Batch 3 round A (merged)
- N1 platform safe FS + IgnoreWriter merged (--no-ff). Windows code compiles (GOOS=windows vet) but has never run; manual
  checklist from the report: run `go test ./internal/platform/... -run "SafeInit|IgnoreWriter|Junction|ShortName"` on Windows,
  hold `.gitignore` open to see ErrLocked, `mklink /J` a parent, `\\wsl$` and subst parents refused, >260-char parent works.
  Risks: Unix refuses every symlinked ancestor of the project parent (a symlinked home is refused); a OneDrive placeholder
  `.gitignore` is refused; ReFS file-index uniqueness; Unix cannot fully exclude a concurrent editor (advisory flock + readback).
- N2 git init/origin/in-process publish transport merged. go-git v5.19.3 already pinned; transitive licences checked by hand
  (Apache/BSD/MIT; securejoin BSD-3 AND MPL-2.0 ok), `task license` not run (go-licenses not installed): run at the gate.
  Open: no TreeHash helper (shell must use receipts); no test that the missing-author refusal leaves the index untouched.
- After each: golangci-lint 0 issues, platform/git/archtest tests green, task lint and typecheck green.
- Round B (N3a core Step, P1 ignore/comment/pull executors) dispatched; P split into P1 (this) and P2 (InitRepo/PublishRepo) because
  five executors do not fit 40 turns. Round C = N3b + P2, round D = Q + test-engineer.

### Batch 3 rounds B and C (merged)
- N3a core/newrepos Step merged. Additive frozen-type changes: `StageFailed.Reason`, `Stopped.Attempt` (retry needs a strictly
  greater attempt). Lead fixed db to persist `Stopped.Attempt` (9b13054, mutation-checked).
- P1 proposals IgnoreEntries/ReviewComment/PullFF merged; P2 InitRepo/PublishRepo merged. Agents cannot create InitRepo/PublishRepo
  proposals (P2 choice). **Batch 5 decision:** MCP-05 lists `propose_publish`, so W must allow agent-created PublishRepo
  (never InitRepo), still human-confirmed.
- N3b new-repo shell merged (+ follow-up: Retry, audit through audit.Prepare after the archtest caught it, waitSettled flake,
  Untracked-while-running test; `-race -count=3` green).
- Wiring contracts for Batch 5 (from the reports):
  - call `proposals.Service.OperationChanged(ctx, op)` on every lifecycle state change; nothing calls it yet;
  - `newrepos.TryLock(mainlock.Registry, poll)` adapts the registry to MainLock; `PreparePublish(op, req, proposalID)` before
    `ConfirmPublish` (pending publish spec is in memory: restart means preview again); effect rows need real proposal rows first;
  - ConfirmInit/ConfirmPublish get `InitSpec.Hash()`/`PublishSpec.Hash()`, not the proposal action hash;
  - the lifecycle port has no Reject: rejecting an init proposal leaves the operation AwaitingConfirm (wire Service.Reject);
  - the proposal hand-off effect stays at `intent` (db allows receipts only for comment effects): small db change if wanted;
  - proposals wiring: wrap the hosts sentinel in `proposals.ErrOutcomeUnknown`; map pull refusals to ErrPullHeadChanged/ErrPullBusy;
    comment errors unclassified are treated as unknown; real GitHub 404 on lookup is reported unknown, so Retry of create_remote
    stays refused until a human acts (fake host says absent);
  - PullFF is creatable by agents (confirmation still required): decide at MCP registration;
  - ignore/pull effects use audit stage entries, not effect rows; Windows platform code is untested on Windows.

### Batch 3 interruption (usage limit, 2026-11-16 evening; resumed 2026-11-17)
- Main at the last merge of P2 plus log commits. N1, N2, N3a, N3b(+Retry), P1, P2 merged and green.
- In flight when the limit hit: **Q** (worktree-agent-a026d09494e536e46: core transition table + producer committed, tests NOT written,
  was fixing the ID generator); **TE-1** (newrepos mutation evidence and gap tests; died before creating a worktree, nothing to resume
  except re-dispatching it).
- Remaining for Batch 3: finish Q, merge, TE-1 (re-dispatch), full gate (generate, lint, typecheck, test, test:web, task license),
  safety-reviewer + fe-reviewer on 2b73dbf..main, fix Blocking/Must-fix, close Batch 3.

### Batch 3 round D (Q, TE-1, reviews) 2026-11-17
- Q notify producer merged; TE-1 new-repo tests and ~40 killed mutants merged (defect found: COM/LPT superscript device names accepted
  by CheckLeaf; fixed by lead in 36981a3 with a test shown to fail without it).
- Review gate on 2b73dbf..main: safety-reviewer PASS; fe-reviewer PASS (no Blocking/Must-fix).
  Lead fixed in 36981a3 (each mutation-checked where noted): publish credential bound to the repo path, not only the host
  (same-host redirect to another repo refused: test shown to fail without it); `user.useConfigOnly=true` for HasAuthor and the first
  commit (no auto-detected machine identity); `commit.gpgsign=false`.
  fe Should-fix 1-4 (executor Retry path, untrack settles as Unknown, stuck worker, StopConflict retry mismatch) dispatched as BF3.
  Still open as Consider: re-verify folder identity after each git stage (git works by path, small local race); `.git/HEAD` read
  follows a symlink (read only); HighWater grows per verification run (notify); persistHighWater does not treat ErrConflict as no-op;
  CommentHostContract runs only against the fake; no fuzz of ValidateReceipt beyond seeds; platform Windows-specific tests not added.
- Gate on main before BF3: generate (no diff), lint, typecheck, `go test ./...`, test:web 528/528 all green.
  `task license` cannot run here: go-licenses v2.0.0 cannot parse the `tool` directive in go.mod (and fails on stdlib module info);
  go.mod did not change in Batch 3 (go-git was already a pinned indirect), and N2 checked the transitive licences by hand. Verify in CI.

### Batch 3 closed (2026-11-17)
- BF3 merged (executor Retry path with a real-service contract run, untrack settles Unknown, failed worker feeds Recovered, one
  `StopReason.Retryable()` rule, Retry no longer holds the service lock: it self-deadlocked on publish). Test race in TestInitHappyPath
  fixed by the lead. `-race` clean: newrepos, notify, git, platform, core/newrepos, core/notify. Not done (accepted, noted for wiring):
  Uncertain-then-retry in one attempt; a late successful stage result after untrack is dropped (effect stays Unknown, no receipt).
- Batch 4 plan: round 1 A1 (core/actions) + S (core/standup + internal/standup); round 2 A2 (internal/actions shell) + Replay-fact
  task (internal/tasks + core/tasks, decided by the human); round 3 TM2 test-engineer (actions, standup). Reviews after Batches 4+5.

### Batch 4 round 1-2 (A1, S, A2 merged; T2, P3 in flight) 2026-11-17
- A1 core/actions (total catalogue, E6 property over every reason combination, ~20 mutations killed), S standup core+shell,
  A2 actions shell merged, each gated (lint 0 issues, tests green).
- Deviations/decisions: commit-grouping, ignore and MR-review offerings submit Ask tasks that propose through the normal MCP/proposal
  path (the frozen core result allows only a TaskID for them); only PullAndCheck creates a proposal from the shell. PullFF `Verify` is
  always false (Request has no field for it): **open for Batch 5** (add the option to the DTO + core, or drop optional verification
  from the UI with a human OK). Core `IDs.Task` is a placeholder (tasks generates the real ID).
- **E6 gap until fixed:** the unreadable-repo action needs `FolderDrafts.SaveWaitingDraft` (nothing implements it; without it the
  request is refused with a plain message). Owner: task T3 in internal/tasks (after T2), then wiring.
- Adapters nobody implements yet (needed by Batch 5 wiring in cmd/ygg, composition root): actions.ScopeReader (Facts/StillTracked/
  NoiseFacts/Target/ReposOf/PullTarget), ContextReader, ProposalCreator (P3 provides proposals.Service methods; cmd adapts),
  FolderDrafts (T3), standup MRSource (cached host MRs) and BuildSource (db builds in [from,to)): task S2 for the last two.
- Standup: refresh publishes nothing (no standup message on Phase6Hub); polish publishes PublishAction. API adapter for
  api.ActionService (Catalogue, Preview, Execute, Get) is not written: W.
- Observations not yet fixed: core.checkID accepts \v, \f, NUL; title/identity checks reject only C0 and DEL (C1/bidi pass).

## Batch 4: usage limit interruption and resume

P3 (proposals `CreateForRequest`/`FindForRequest`) and T3 (`SaveWaitingDraft` for unreadable repos) were stopped by the usage limit. Their work was saved in their worktrees, and both were resumed through SendMessage after the reset. Still to do in Batch 4: merge P3 and T3 one at a time, S2 (real standup MR and build sources), TM2, the Batch 4 gate.
