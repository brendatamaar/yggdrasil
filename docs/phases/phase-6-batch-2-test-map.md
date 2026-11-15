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

## R — bounded noise analysis and scoped history facts

Landed source: `2d0cdc1`. Designs:
[noise](../design/phase-6-noise.md),
[standup](../design/phase-6-standup.md),
[actions](../design/phase-6-actions.md).
Requirements: WS-09, AU-02 and preservation of AC-04's existing ff-only behavior.
This backend supplies facts and proposal-selection commands, not ignore-file
execution, action wiring or standup aggregation/UI. E6 needs those later owners.

TM1 owns tests/helpers only in `internal/repos/**` and
`internal/core/repos/**` for this map, after ownership handoff. Common T briefing,
independent oracles, controlled assertion failures, restoration, resource lock,
five-task/race/fuzz checks and honest manual limitations apply. No Git/platform,
DB, proposals, actions, standup, API, cmd, frontend or production edits.

### Invariants to properties

| Invariant | Independent oracle and generated sequences |
|---|---|
| Observations never mutate | Any observations, scope removals and stale proposal outcomes produce no write/agent/process/proposal execution. Only valid explicit selection can emit a create-proposal command; that command is not approval or execution. |
| Conservative thresholds | At least 100 total untracked, 50 sampled files beneath one approved generated directory and at least half the sample there. Exact 99/100, 49/50 and odd/even half boundaries; files under nested generated dirs count once per exact directory. No source/unknown directory or unescaped wildcard suggestion. |
| Exact deterministic patterns | Order-independent samples produce the same sorted candidates, evidence and anchored escaped repo-relative patterns. Metacharacters and Unicode remain literal. Check resulting Git ignore semantics against temporary repos, including outside-prefix paths that must remain visible. |
| Sampling stays honest | Candidate counts derive only from the provided sample; truncated/missing samples remain incomplete. Total count, sample count and per-directory evidence are never silently interchanged. Problem/unknown status must not become fresh noise data. |
| Selection is bounded and immutable | Nonempty duplicate-free subset of current candidates, <=32 entries/4 KiB, valid IDs/time and explicit absent-or-hash precondition. Mutation of caller slices cannot change emitted commands. New observations or wrong/nonterminal proposal outcomes cannot edit a pending proposal. Matching terminal result refreshes once; scope removal clears without effects. |
| Tracked-ready scope wins | Noise cache and history require tracked ready repos in tracked projects; retained cache cannot expose an untracked/reserved repo or a snapshot for a different path. History rechecks scope/path/project after reading before returning contents. |
| Partial history never fabricates completeness | Preserve true truncation even for zero matches. Unborn/empty readable repo succeeds empty; unreadable, malformed, cancellation and lost scope return unavailable, not successful empty history. |
| Bounded deterministic history | Precise UTC half-open author-time range <=31 days, count 1..200, bounded total bytes, full 40/64 lowercase IDs, unique identities, timestamp descending/hash ascending. No rounding/local-time drift, committer-date substitution or latest-only sampling. |

Exercise NoiseStep with reproducible random input sequences and inspect every
transition. Independently calculate thresholds, evidence, escaping and selection
membership; avoid production helpers as the oracle. Include malformed events
that should remain inert and verify commands, not merely the final variant.
Do not treat forged invalid internal states as valid public boundary input.

### Boundary parsers

- `ValidNoisePath` / `NoiseFacts.Validate` / `ParseNoiseSnapshot`: representable
  snapshot/facts round-trip, accepted implies independent relative path/count/
  uniqueness bounds, and one case per rule. Include invalid UTF-8, NUL framing,
  controls/CR/LF, absolute/drive/ADS/backslash/traversal/empty/dot components,
  `.git` components, trailing Windows-ambiguous components, duplicates, negative
  counts, >5000 paths, path length limits and too-large encoded sample. Preserve
  exact safe path bytes; never normalize a hostile name into a different file.
  Truncated counts can be lower bounds; canonical incomplete labelling is not
  raw equality with an intentionally inconsistent input flag.
- `SnapshotFrom`: retain only existing bounded untracked sample, exact counts
  and truncation. No additional filesystem scan, status invocation or file write
  to answer NoiseFacts. Ordinary repo status remains usable if noise parsing fails.
- `ParseHistoryRange`: canonical UTC round-trip, accepted implies increasing
  nonzero exact instants, <=31 days and 1..200 count, each invalid boundary plus
  overflow/extreme dates. Exact end remains excluded; nanosecond bounds retained.
- `parseHistoryCommits`: generated full-field representable round-trips and
  independent accepted-valid checks; SHA-1/SHA-256, malformed ID, invalid UTF-8/
  controls, zero/out-of-range dates, duplicates, wrong tie/time order, count and
  byte overflow. Extend fuzz to multi-record sequences, not just a valid first
  element. No production `historyOID`, `historyText` or parser as validity oracle.
- Selection boundary: separate absent file from existing empty-file hash, malformed
  hash, unexpected entry, duplicate/oversized selection and invalid IDs/time.

### Capability contracts and fault scenarios

1. Shared NoiseSource consumer clauses against scripted status reader and real
   safe Git/status on temporary repo with >5000 generated files. Exact total,
   capped sample, incomplete flag, CRLF content, aliases, cancellation, problem
   snapshot and removed/reserved scope. Repeated reads consume cached facts only.
2. Shared HistoryReader clauses against scripted HistoryGit and actual LogRange:
   author dates disagreeing with commit order, half-open bounds, UTC conversion,
   equal-time hash tie, count truncation, unborn, readable empty, error and cancel.
   Real byte cutoff with zero in-range matches must remain explicitly partial.
3. Scripted history facts at the boundary reject malformed/unordered/oversized
   results. Scope/path/project removal during the read drops all returned data;
   invalid arguments and unavailable scope make zero Git calls. Independent
   concurrent readers cannot corrupt results or alias returned slices.
4. Existing ff-only pull/actions/mainlock characterization remains unchanged;
   new noise/history helpers cannot introduce automatic pulls or a merge fallback.
   Ignore precondition/confirmation/write recovery belongs to P's later map.

Inspect existing R implementation tests first. Retain their useful threshold and
partial-zero assertion-failing controls. Add missing properties/independent
parser/contracts with meaningful failure evidence; do not mirror helper internals.
Native Windows checklist: large generated samples and names with spaces/Unicode/
CRLF; unchanged ordinary status responsiveness; exact anchored ignore preview
without any write on observation; date bounds/order and partial/unavailable labels.
Actual ignore confirmation and standup presentation need their integrated checks
later. Linux temporary repos do not establish native Windows behavior.

## H — host context and confirmed host capabilities

Landed source: `63c14d9` plus lead repair `bda193d`, merge `cc626a7`.
Designs: [host context](../design/phase-6-host-context.md),
[actions](../design/phase-6-actions.md),
[new repos](../design/phase-6-newrepos.md).
GH-05/06, AC-01/02/05, NR-04 backend capabilities only. Application wiring must
inject a fresh tracked-ready and object-ownership resolver; confirmed writers
must be available only to proposal execution. Capability existence is not evidence
of a live UI/API feature or confirmation enforcement at later call sites.

TM1 owns tests/helpers/recordings only in `internal/hosts/**` and
`internal/core/hosts/**` for this map. Common briefing, independent oracles,
assertion-failing controls/restoration and final five-task/race/fuzz rules apply.
No production, API, cmd, DB, git, platform or later-owner package changes.

### Invariants to generated properties

- Context requires current tracked-ready/object scope before cache, network,
  after waits and before publication. Foreign object, relink/path/head/revision,
  credential/config rotation and untracking invalidate results. Cache validity
  is pinned to key/head/target/credential/config and 60-second expiry.
- Context attempts fence fetched/failure/wake/cancel events. Unhandled or stale
  variants cannot publish; cancellation invalidates current attempt. Transient
  and rate-limit reads retry at most twice with supplied-time 1s/3s backoff and
  the later instance deadline; no detached timer or worker outlives its context.
  Joined readers do not start extra fetches; cancellation and completion release
  waiters, and one caller cannot mutate cached slices seen by another.
- Logs, issue bodies, diffs, metadata and unavailable labels are redacted before
  persistence/attachment. Tokens and signed URLs never reach returned payload,
  errors/logs, core, child environment or audit. UTF-8 truncation preserves valid
  text and serialized structural overhead fits the 64 KiB context budget.
- All reads remain bounded: 20-second request, four concurrent per configured
  instance including poller, ten pages/500 entries, 256 KiB per job log and 1 MiB
  raw aggregate before redaction. Failed reads consume observed bytes; per-job
  overflow is explicit partial/truncated, never silently complete. Source counts
  and partial/unavailable distinctions stay honest for empty and nonempty data.
- Authenticated requests retain configured origin. Signed log redirects use only
  injected approved origins and carry no Authorization/PRIVATE-TOKEN/Cookie,
  including return hops. Disallowed redirects make no foreign authenticated call;
  provider URLs never become caller-controlled arbitrary network targets.
- Confirmed adapters never retry a POST. Lost/invalid/non-2xx replies remain
  Unknown until read-only reconciliation. Comments require exact full head and
  immutable marker/body hash; duplicate/conflicting/incomplete searches never
  authorize resend. New repo adoption requires exact owner/name/marker/privacy,
  immutable remote ID/clone origin and proven empty refs; 404 is not absence.
- Publication authorization changes only Authorization on configured credential-
  free HTTPS clone origin, with deployment prefix and safe query/path restrictions.
  Transport reauthorizes every request so a foreign redirect cannot see credentials.

### Boundary maps

1. `ContextKey.Validate`: generated representable round-trips, independent valid
   RepoID/kind/object/full-head oracle, invalid UTF-8/delimiters/URL/object bounds
   and one rejection per rule; fuzz every component and retain existing corpus.
2. Provider JSON/log parsers: recorded GitLab/GitHub issues, issue detail, runs/
   jobs and immutable MR/base/head/version diff. Round-trip the representable
   domain projection, independent accepted-valid assertions, each malformed or
   mismatched identity/status/timestamp/unsafe URL/page/size rule and fuzzing.
   Ignore unrelated provider fields deliberately; do not require an impossible
   byte-for-byte provider-wire round-trip. External checks/Gitea stay unsupported.
3. Redaction/truncation: inject sentinel credentials in every string field and
   errors/redirects; compare independent serialized byte bounds and UTF-8 checks.
   Include very many small labels/IDs and multibyte boundary cuts. Missing data
   must remain labelled rather than materialized as a successful complete payload.
4. Comment/create/lookup parsers: full 40/64 heads, positive IDs, exact marker/body
   suffix/hash, duplicate results, pagination, owner/visibility/description and
   credential/query-bearing clone URLs. Require safe outcomes, not just nil errors.
5. Push destination: independently parse scheme/authority/port/prefix/path/query,
   check complete request equality except Authorization, and fuzz unsafe raw URL
   forms/Host/RequestURI/credentials. Rejected request body closes without mutation.

### Fake/live capability contracts and specific regression work

- Shared ContextReader scenarios through scripted ContextClient and recorded
  GitLab/GitHub HTTP fixtures: success/empty/partial/tokenless/expired/rate-limit/
  5xx/unsupported, retry bounds, scope/head changes during reads, cancellation,
  joined waiters, cache TTL/config/credential invalidation and mutation isolation.
- Getter/provider contracts use real httptest connections, paginated short pages
  with continuation, immutable IDs and versions, bounded total bytes including
  failed bodies, and shared poller/context concurrency. Do not substitute a fake
  that reproduces the implementation bug as its own expectation.
- **Lead repair regression:** the original per-job read used the remaining 1 MiB
  aggregate limit. Assert actual consumed bytes <= min(remaining, 256 KiB)+one
  overflow sentinel before redaction, and explicit labelled partial on overflow.
  Cover exact limit, limit+1, far larger body and remaining aggregate below limit.
  The aggregate-budget failed-read test must remain meaningful after this cap;
  do not simply assert one request while ignoring actual bytes or missing labels.
  Mutation removing the per-job cap must fail this independent regression.
- Shared comment writer/lookup contracts through fake and recorded HTTP: exactly
  one POST, pre-POST head/scope check, valid comment ID, lost reply -> exact found,
  changed head/body/marker/duplicate/missing/incomplete list -> conflict/unknown
  as specified. Never auto-POST after read failure or uncertain result.
- Shared RepoHost contracts: private user/org create permission, one POST,
  malformed/lost reply, strict operation-marked lookup, unrelated existing repo,
  hidden private 404, empty/nonempty/incomplete refs. No cleanup or adoption guess.
- PushAuthorizer/transport contracts: correct configured clone origin, API-origin
  rejection, only-Authorization mutation, wrong origin/port/prefix/path/query,
  nil/malformed requests and real redirect handshake proving foreign transport
  sees no credential-bearing request. No actual external host or Git publish.

Inspect landed tests before adding coverage. Existing head-guard/budget controls
and fuzz corpus are useful evidence, but add independent rules where current
oracles reuse production validators. TM1 is test-only: report production findings
for lead repair and preserve their failing regression evidence.

Manual Windows/live-host checks: separately authorized test accounts/repos only,
permissions/token expiration/missing token, failed logs/signed redirects, issue
import inertness, stale MR head, exact comment preview/reject/confirm and user/org
private GitHub destination. Real agent CLI use needs separate session agreement.
No automated result here establishes native Windows networking/process behavior
or external host availability/permissions.
