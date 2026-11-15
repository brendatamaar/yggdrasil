# Phase 6 Batch 2 post-merge test maps

Derived by the lead after each designed feature lands. TM1 executes these maps
in Batch 4 after the owning packages are released; this document is its prepared
brief, not a claim that the tests or final feature gates are complete. Reuse and
strengthen existing assertions rather than duplicating them. No production edits.

## T — issue drafts, explicit start and durable task submission

Landed source: `dc0d9f8`, merge `97a7b63`. Designs:
[actions](../design/phase-6-actions.md),
[new repos](../design/phase-6-newrepos.md), existing tasks/guard designs.
Requirements: AC-05, NR-02, AG-08, SF-05/06/07/09. These are backend contracts;
W/U/E own issue-to-board presentation and integrated action flows. E6 contribution
is a meaningful issue action without implicit execution, plus safe later admission.

TM1 owns tests/helpers only in `internal/tasks/**` and `internal/core/tasks/**`
for this map. No DB, API, cmd, frontend, A/S or other owner's package edits. Use
existing real SQLite harness, safe temporary Git fixtures and scripted adapter.
Read `.claude/agents/test-engineer.md`, `docs/AGENTS.md` and the approved designs;
follow functional-effect:model/test requirements. **Show each test can fail**:
record safe controlled mutations/negative controls, require assertion failures,
restore production exactly and verify the restored source. Report uncovered
production defects with failing regressions; do not fix production as test engineer.

### Invariants to properties

| Invariant | Independent oracle and scenarios |
|---|---|
| Import never runs work | Generated valid import/scope/dedup sequences yield only Ask Draft or refusal. No turn, session, worktree, queued job or adapter Start from imports/replays; source and selected identity retained. Existing `TestPropImportNeverStarts` is a starting point. |
| Scope precedes dedup | Tracked-ready repo belongs to tracked project in the committing transaction; untracked, reserved, foreign-project and changed-scope cases refuse even when an issue/request link already exists. Assert no collateral writes. |
| Issue identity is permanent | Same project/host/project-path/issue returns one original task across distinct request IDs, concurrent contenders, restart and after draft start. Different issue/project keys do not collide. Imported content may change for a new request without replacing the first task. |
| Request identity is immutable | Same RequestID and complete payload returns original task/first turn without redispatch. Changing any immutable field or request kind refuses. Generate changes to Agent/Model/mode/opt-in/scope/repo/title/prompt/project; retain the distinction between request dedup and issue dedup. |
| Explicit start is required | A draft starts only after ordinary admission. Repeated/concurrent starts cannot append a second first turn; setup drafts refuse through both StartDraft and ordinary Message. A rejected start leaves the draft inert. |
| Admission never widens access | No-origin alone changes no policy. Direct edit requires explicit current_branch, opt-in/warning, write modes, fresh trust, enforcing adapter and the shared editor lock. Code stays isolated; Ask preserves integrity. Competing editors and trust changes cannot bypass admission. |
| Identity/mode survive restarts | Import Agent/Model are immutable; omitted/matching start choices use stored values, differing choices refuse. First writing-mode choice survives Ask follow-ups and restart; switching writing mode still refuses. |
| Persistence and effects are ordered | Task/first turn/request link/audit commit together. No adapter call on rejected or rolled-back writes. A crash after commit before enqueue reconciles original task/turn once after restart. |

Use generated sequences and check invariants after every operation, including
collisions and rejected operations. Keep failures reproducible with seeds and
small failing sequences. Pure PlanDraft receives already parsed specs and shell
facts/IDs/time; tests must not manufacture invalid internal facts and reinterpret
that as a supported public input contract.

### Parsers and reconstruction

- `IssueRef.Validate` / source / dedup: independent component-level UTF-8,
  identifier and delimiter checks; positive issue numbers; blank/control,
  oversized, credential/URL and ambiguous identities rejected. Include invalid
  UTF-8 split across two fields. Generated provenance reconstructs the same issue.
- `ParseDraftSpec`: canonical round-trip/idempotent parse, accepted implies an
  independent primitive validity predicate, and each rejection rule separately.
  Exercise IDs/request length, title rune bound versus byte length/whitespace/
  controls, brief byte bound/UTF-8/NUL, agent/model shape and issue constraints.
  Extend fuzz fields beyond prompt/title so identity rejection is exercised too.
  Do not reuse `ValidModelName`, `ParseDraftSpec` or production validators as the
  validity oracle. Whitespace normalization is intentional; do not demand raw
  noncanonical equality.
- Request digest/replay reconstruction: independently encode the documented kind
  plus full payload, assert first identity/result round-trip after database reopen,
  and reject changed immutable fields. No need to expose an internal digest API
  or create a second production serialization contract just for the test.
- Start errors: invalid task, absent/foreign task, non-draft/setup task and changed
  identity are distinct supported refusals. Preserve user-facing ordinary
  admission behavior; never accept an empty success result as a refusal.

### Capability contracts and failure matrix

Run common clauses through real `tasks.Service` with its temporary SQLite store
and scripted mock adapter. Where a fake exists, share the contract clauses with
its real implementation; use task-package wrappers/fault hooks rather than editing
DB production or duplicating the database implementation in a fake.

1. Import success/duplicate/request mismatch/issue dedup and tracked-ready scope;
   source/title/prompt/Agent/Model round-trip after restart. No execution effects.
2. Submission success, equal replay, changed payload, same-key contenders and
   different-key issue contenders: exact task/turn/link counts and stable winner.
3. Transaction fault at audit/link/task/turn boundaries where existing hooks allow:
   all related state rolls back, including preceding audit writes. Propagate errors;
   reload winner outside a failed transaction. Do not count an early pre-commit
   refusal as proof of an actual SQLite commit/audit failure.
4. Crash after committed admission before command interpretation: restart/resume
   observes one first turn and at most one mock Start. No second request admission.
5. Admission matrix: writes off, absent opt-in, limited enforcement, untrusted or
   stale-trust repo, absent origin, busy shared editor lock, ordinary Code and Ask.
   Assert actual selected working directory, warning and adapter start count.
6. Explicit start races: same service and independently reopened service where the
   harness supports it; immutable first turn, no setup escape through Message,
   unchanged draft after refusal, correct editor-lock release after cancellation.

Already-landed implementation tests cover much of this map. Inspect them first,
retain useful mutation evidence, and add only missing independent properties,
parser cases or contract clauses. Later action service recovery belongs to TM2;
remote comment/publish effects and setup reservation behavior belong to their
own maps, not this T contract.

### Verification and handoff

All five actual Task commands must pass on final source; relevant tasks/core race
and bounded fuzz runs must pass. Follow the shared validation lock/resource/Node
handoff in `phase-6-batch-2-briefs.md`; preserve failed logs separately. Do not
retry a failing assertion until green without a diagnosed correction. Commit tests
on the isolated branch, report exact requirements/checks/negative controls and
release package ownership. Production findings remain open for the lead.

Windows manual verification: create/import an inert issue draft; start it with a
mock or separately authorized real enforcing adapter. Verify direct-edit opt-in,
main-checkout warning, fresh trust and single-editor contention before adding an
origin; Code remains in a worktree. Verify paths with spaces/Unicode/long paths,
Ask integrity, cancellation and child cleanup. Real host or agent use requires
separate session authorization; Linux fixtures do not establish native Windows
behavior.
