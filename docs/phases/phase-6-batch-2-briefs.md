# Phase 6 Batch 2 dispatch briefs

Prepared for dispatch after Batch 1 final five-command integration and both
review gates pass. The lead supplies the committed main batch-start SHA.
All three owners use Codex in isolated worktrees, with no nested delegation.

## Common requirements

Read `.claude/CLAUDE.md`, `docs/AGENTS.md`, `.claude/agents/go-backend.md`,
Phase 6 in PLAN, the named PRD requirements and decision log, the approved
designs, and the Batch 2 rows in `phase-6.md`. Follow the
functional-effect:model skill (AGENTS 5.11), including its Go, profile and test
references. Reuse the Batch 1 extractions. Implement the approved feature, not
another extraction. New states/inputs/commands use frozen foundation types;
new internal helpers are confined to the owned domain.

Own whole assigned packages including tests. Do not edit DB, API, cmd, MCP,
git/platform, shared core/root, generated files, dependency manifests, Taskfile,
frontend, bookkeeping or another owner's package. If a missing shared port,
query or type prevents correctness, report the exact gap immediately and pause
that dependent part; the lead makes a serial contract commit on main. Never
substitute in-memory deduplication for a required durable transaction.

No new dependencies or functional-effect exceptions without lead review. No
push, tag, PR, file/branch/worktree deletion, global Git config or HOME override.
Use temporary fictional repos, recorded HTTP/fake hosts and mock adapters only;
no real hosts, real agent CLI or model API in automated tests. Do the work
yourself; no subagents. Commit on your branch and end with the report format
from your agent file, including exact requirement coverage, signatures, test
results/skips, dependencies and package ownership handoff.

For every Go compilation/test/race/lint run, acquire the host-local shared
`/tmp/ygg-phase6-validation.lock` with flock; use GOMAXPROCS=2, GOFLAGS=-p=1
and owned RAM scratch. Do not start duplicate checks or compete on SQLite
compilation. Final validation uses `python3 /tmp/ygg-phase6-validate.py B2-H`
(or B2-T / B2-R), which already locks internally: no outer flock. All actual
`task generate`, `task lint`, `task typecheck`, `task test`, `task test:web`
must pass; generated output remains unchanged. Run relevant scoped/race tests
and report Windows-only skips. Use the pinned lint formatter. Lead handles
already-approved disposable cache cleanup when needed; report disk blockers.

## H — Host reads and confirmed-write capabilities

Owner go-backend-H. GH-05/06, AC-01/02/05, NR-04.
Own `internal/hosts/**` and `internal/core/hosts/**` only.
Designs: `phase-6-host-context.md`, `phase-6-actions.md`, `phase-6-newrepos.md`
in `docs/design/`; reuse existing `git-hosts.md`.

Implement frozen `context_ports.go`: ContextClient/Reader, CommentHost,
RepoHost and PushAuthorizer with core ContextKey/payload/state/commands and
ContextStep. Reuse Getter, HostClient, instance resolution, redaction and
injected capabilities. GitLab/GitHub failed-job logs use immutable run/job IDs;
issues and MR diff/head are bounded by declared byte/page/time limits. Enforce
tracked-ready scope before cache lookup and execution, head/attempt fencing,
redaction before attachments/persistence, cancellation ownership and explicit
partial/unsupported/no-token/expired-token/rate-limit outcomes. Gitea and
external checks retain honest capability limits.

Write adapters are available only to confirmed proposal execution; no direct
HTTP/MCP/agent dispatch. Post/create calls are single attempts with unknown
outcome reconciliation; exact marker/body/head or owner/visibility/empty refs
is required before adopting a remote result. No blind retries. Tokens stay in
headers at the correct configured origin, never child processes/URLs/config/
core/logs/audit. Service wiring belongs to W later, not this task.

DoD: transitions, boundary/property/fuzz and recorded HTTP capability contracts,
cache/attempt/scope/credential tests, mandatory checks and relevant race pass.
Contributes host context to E6 and new-repo publication. Manual Windows/live
host permission and posting checks remain in the phase-close checklist.

## T — Draft and direct-edit admission plumbing

Owner go-backend-T. AC-05, NR-02, AG-08, SF-06/07/09.
Own `internal/tasks/**` and `internal/core/tasks/**` only.
Designs: actions/newrepos plus existing tasks and guard designs.

Implement frozen `ports_draft.go` DraftService and RequestSubmitter, core
PlanDraft/DraftSpec/DraftStart, using the existing Store.Commit/admission/Message
path and Batch 1 helpers. First audit that durable RequestID-to-task lookup and
changed-payload rejection are supported by available store ports; report a
missing shared persistence capability before attempting a workaround.

Issue import creates one ordinary Ask draft with scoped provenance/dedup,
no turn/process/worktree/agent call. Resolve tracked-ready project/repo inside
the committing transaction. Duplicate imports return the original draft.
Explicit StartDraft uses ordinary parsing, queue, trust, lock and guard admission;
repo-setup drafts never start. Request-aware submission links request and task
atomically with task/audit creation; duplicate/crash recovery returns the first
result and changed immutable input refuses. No in-memory-only request ledger.

NR-02 requires explicit current_branch opt-in/warning, write modes enabled,
fresh trust, shared single-editor lock and enforcing adapter; ordinary Code
stays worktree mode, and absence of origin never bypasses restrictions.

DoD: draft has zero effects, durable dedup/restart/race tests, admission matrix
for trusted/untrusted/limited-enforcement/no-remote and setup refusal, mandatory
checks and scoped race pass. E6 issue-to-board and NR-02 support; Windows direct
edit/real adapter checks remain manual and separately authorized.

## R — Noise and standup inputs

Owner go-backend-R. WS-09, AU-02, AC-04.
Own `internal/repos/**` and `internal/core/repos/**` only.
Designs: noise/standup/actions plus existing repo-status/git-actions designs.

Implement frozen `ports_noise.go` NoiseSource/HistoryReader and pure NoiseStep,
reusing bounded status facts, refresher/actions and safe git history capability.
No automatic pull path or file writes. Respect threshold/sample/allowlist and
anchored escaped exact patterns from the design. Deterministic candidate order,
true evidence/counts and incomplete labels are required; truncated sampling
never becomes a complete-scan claim. Preserve sampled status performance and
tracked-ready scope. Bound history/date-range/count reads and expose truncation
or unavailability honestly for standup; report any missing shared git port.

DoD: large untracked temporary repos, path/CRLF/truncation/threshold tests,
properties and boundaries, history scope/range/contracts and unchanged ff-only
behavior, mandatory checks and scoped race pass. E6 noise/pull facts and standup
support. Actual ignore writes belong to P; Windows filesystem/manual checks
remain in the phase-close checklist.

## F4 contract and environment handoff

Resume only after the lead confirms F4's full integration and review gates.
The missing T persistence contracts now exist in internal/db/store_submissions.go:

- Tx.GetTaskSubmission(ctx, RequestID) (TaskID, string, error).
- Store.GetTaskSubmission(ctx, RequestID) (TaskID, string, error).
- Tx.LinkTaskSubmission(ctx, RequestID, digest, TaskID) error.
- Tx.GetTrackedReadyRepo(ctx, ProjectID, RepoID) (core.Repo, error).

Task/turn/link/audit must share one Commit. Propagate errors so losing writes
roll back, then reload the winning link outside the failed transaction. Same
link is idempotent, changed digest is ErrDigestMismatch, rebinding is ErrConflict.
Scope lookup also requires a tracked project. Full action Agent/Model replay
persistence is repaired; old incomplete rows fail closed, never redispatch.

The host environment restarted and disk expanded; old temporary Node wrappers
are broken. Prefix commands with PATH=/tmp/ygg-f4-tools/node_modules/.bin:$PATH
for task-owned Node 22 and pnpm 11.4 tooling. The shared validation helper and
lock have been restored at the same paths. Read the resolved main resume SHA
from the lead's invocation; original package ownership remains unchanged.

## F5 handoff for preserved feature work

Wait for the lead's final F5 gate confirmation and resolved main resume SHA.
Fast-forward the existing dirty feature worktree without discarding owned edits;
main changed shared git/platform capabilities and the previously untouched host
port declaration. Report conflicts instead of overwriting work.

H now uses PushAuthorizer.Authorize(ctx, InstanceID, *http.Request) error.
Validate destination scheme/authority/port and credential-free URL against the
configured HTTPS clone origin BEFORE setting only Authorization. Do not rewrite
request destination/method/body or other headers; transport also rejects foreign
redirects. This resolves the reported destination argument gap.

R now uses Git.LogRange(ctx, dir, from, to, max) ([]git.Commit, bool, error).
Dates are author timestamps, UTC [from,to), Date descending/hash ascending;
range <=31 days and max 1..200. Byte/pipe cutoff returns only observed complete
matches with truncated=true, even with zero matches, so label partial honestly.
Count excess also sets truncated. Errors remain unavailable, never complete
empty data. Capture is limited to 1 MiB stdout/16 KiB stderr/five seconds.

T retains immutable import-selected Agent/Model. Matching/omitted start choices
use ordinary admission; differing choices get an explicit refusal. W/U will
show imported drafts separately on the board using the existing status=draft
task-list API and explicit Start, preserving the four AG-12 columns and hiding
repo-setup drafts. No new draft column or metadata replacement DB port needed.

All owners must repeat final validation after adapting to the shared source.
Preserve failed/interrupted run logs separately; earlier passes only establish
the earlier source revision. Platform's inherited PTY lifecycle follow-up is a
later sequential platform assignment, outside H/T/R ownership.
