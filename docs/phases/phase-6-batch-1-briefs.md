# Phase 6 Batch 1 dispatch briefs

Status: prepared; **not dispatched**. Dispatch only after foundation regression
integration, all five required checks and both review gates pass. The lead adds
the actual committed foundation SHA and batch-start SHA to each invocation.

The human approved Codex for all remaining implementation and test assignments.
Each assignment uses an isolated worktree from committed main; at most three
implementation agents run at once. No two tasks below own a file or Go package.
Read `.claude/agents/go-backend.md` and `docs/AGENTS.md`, applying the approved
provider override while keeping every ownership, safety and report rule.

Common brief requirements:

- Verify `git merge --ff-only main` before edits. Never create a checkout in shell.
- Read the named PRD IDs, approved design and current code. Follow the
  functional-effect:model skill (docs/AGENTS.md 5.11).
- Apply `/functional-effect:extract` to the assigned flow, with characterization
  tests first. Preserve observable behavior, error messages and existing safety
  gates. No feature implementation is hidden in the refactor.
- Frozen foundation API/DB/generated/service contracts remain unchanged. Helpers
  wholly internal to the owned domain may be added; report any cross-package
  interface change to the lead. No core IO, clocks, randomness, logging or OS
  checks. No new allowlist/fe:allow exception.
- Run `task generate`, `task lint`, `task typecheck`, `task test`, `task test:web`
  and relevant targeted/race checks. Report environment failures and skips.
  Use the pinned lint formatter for new files. Coordinate heavy checks because
  disk space is constrained; use task-specific executable RAM Go scratch if useful.
- Do the work yourself; no subagents. No push, tag, file/branch deletion, PR,
  real repos/hosts, agent CLI/model API tests, HOME override or global Git config.
- Commit on your branch and end with the report format from your agent file.

## XH — Host context resolution extraction

Owner: go-backend-H. PRD GH-05, GH-06, AC-01, AC-02, AC-05 groundwork.
Owned paths: `internal/hosts/**`, `internal/core/hosts/**` (including tests/fixtures).
Must not edit tasks, repos, DB, API, cmd, platform, generated code or web.
Design: `docs/design/phase-6-host-context.md`, existing `docs/design/git-hosts.md`.

Use existing `hosts.HostClient`, `Getter`, config/remote boundary parsers and
`core/hosts` polling/context types. Characterize `Service.readLink` instance/project
resolution and fetch-failure mapping first, then move remaining choices to pure
core functions supplied parsed remote/config/response facts. The shell retains
remote IO, token lookup, JSON/URL parsing, concurrency, redaction and publication.
Do not move a URL parser into core by weakening its import rules. Preserve cache,
instance fallback, rate-limit, cancellation and stale-result behavior. Do not
implement GH-05/06 reads or activate ContextStep in this extraction task.

Done: original behavior characterized; moved decisions have deterministic/property
coverage; all required checks pass; report exact core helper signatures and evidence.
Contributes to Phase 6 E6 by preserving reliable scoped host context for later H/A.

## XT — Task submission extraction

Owner: go-backend-T. PRD AC-01, AC-03, AC-05, NR-02 groundwork.
Owned paths: `internal/tasks/**`, `internal/core/tasks/**` (including tests/fixtures).
Must not edit hosts, repos, guard, DB, API, cmd, generated code or web.
Design: `docs/design/phase-6-actions.md`, existing `docs/design/tasks.md` and guard design.

Use `Service.Message`, `scopeRepos`, existing parsed-message/core-task types and
`guard.Admit` exactly as frozen. Characterize new/resumed submission, scope/project
selection and writing-mode restrictions before extracting any remaining pure
choices. Read store/adapters/trust/snapshot facts in shell, then pass plain values
to core. Preserve task admission, existing mode-switch rules, Ask integrity,
maxAgents queue and single-editor locks. Do not implement issue draft/start APIs,
direct-edit bypasses, plan/budget/subtask features or change guard policy.

Done: behavior-preserving extraction behind characterization tests, no admission
regressions, clear helper/input types, all checks pass. E6 contribution: safe reuse
of ordinary task submission by actions and first-prompt new-repo flows.

## XR — Repo facts extraction

Owner: go-backend-R. PRD WS-09, AC-04 groundwork.
Owned paths: `internal/repos/**`, `internal/core/repos/**` (including tests/fixtures).
Must not edit hosts, tasks, git, platform, DB, API, cmd, generated code or web.
Design: `docs/design/phase-6-noise.md`, existing repo-status and git-actions designs.

Use the existing status-reader/refresher and git-action pure steps. Characterize
status-to-facts/truncation and pull-refusal outcome choices before moving any
remaining decisions from shell to core. Keep filesystem/git reads, lock acquisition,
time and publication in shell. Existing core decisions should be reused rather than
rewritten. Preserve ff-only semantics, dirty/diverged refusal, shared mainlock,
tracked-only scope, accurate sampled-file counts and status refresh performance.
Do not implement noise classification, ignore writes or new pull paths in this task.

Done: observable behavior unchanged and characterized; new pure decision coverage;
required checks pass; report any flow already pure with concrete evidence rather
than generating churn. E6 contribution: trustworthy status/pull facts for R/A.
