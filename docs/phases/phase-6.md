# Phase 6 — Workspace actions, notifications and new repos

Status: **F0–F5 and Batch 1 complete; Batch 2 resuming preserved feature work**.
Target: `v0.7.0`. Baseline inspected: `main` at `2f96629`, Phase 5 tagged `v0.6.0`.
This document does not authorize a tag, push, branch/file deletion, live host mutation or real agent CLI run.

## Scope and evidence read

Follow `.claude/CLAUDE.md`, `docs/AGENTS.md` in full, and the Phase 6 checklist in `docs/PLAN.md`.
Read PRD sections 6.1–6.2 (WS-09, GH-05/06), 6.8 (AC-01–06), 6.10 (AU-02), 6.14 (NR-01–04), 6.15 (UI-03/06/07), the entire decision log D1–D17, and the supporting agent, verification, proposal, MCP, toolchain and safety requirements. Read `docs/spikes.md` B1–B5 (including the B5 empty-settings-source and strict MCP isolation decisions); existing git-host, git-action and proposal designs inform the contracts below.

Code inspected and reused:

- `internal/hosts/{client,getter,service}.go`, host clients and `internal/core/hosts/types.go`: read-only polling, header-only tokens, ETags, rate limits and instance resolution already exist. `HostClient` has only `RepoState` and `Inbox`; `Pipeline` lacks the run/job identity needed to fetch logs. Do not derive identity by trusting arbitrary browser URLs.
- `internal/repos/{actions,status_service}.go`, `internal/git/status.go`, `internal/core/repos`: ff-only action batches, shared main-checkout locks, status refresh and bounded file lists already exist. Noise detection must handle truncated status without claiming a complete scan.
- `internal/tasks/{service,admit,apply,config}.go`, `internal/core/tasks`, API task/board contracts: reuse admission, trust, queue, Ask integrity and task relationships. Creating a message starts work; importing an issue needs a separate **draft-only** path.
- `internal/core/proposals/{action,action_validate,types}.go`, `internal/proposals/{service,exec,exec_install}.go`: confirmation hashes, immutable actions, audit, reconciliation and install execution exist. `publish` is an enum value but has no executable action; `AgentFile` only writes protected agent files and cannot be reused for `.gitignore`.
- `internal/api/{server,topic,tasks,tasks_service,proposals,types_board,types_hosts}.go`, `cmd/ygg/{tasks,hosts,proposals,app}.go`, sqlc queries/migrations and `tygo.yaml`: use existing wiring, hub and generation conventions. Latest migration is 00006; 00003 and 00005 have Go migrations plus sqlc schema mirrors.
- `web/src/widgets/{needs-attention,ci-failures,merge-requests}`, widget registry, repo rows, command registry, proposal UI and board/composer: attention reasons include unreadable repo, conflicts, divergence, failed CI/build, behind and dirty state. Cover all of them, not just the five named actions.
- `web/src/components/{LiveSync,NotificationsMenu}.tsx`, `web/src/stores/notifications.ts`: task-status in-app notifications exist, but browser delivery, verification notifications and reconnect deduplication need work. `internal/notify` is a placeholder. Standup is absent from the eleven-widget registry.
- `Taskfile.yml`: generation is sqlc + tygo; `task test:e2e` currently only prints a placeholder. It cannot be counted as browser-test evidence until replaced with a real runner.

Phase 3's unchecked OpenCode item is a carried baseline gap, not an implicit Phase 6 deliverable. Test new flows with the mock adapter; report reference-adapter limitations at close.

## Product and safety decisions to settle in design

These are proposed contract choices for review, not changes to the PRD or exceptions to safety rules. F0 must resolve them before F1 commits public interfaces. If a choice would weaken an invariant, stop and ask the human.

1. **Attention coverage:** expose typed actions from one server catalogue, revalidated at execution. Failed CI → Ask diagnosis with bounded log attachment; optional fix → separate Code/worktree action. Failed local build → Ask diagnosis with build log. Dirty → draft commit proposals. Behind → confirmed ff-only pull and optional fast verification using existing services. Conflicts/divergence/unreadable repo → safe Ask diagnosis using only available tracked metadata, with an explanation when files cannot be read. No automatic reset, merge repair or removal. A meaningful diagnosis action remains available when a token or supported host capability is absent; explain which context is unavailable.
2. **MR review:** fetch the MR diff and immutable head identity, draft review comments in Ask mode, then create hash-bound comment proposals. Confirmation alone may post; changed MR head requires a fresh proposal. Implement host capabilities explicitly; unsupported hosts explain their limitation and still permit a local review draft. Review text never becomes an instruction that bypasses guards.
3. **Noise:** conservatively group likely generated/dependency directories from untracked status, with an explicit threshold and bounded sampling agreed in F0. Offer exact `.gitignore` additions as a general, narrowly scoped file-change proposal with expected-content hash, preserving existing text/CRLF. No automatic ignore edits, deletion or arbitrary write primitive.
4. **New repo:** a user chooses a new child directory of a tracked project; validate the canonical parent and leaf, reserve a repo identity attached to that project, then confirm initialization, README/ignore contents and first commit. Recheck target absence before every write; never initialize over an existing folder/repo or clean up by deleting files. Model partial initialization and explicit resume. Finalize tracking before making it visible to agents/MCP.
5. **Before a remote exists:** NR-02 must reuse the explicit current-branch opt-in/warning, shared lock, trust scan, write-mode switch and adapter enforcement gates (AG-08/SF-06/SF-09). No silent downgrade from Code to direct editing and no widening for a limited-enforcement adapter. A first prompt waits for successful initialization and explicit task submission. Document how this fits the existing direct-editing policy; if it needs a new safety exception, stop for a decision rather than implement one.
6. **Publish:** GitHub user/org, private by default, explicit visibility, destination, branch and expected HEAD in the confirmation. Create remote repo → add credential-free origin → push, with durable progress. Never overwrite an existing origin, adopt an unrelated remote repo on retry, or delete a remote/local repo to roll back. Unknown network outcomes require read-only reconciliation before a human retry. Use server HTTP credentials only in headers; authenticated git transport must not pass tokens to a child process (invariant 3). F0 must choose an enforceable transport or report this as a blocker before implementation; an askpass/token environment workaround is prohibited.
7. **Notifications:** only task title and status in visible text, including verification pass/fail. Internal IDs/revisions may support navigation/deduplication but no prompts, paths, diffs or logs enter notification payloads. Browser permission is requested from a user gesture; delivery only while backgrounded. Denied/unsupported browsers retain in-app delivery. No external channels (D11).
8. **Standup:** bounded project/date-range aggregation of commits, MRs and builds; deterministic text, copy, then optional user-triggered Ask task to polish. No scheduled jobs or automatic model calls. Partial/unavailable sources are labelled.

## Foundations — serial, lead-owned, committed to main before fan-out

All F tasks are performed by the lead, sequentially. No implementation subagent worktree is created until **all** foundations are committed to `main`, generation is current and the five required checks pass. Record the contract commit SHA here before dispatch. If a shared contract later needs a change, pause dependent fan-out, make a lead-owned contract commit on main, then rebase/update briefs; agents must not invent competing interfaces.

| Task | Owner / PRD IDs | Owned paths | Interfaces and definition of done | Phase 6 contribution |
|---|---|---|---|---|
| **F0 Design gate** | lead; all Phase 6 IDs | `docs/design/phase-6-*.md`, this plan | Run `/functional-effect:design` for each feature below **before code**, using the installed design/model instructions. Each design records closed states/inputs/commands and pure `Step`, transition table, call graph, cardinality, domain/retry/escape-hatch/die failures, boundary parsers, capabilities/fakes/contracts, invariants and named properties. New-repo/publish and comment posting require explicit crash/unknown-outcome reconciliation. Show designs and obtain their required approval before F1. Resolve the safety/public-interface questions above; no new `fe:allow`. | All checkboxes; necessary foundation for exit E6. |
| **F1 Shared core and capabilities** | lead; GH-05/06, AC-01–06, WS-09, UI-06, AU-02, NR-01–04 | `internal/core/**` shared types and new domain skeletons; consumer interface declarations in `internal/{hosts,actions,notify,standup,newrepos,proposals,tasks,git,platform}/`; capability fake/stub declarations; `.functional-effect.json` | Commit state/input/command types and `Step(state,input)` signatures for every design. Extend existing host/proposal/task types rather than create parallel entities. Add narrowly scoped host reads/writes, draft-task creation, initialization/publish progress, ignore-edit/comment/publish/init/pull proposal contracts, notification revisions and standup inputs. `agents.Event` remains normalized; change `internal/agents/event.go` only if design proves necessary, regenerate with F3. Consumer-local interfaces; no service implementations yet. Skeletons compile and return unsupported rather than accidentally execute. Architecture tests clean. | Shared contracts for every checklist item and E6. |
| **F2 Persistence** | lead; AC-02/05, WS-09, UI-06, NR-01–04, SF-05 | `internal/db/**` | New `00007_phase6.sql` (never edit applied migrations), queries, store adapters and sqlc output. Persist draft issue provenance/dedup key, proposal variants/progress, new-repo reservation and publish reconciliation; notification revision/delivery records as required by approved design. Reuse tasks/repos/proposals, not new top-level entities. State and audit commit together through `Store.Commit`; optimistic versions. Test upgrade from version 6 with existing data, fresh database, rerun and FK/constraint failures. No need to persist raw CI logs solely to support actions. | Reliable drafts, mutations and notifications; E6 evidence. |
| **F3 API, events, generated contracts and checks** | lead; all Phase 6 IDs | `internal/api/**` contract/service declarations, `topic.go`, hub signatures; `tygo.yaml`, `web/src/api/gen/**`; `Taskfile.yml`, new browser-runner configuration and package lock/config if needed | Freeze DTOs for action catalogue/preview/execute, host logs/issues/MR context, draft tasks, noise findings, notifications, standup, new-repo preview/progress and publish. IDs resolve tracked scope on server, no arbitrary host URL or filesystem path inputs. Define event revision/gap behavior. Generate SQL/TS from source; no `any` mappings hiding contract breaks. Establish real Playwright task/config if used for exit evidence, with compatible dependencies. Run `task generate`, `task lint`, `task typecheck`, `task test`, `task test:web`; commit source + generated output on main. | Enables UI/API integration, all checklist items, E6. |

Design documents **produced by F0 and approved by the human**:

- [Host context](../design/phase-6-host-context.md): GH-05/06, AC-01/02/05; bounded reads, failures and credential boundaries.
- [Workspace actions](../design/phase-6-actions.md): AC-01–06; catalogue, attachments, draft issue import, mode transitions and pull/verify.
- [Noise proposals](../design/phase-6-noise.md): WS-09; sampling/classification and stale-safe ignore edit.
- [New repo and publish](../design/phase-6-newrepos.md): NR-01–04; reservation, confirmation, partial effects, transport and reconciliation.
- [Notifications](../design/phase-6-notifications.md): UI-06/D11; producer ordering, revisions, reconnect deduplication, browser reducer.
- [Standup](../design/phase-6-standup.md): AU-02/UI-03; aggregation, partial sources and polish submission.
- [UI workflows](../design/phase-6-ui.md): AC-06/UI-07 and dialogs for the above; pure reducers, loading/error/empty/denied states.

## Dispatch and ownership rules

Each row below is one agent invocation; suffixes name assignments, not new agent definitions. Use the repository's named agent roles and settings; never silently substitute an unavailable model/provider. Implementation uses isolated worktrees branched from committed `main`. **At most three implementation agents at once.** Each batch finishes, merges and passes its review gate before the next starts.

Package ownership includes tests and helpers. Even different files in the same Go package cannot be worked on concurrently. To make this mechanically checkable, batches below partition whole package trees; one frontend agent owns **all `web/**`** in a batch. No lead edits to an agent-owned package during its run. Shared core/API/DB/generated contracts remain frozen; changing them is a separate serial lead step. Test-engineer takes over a package only after its implementation owner has merged and released it.

Every brief includes PRD IDs, the approved design paths above, owned/forbidden paths, main contract SHA and interfaces, “follow the functional-effect:model skill (docs/AGENTS.md 5.11)”, the checks below, and “commit on your branch and end with the report format from your agent file”. No agent edits bookkeeping/shared contracts to resolve an integration error unilaterally.

**Common DoD (every implementation row):** stated behavior and IDs implemented; boundary validation; state/audit transaction; cancellation ownership; plain-language errors; relevant tests using temp repos/recorded HTTP/mock agents; conventional commits with PRD IDs. Run `task generate`, `task lint`, `task typecheck`, `task test`, `task test:web`; generated files must stay consistent with frozen contracts. Additionally run relevant race/Windows tests. Record environmental blockers as failures to verify, not passes. A test exposing a bug is reported, fixed by the implementation owner, then rerun before closure.

### Batch 1 — characterize and extract before adding features (three agents)

These are explicitly separate extraction tasks, **not hidden inside feature tasks**. Use `/functional-effect:extract` on the named flow, characterization tests first. Existing pure logic is reused; extract only remaining mixed decisions, not whole-package rewrites. F0 designs precede these tasks too.

| Task | Owner / PRD IDs | Owned paths | Interfaces to use / specific DoD | Exit contribution |
|---|---|---|---|---|
| **XH Host context resolution** | go-backend-H; GH-05/06, AC-01/02/05 | `internal/hosts/**`, `internal/core/hosts/**` | Existing `HostClient`, `Getter`, `core/hosts` inputs; F1 capability declarations. Characterize instance/project resolution and fetch-failure mapping in `service.go`; move remaining decisions to pure core with facts supplied by shell. Preserve polling, token handling and rate limits. | Enables reliable context for E6. |
| **XT Task submission** | go-backend-T; AC-01/03/05, NR-02 | `internal/tasks/**`, `internal/core/tasks/**` | `Service.Message`, `scopeRepos`, existing `core/tasks` parser and `core/guard` admission. Characterize submission/scope/mode decisions before moving remaining pure decisions into core; trust scan, store, adapter and queue stay shell effects. No draft feature or changed behavior in this task. | Safe task entry for E6 and new-repo flow. |
| **XR Repo facts** | go-backend-R; WS-09, AC-04 | `internal/repos/**`, `internal/core/repos/**` | Existing status-reader/refresher and git-action step. Characterize status-to-facts/truncation and pull refusal mapping, extract mixed decisions only. Preserve ff-only behavior, locking and status refresh performance. | Noise/pull context and fallback diagnoses for E6. |

After merging XH/XT/XR, release their packages to Batch 2 owners. Do not concurrently test-map these same packages while Batch 2 edits them.

### Batch 2 — host context, task drafts and workspace facts (three agents)

| Task | Owner / PRD IDs | Owned paths | Interfaces to use / specific DoD | Exit contribution |
|---|---|---|---|---|
| **H Host reads and confirmed-write capabilities** | go-backend-H; GH-05/06, AC-01/02/05, NR-04 | `internal/hosts/**`, `internal/core/hosts/**` | F1 host context/read/write capabilities, `Getter`, redactor, existing instance resolver. GitLab/GitHub failed-job logs with immutable run/job IDs, issues and MR diff/head; pagination and byte/time bounds; plain unsupported/no-token/expired-token/rate-limit states; redact attachments before persistence/agent use. Confirmed-write adapters expose review-comment and GitHub create/lookup only to proposal executor; never endpoints callable directly by agents. Authenticated transport must follow F0 decision. Recorded HTTP tests, no real hosts; keep Gitea capability limitations honest. | GH boxes; CI/MR/issue context for E6; publish capability. |
| **T Draft and direct-edit admission plumbing** | go-backend-T; AC-05, NR-02, AG-08, SF-06/07/09 | `internal/tasks/**`, `internal/core/tasks/**` | F1 draft creation/start contracts, F2 queries, existing admission and `Message`. Import creates one ordinary `draft` task linked to tracked repo/project and source issue, no turn/process/worktree until submitted; repeated import uses agreed dedup key. Explicit start goes through ordinary queue/admission. NR-02 reuses opt-in and all existing restrictions; test trusted/untrusted/limited-enforcement and no-remote transitions. | Issue-to-board action for E6; NR-02. |
| **R Noise and standup inputs** | go-backend-R; WS-09, AU-02, AC-04 | `internal/repos/**`, `internal/core/repos/**` | F1 bounded repo-facts capability, existing status/git action services. Return deterministic candidate ignore entries with threshold/sample/truncation evidence; never write `.gitignore`. Expose bounded commit and pull outcome facts needed by standup/actions; no new automatic pull path. Temp repos with large untracked sets, CRLF, unusual paths and no false complete-scan claim. | Noise checkbox and action context for E6; standup facts. |

### Batch 3 — new-repo services, proposal execution and notifications (three agents)

| Task | Owner / PRD IDs | Owned paths | Interfaces to use / specific DoD | Exit contribution |
|---|---|---|---|---|
| **N New-repo lifecycle and safe git support** | go-backend-N; NR-01–04 | `internal/newrepos/**`, `internal/core/newrepos/**`, `internal/git/**`, `internal/platform/**` | F1 lifecycle and git/platform ports, F2 progress store, host transport from H, `platform.Start`, clean env and mainlock. Preview/reserve/initialize/finalize/publish stages callable only by confirmed executor; no automatic first task. Missing author, name collisions, untracked project, path escapes/reparse races, existing origin, partial init/create/push and restart have explicit outcomes. Implement absent-origin add/read and safe push helpers without credentials in process/env/config/URLs. OS behavior only in platform, compile darwin/linux. | All new-repo checkboxes; safe context for new repo attention items. |
| **P Proposal extensions** | go-backend-P; AC-02/03/04, WS-09, NR-01/03/04, RV-02, MCP-05 | `internal/proposals/**`, `internal/core/proposals/**` | Frozen F1 action types/parsers/Describe/hash, existing `Service`, H write ports, N lifecycle port (fake until N merges), existing install/build and git-action ports. Execute ignore-file edit, review comment, new-repo init, publish and confirmed pull only after matching confirmation. Grouped commits use existing exact-path/hash-bound commits, not stage-all. Install stays toolchain/main-only. Revalidate scope and stale content/HEAD/MR at execution, audit all stages, reconcile ambiguous remote results without blind retry. Property and executor tests cover rejection, stale hash and duplicate confirm. | Mutation actions underpin E6; NR/WS-09 safety. |
| **Q Notifications** | go-backend-Q; UI-06, D11 | `internal/notify/**`, `internal/core/notify/**` | F1 notification inputs/events and F2 delivery store; existing task/verification publisher ports, time/ID as inputs. Normalize Needs you, verification result and finished task transitions; bounded history and revision dedup/gap semantics per design. Task title/status only; no raw event/log forwarding. Stop workers on cancellation, persist state/audit as designed. | Notification checkbox; status of tasks launched from attention items. |

N owns platform in this batch because initialization path safety/credential-free transport may need Windows support; no parallel platform-windows agent may edit that package. If specialist work is necessary, split N into sequential invocations with an explicit ownership handoff, not a fourth parallel agent.

### Batch 4 — action orchestration, standup and backend test maps (three agents)

| Task | Owner / PRD IDs | Owned paths | Interfaces to use / specific DoD | Exit contribution |
|---|---|---|---|---|
| **A Workspace action orchestration** | go-backend-A; AC-01–06, WS-09 | `internal/actions/**`, `internal/core/actions/**` | F1 catalogue/request/result contracts; H logs/issues/MR ports, T draft/start, R facts, P proposal creation, existing verify/build APIs. Pure catalogue covers every attention reason; execute rechecks current tracked scope and capability. Attach bounded source-labelled context, not host-controlled commands. Diagnose remains Ask; Fix explicitly requests Code/worktree. Review produces proposals; commit grouping uses current diff hashes. Pull uses P confirmation and existing ff-only service; never fallback merge. All action paths auditable and duplicate clicks handled per design. | Primary E6 implementation; AC-01–06 checkbox. |
| **S Standup** | go-backend-S; AU-02, UI-03 | `internal/standup/**`, `internal/core/standup/**` | F1 aggregation ports; R commits, H cached MR reads, existing builds history. Deterministic bounded date-range summary, source labels and partial errors; explicit Ask polish through T with source text; copy is UI-only. No scheduler/network/model call hidden in a summary refresh. | Standup checkbox; reuse of workspace→task path. |
| **TM1 Landed backend test maps** | test-engineer; GH-05/06, WS-09, NR-01–04, UI-06, AC-02/05, SF-* | Tests/helpers/fixtures only in package trees from Batches 1–3: `internal/{hosts,tasks,repos,newrepos,git,platform,proposals,notify}/**`, corresponding `internal/core/{hosts,tasks,repos,newrepos,proposals,notify}/**`; `fixtures/demo-gen/**` | Lead derives map **after each feature merges**: invariants→properties, parsers→round-trip/accepted⇒valid/rejection/fuzz, capabilities→fake+live/recorded contract suites. Show each test can fail by a controlled mutation/negative control, restore it before commit. Fake remote creation/comment/push with crash points, draft has zero agent calls, no token in child/env/config/URL/log/audit, no write before confirm. Extend fictional fixtures for attention reasons/large untracked sets. Must not touch A/S or API/web/cmd. | Safety/context evidence for E6 and other checkboxes. |

### Batch 5 — API/wiring, full frontend and remaining domain tests (three agents)

| Task | Owner / PRD IDs | Owned paths | Interfaces to use / specific DoD | Exit contribution |
|---|---|---|---|---|
| **W API, MCP and application wiring** | go-backend-W; all Phase 6 IDs; MCP-05/06 | `internal/api/**`, `cmd/ygg/**`, `internal/mcp/**`, `internal/core/mcp/**` | Frozen F1/F3 DTOs/topics and service ports; wire A/S/N/P/Q plus existing task/host/build/repo services, subscribe notifications to task and verification results, shutdown before store close. Host/Origin/session on all mutation surfaces; tracked-ID lookups, bounded bodies/errors. Register `propose_publish` and review-comment proposal tool only after executor is available; tools can propose, never confirm/post/publish. Do not implement Phase 7 `propose_subtasks`. Test API routes and MCP scope with fakes. | Connect every checkbox end to end; E6 API support. |
| **U UI integration** | frontend; AC-01–06, WS-09, UI-03/06/07, AU-02, NR-01–04 | `web/**` except frozen generated files and foundation-owned package/runner config | Generated F3 DTOs, TanStack Query, shared command registry, existing composer/board/proposal panels. Shared feature module supplies actions to widget rows, repo rows and palette; no widget imports another widget. Cover Needs attention including failure fallbacks, CI, MR review/comment confirmation, grouped commits, pull result, issue draft import/start. Standup widget + copy/polish; new-repo/init/direct-edit warnings/install/publish confirmations; no-remote guidance. Browser notification reducer handles user permission gesture, background delivery, dedup and denied/unsupported fallback; notification server history through Query, UI-only read/permission state in Zustand. Empty states for zero projects/repos, no origin/token/issues/MRs/builds/tasks/standup, loading and errors distinct. Keyboard/Radix, dark and light theme browser inspection. No live hosts/models. | Visible E6 plus all remaining checklist UI. |
| **TM2 Action and standup test maps** | test-engineer; AC-01–06, AU-02, SF-* | Tests/helpers only in `internal/{actions,standup}/**`, `internal/core/{actions,standup}/**` | Post-merge lead maps for A/S, same property/boundary/contracts and failure-demonstration standard as TM1. Every attention reason maps to a meaningful action; stale/untracked scope never launches; Ask→Code requires explicit request; issue import remains draft; partial standup does not become fabricated content. No edits to API/cmd/MCP/web. | Direct E6 catalogue/orchestration evidence. |

### Batch 6 — integrated verification (one agent; no parallel implementation)

| Task | Owner / PRD IDs | Owned paths | Interfaces to use / specific DoD | Exit contribution |
|---|---|---|---|---|
| **E End-to-end and remaining test maps** | test-engineer; all Phase 6 IDs and invariants 1–11 | Tests/fixtures/helpers in `cmd/ygg/**`, `internal/{api,mcp}/**`, `web/**`; `scripts/smoke.ps1`, browser tests in `web/e2e/**`; no production edits | Derive W/U maps after merge, reuse fixture generator and fake hosts/mock adapter. Add real executable Playwright cases through F3 runner and built-binary Windows smoke coverage. Table-driven `TestPhase6NeedsAttentionActions` covers every current reason plus combinations: row + repo + palette dispatch, expected mode/context/proposals, usable fallback when unavailable. Mock agent diagnosis→explicit fix→verify→confirm proposals; issue stays draft until start; ignore edit stale refusal/CRLF; init→direct-edit admission→confirmed install→private publish against fake host with restart/duplicate cases. Notification privacy, verification pass/fail, background-only browser delivery and reconnect dedup; zero/few repo empty states and standup copy/polish. Run required checks, real `task test:e2e`, `task smoke` on Windows, `task test:race`, `task build`, `task license`, `task perf` as applicable; report skips. | Final E6 evidence and all checklist acceptance. |

## Integration, reviews and bookkeeping

Merge one branch at a time: read report and diff, `git merge --no-ff <branch>` into main, then `task generate`, `task lint`, `task typecheck`, `task test`, `task test:web`. Fix failures before the next merge. Contract conflicts are resolved by the lead, never parallel agents. Do not mark a checklist item done merely because its backend has landed.

After **each** batch, run read-only `safety-reviewer` and `functional-effect:fe-reviewer` against `git diff <batch-start>..main`; fix all Blocking/Must-fix findings and rerun both until clear. Review invocations run after implementers finish; they do not expand the implementation concurrency limit. Missing reviewer/skill availability is a reported gate blocker, never silently omitted. Before close, run `/functional-effect:review` on all touched/new packages and resolve fix-now findings. The lead maintains this plan and its decision log; updates `docs/PLAN.md`, `docs/CHANGELOG.md`, `docs/threat-model.md`, `.env.example` optional host permission documentation, and MCP documentation if changed. Ownership for those files is lead-only, between batches.

For each designed feature, the lead derives its test map as soon as it lands; TM1/TM2/E execute these maps after ownership is free. Add test maps for review fixes too. Test-engineer writes the coverage; the lead does not replace that task with self-written tests. No real agent CLI/model/real host in automated tests. New dependencies require compatible licenses and justification.

## PLAN checklist and exit traceability

**E6 is the sole explicit Phase 6 exit criterion: every “Needs attention” item has at least one agent action.** The other rows below are Phase 6 checklist acceptance, which must also be met before close.

| PLAN requirement | Contributing tasks (foundations apply to every row) | Required evidence |
|---|---|---|
| GH-05/06 logs and issues | XH, H, T, W, U, TM1, E | Recorded GitLab/GitHub reads, bounded logs, scope/auth failures, issue imported as an unstarted draft. |
| AC-01–05 actions | XT, XR, H, T, R, P, A, W, U, TM1/TM2, E | Mode-correct diagnosis/fix, proposed MR comments/commits, confirmed ff-only pull or explanation, issue→draft→explicit start. |
| AC-06 three surfaces | A, W, U, TM2, E | Same typed action from widget row, repo row and searchable keyboard palette; no duplicate action logic. |
| WS-09 noise | XR, R, P, A, W, U, TM1, E | Threshold/sample tests; expected-content hash prevents stale edit; confirmed `.gitignore` changes preserve CRLF and user entries. |
| UI-06/D11 notifications | Q, W, U, TM1, E | Needs-you, verification pass/fail and completion; minimal fields; browser permission/background/dedup/fallback tests. |
| AU-02/UI-03 Standup | R, H, S, W, U, TM2, E | Commit/MR/build aggregation, partial/empty states, copy and explicit Ask polish; registered independent widget. |
| NR-01–04 new repo/direct edits/install/publish | XT, T, H, N, P, W, U, TM1, E | Confirmed initialization, tracked-only task scope, direct-edit gate, main-only clean-env install, private user/org publish with crash reconciliation and token-free child processes. |
| UI-07 empty states | W, U, E | Browser walkthrough of zero projects, one empty project, few repos and unavailable optional hosts; actionable next steps. |
| **E6 every attention item has an agent action** | XH/XT/XR, H/T/R/P, A, W, U, TM1/TM2, E; Q/S/N support surrounding flows | Catalogue and browser matrix for unreadable repo, conflicts, diverged, failed CI, failed local build, behind, dirty and combined reasons. No stranded reason after ranking/limit/filtering, no action that only opens a detail page, no unsafe repair. Retest any newly introduced attention reason. |

## Human verification on Windows at phase close

Automated fixtures do not establish real browser permission/OS notification behavior, hosted permissions, Defender/reparse behavior or actual agent CLI compatibility. Report each item as passed, failed or pending, with no implied live-run authorization from this plan.

1. On a fictional demo workspace, use widget, repo row and `Ctrl+K` for every attention reason (including unreadable/conflicted/diverged repos and failed local builds). Confirm each offers a useful agent action and explains missing context. Diagnose is Ask; Fix creates a worktree only after explicit selection. Check keyboard flow in a dark and light theme.
2. When explicitly authorized in that session, test real GitLab/GitHub failed logs, MR review and issue import with test accounts/repos. Verify tokenless/expired/minimal-permission cases; issue drafts do not start an agent. Preview comments, reject once, then confirm on a disposable MR; stale-head comments refuse. Real agent CLI testing needs separate session agreement.
3. Use a large untracked generated directory: ignore proposal lists exact additions; reject leaves disk unchanged; confirm preserves existing `.gitignore`/CRLF; change the file before confirming and see refusal. No generated file is deleted.
4. With Edge/Chrome, allow notifications from a user gesture, background the tab and trigger Needs you, verification success/failure and completion. Windows toast contains only task title/status; foreground shows in-app only. Exercise denial, reconnect/reload and Windows Do Not Disturb settings; no duplicate toast for replayed events.
5. Standup across several mock repos shows commits, MRs and builds; date range and copy work; missing host data is labelled. Polish starts an explicit Ask task and does not silently consume agent usage.
6. Create a new repo under a tracked project with spaces/non-ASCII/long Windows paths; try an existing directory, outside path and junction escape. Confirm README/ignore/first commit contents before writes. Cancel/restart partial initialization; existing files remain intact, and recovery never deletes them. Missing git author has a useful recovery message.
7. Before adding a remote, try direct editing: warning/opt-in, trust and single-editor lock hold; reject an unsupported limited-enforcement adapter. Confirm dependency install uses the project's toolchain, main checkout and clean env; cancel it and check descendants stop. Existing editor/Explorer survives exiting `ygg`.
8. Only after explicit session authorization, publish a disposable repo to a test GitHub user and org. Check private default, requested visibility/permissions, credential-free origin, first push, already-existing destination/origin refusal, and recovery after an interrupted request. Never expose tokens in a child process, URL/config, log or audit. No automatic remote cleanup.
9. Start with no projects, then an empty tracked project and one repo without remote/token/history. Each relevant page/widget explains the next step without false errors or dead actions. Run `task smoke` and the integrated suite on Windows; process/job cleanup regressions remain covered from Phase 5.

## Approval boundary and execution log

Stop after presenting this plan. No agents have been spawned and no foundations implemented. Plan approval enables F0 design work, not unresolved safety exceptions; F0's own design approval gate precedes contract implementation. Subsequent brief dispatch must cite the committed main foundations SHA. At close report requirement IDs, exit evidence, unmet checks and Windows checklist results; propose `v0.7.0` and stop. Do not tag, push or begin Phase 7 without the human's instruction.

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
