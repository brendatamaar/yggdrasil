# Phase 6 host context — Model Functional Effect design
Profile: service

GH-05, GH-06; supports AC-01/02/05 and NR-04. Extends `internal/core/hosts`
and `internal/hosts`, preserving the current polling reducer and optional tokens.
Host reads are on demand, not extra dashboard polling for every job log or issue.

## Shapes and state map

`ContextKey{RepoID, Kind, ObjectID, Head}` uses a server-resolved instance/project.
Kind is FailedJobs, Issues, Issue, or MRDiff. Issue identity is `(instance,
project, number)`; CI identity is run/job ID, not its browser link. GitHub's current
aggregate check status is not necessarily an Actions run: list associated runs
and show unsupported external checks explicitly rather than guess a log URL.

Six states: `Empty`, `Loading{Attempt}`, `Ready{Value,Expires}`,
`Partial{Value,Unavailable,Expires}`, `Waiting{Attempt,At,Reason,Retries}`,
`Blocked{Reason,Until}`. A cancelled request invalidates its attempt and becomes Empty.
`Until` is the negative-cache deadline of a Blocked result of a read (5 s,
`ContextBlockedTTL`, set from the supplied time). It is zero for a block that came
from the scope the shell supplied (untracked, unsupported host, no token): that
kind is never answered from cache, the shell supplies a fresh scope on every
request. The entry also carries `Seq`, the highest attempt issued, which only
grows: every scope or config reset bumps it, so a late result of a dropped attempt
can never match a later one.
Payloads are a sealed set: `JobLogs`, `IssueList`, `IssueDetail`, `MRContext`.
Tokens never enter these values, the core, API or event stream.

## Inputs

All inputs are a closed, sealed set. A time input (`Now`) that is zero is invalid:
the step ignores it (state unchanged, no commands), so a broken clock cannot create
an instantly expired entry or an instantly due wake.

| Input | Meaning |
|---|---|
| `Requested{Key,Scope,Now}` | A caller wants the key. `Scope` is what the shell resolved: `tracked`, `untracked`, `unsupported` (no client for the host kind), `no_token`. The shell only reports the last three; the core decides what they mean. |
| `Fetched{Key,Attempt,Value,Unavailable,Now}` | The attempt produced a payload. For the current attempt, a payload that is not the object the key names (another run or issue, an MR whose head moved) ends it as Blocked{not_found}; a bad gap reason or more than 500 gaps ends it as Blocked{unsupported}. Never left Loading. |
| `FetchFailed{Key,Attempt,Class,RetryAt,Now}` | Temporary or classified failure (transient, rate_limited, unauthorized, not_found). |
| `Rejected{Key,Attempt,Reason,Now}` | A terminal refusal a retry cannot fix. `Reason` is a `ContextRefusal`, a closed set (`not_found`, `unsupported`, `too_large`) that cannot name forbidden, no_token, untracked or retry_exhausted. A value outside the set still ends the attempt, as unsupported. |
| `RateLimited{Key,Attempt,Until,Now}` | Before reading, the shell found the instance limited until `Until`. A wait, not a failure: it does not use a retry. |
| `Wake{Attempt,Now}` | The wait of a waiting attempt is over. |
| `Cancelled{Attempt}` | The caller gave up on an attempt. |
| `ScopeChanged` | The target the key resolves to changed (relinked, another project or revision). |
| `ConfigChanged` | The instance or its token changed. |

`ScopeChanged` and `ConfigChanged` take the same transition (drop whatever is held,
bump `Seq`, cancel a read in flight); they stay two inputs so the reason an entry
was dropped stays readable in tests and logs. The shell produces both:
`ScopeChanged` when the resolver's target differs from the slot's, `ConfigChanged`
when the credential hash or the instance differs. The slot is kept, not replaced,
so `Seq` keeps fencing. A missing optional source is a labelled Partial result,
not an invented payload.

## Transition table

| From | Input / guard | To | Commands |
|---|---|---|---|
| Empty, expired Ready/Partial, or Blocked past `Until` | Requested, scope tracked | Loading(a+1) | ReadContext |
| Ready/Partial | Requested before expiry, tracked | unchanged | ReturnCached |
| Blocked with `Until` | Requested before `Until`, tracked | unchanged | ReturnCached |
| any | Requested, scope untracked / unsupported / no_token | Blocked{reason, no Until}, `Seq`+1 | CancelRead (if Loading/Waiting), Explain |
| Loading/Waiting | Requested for same key | unchanged | JoinRequest |
| Loading(a) | Fetched(a), payload valid | Ready or Partial | PublishContext |
| Loading(a) | Fetched(a), payload is not the keyed object (or head moved) | Blocked{not_found, Until} | Explain |
| Loading(a) | Fetched(a), bad gap reason or more than 500 gaps | Blocked{unsupported, Until} | Explain |
| Loading(a) | FetchFailed(a), transient / rate_limited, retries < 2 | Waiting | ScheduleWake |
| Loading(a) | FetchFailed(a), unauthorized | Blocked{forbidden, Until} | Explain |
| Loading(a) | FetchFailed(a), not_found | Blocked{not_found, Until} | Explain |
| Loading(a) | FetchFailed(a), retries exhausted | Blocked{retry_exhausted, Until} | Explain |
| Loading(a) | Rejected(a, any refusal) | Blocked{matching reason, Until} | Explain |
| Loading(a) | RateLimited(a), Until after Now | Waiting{At=Until}, no retry used | ScheduleWake |
| Waiting(a) | Wake(a), due | Loading(a+1) | ReadContext |
| Loading/Waiting | Cancelled current attempt | Empty | CancelRead |
| any | ScopeChanged or ConfigChanged | Empty, `Seq`+1 | CancelRead (if Loading/Waiting) |

All other pairs: unchanged, no commands. Backoff is 1s then 3s, respecting the
rate-limit deadline if later; failed reads remain explicitly retryable by the
user once the 5 s window of the Blocked result is over (or at once after a config
or scope change). A cache hit is valid for 60s, keyed by key/head/target/
credential/config.

Deviation from the first version of this table (N1): `ConfigChanged` does not take
Blocked to Loading. It takes every state to Empty: a value cached under the old
credential must not be served, and configuration alone supplies no scope proof, so
the next `Requested` (which carries the proof) is what reads. This is the safer
behaviour and the table now says so.

## Commands and cardinality

The shell interprets every command; none is dead. Nothing was deleted from the
set: the first shell ignored most of them, which this revision fixed.

| Command | Cardinality | Idempotent | Shell interpretation | Produces |
|---|---|---|---|---|
| ReadContext | one bounded fetch, dedup per key | yes, GET | the owner runs the read for the key's kind | Fetched / FetchFailed / Rejected / RateLimited |
| ScheduleWake | one wait owned by the call | yes, key+attempt | injected sleeper until `At`; if the wait does not fit in the request's remaining time: `Cancelled` and the caller gets `RetryAt` | Wake |
| CancelRead | one | yes | cancel the context of the read in flight when its attempt matches | none |
| ReturnCached | cached 60s (5s for Blocked) | yes | answer from the held entry | response |
| JoinRequest | one joined waiter | yes | wait for the owner's read, then ask again | response |
| PublishContext | one | yes, key+revision | answer from the entry the core just stored | response |
| Explain | one | yes, key+revision | answer Blocked{reason}; reason `untracked` is `ErrContextScope` | response |

Limits: 20s total request, 4 concurrent requests per instance including polling,
10 pages/500 issue or job entries, 256 KiB per job log and 1 MiB aggregate host
response budget before redaction. At most 64 KiB redacted context enters an agent
prompt, keeping it under existing 100 KiB prompt limits. Truncation is labelled.
Redirects must not forward authentication to another authority. Provider-issued
signed log redirects are consumed internally through an allowlisted, unauthenticated
fetch; their URLs are never logged, stored or returned to the agent/browser.

## Call graph

```go
// Production
API / action context request
  → parse tracked RepoID + object identity (boundary)
  → ContextService.Read
    → resolver: fresh target (a scope error wraps ErrContextScope; any other error keeps its kind)
    → environment: instance, token, client → scope tracked | unsupported | no_token
    → shell reports what it sees change: ScopeChanged (target differs), ConfigChanged (credential or instance differs)
    → ContextStep(Requested{scope, Now})                    // the core decides
        ReturnCached / Explain / JoinRequest / ReadContext   // the shell interprets
    → owner: readOnce = rescope, rate-limit gate (RateLimited), client read, rescope + config check, redaction
        → ContextStep(Fetched | FetchFailed | Rejected | RateLimited)
            PublishContext / Explain / ScheduleWake          // the shell interprets
        → ScheduleWake: sleeper → ContextStep(Wake) → ReadContext → readOnce again
    → a failed call keeps the slot; an unfinished attempt is closed with Cancelled
// Tests
ContextStep input sequences → properties
ContextService → scripted client + fake clock + fake sleeper (no real backoff)
GitLab/GitHub Reader → httptest recorded API and redirect fixtures
```

## Failures

| Node | Failure | Class | Handling |
|---|---|---|---|
| Scope/parser | untracked/foreign object/invalid key | domain outcome | refuse before network; `ErrContextScope` |
| Resolver | database or other infrastructure error | escape hatch | returned wrapped with context, never `ErrContextScope`; the slot is untouched |
| Shell pre-check | no client for the host kind, no token | domain outcome | the shell reports scope `unsupported` / `no_token`; the core blocks and explains |
| Host reader | 401/403 | domain outcome | Blocked{forbidden}, no retry |
| Host reader | 404; another object than asked for (foreign run id, a PR where an issue was asked) | domain outcome | Blocked{not_found}, no retry |
| Host reader | host cannot give the kind (Gitea logs, GitHub external checks), or the answer cannot be used (undecodable JSON, invalid identity, missing immutable head) | domain outcome | Rejected{unsupported}, no retry |
| Host reader | over the aggregate response budget | domain outcome | Rejected{too_large}, no retry |
| GET | timeout, connection, 5xx, rate limit, GitLab MR with no diff version for the head yet | retry | bounded idempotent read policy above: at most 2 retries, 1 s then 3 s, the later deadline wins |
| Payload | current attempt returns a payload that is not the keyed object, including an MR whose head moved since the key was built (N-c) | domain outcome | Blocked{not_found} for 5 s, never left Loading. A distinct `head_moved` reason was not added: it would widen the shared `ContextBlockReason` and the API's reason set for no new behaviour, since the caller's answer is the same (ask again with the current head, which is another key and reads at once) |
| Overtaken | the target, instance or token changed (or another call dropped the attempt) while this call read | escape hatch | `ErrContextSuperseded`: not a scope error, ask again for a fresh read. `ErrContextScope` is only for a repo the resolver or the core says is not tracked |
| Partial source | one log missing/oversized | escape hatch | labelled partial context, no hidden success |
| Caller | cancelled or timed out | escape hatch | the owner's wait is released; joined callers are released and ask again; the slot stays |
| Clock | returns the zero time | die | the core ignores the input; the shell reports an error and closes the attempt |
| Decoder/core | impossible invariant | die | stop this operation, retain previous cache; redacted diagnostics |

Retention on error: the slot, its counter and any value that is still valid are not
discarded because one call failed. An attempt a failed call left unfinished is
closed through the core (`Cancelled`, to Empty). A held value is only replaced by
a refresh after it expired, so there is no path where a failed refresh discards a
still-valid value; the tests prove the slot and its counter survive a failed read
and that a still-valid value survives a caller that fails.

## Rate limits

Three deadlines exist. Decision for context reads:

- `ContextService.limited` (per instance, learned from the `RetryAt` of context
  reads) and the poller's `Service.state.Limited` (per instance) are the
  authorities. Before every read the shell takes the later of the two and reports
  it to the core as `RateLimited`, which waits (or hands back `RetryAt` when the
  wait does not fit in the request) without using a retry.
- The `Getter`'s per-origin deadline is the transport backstop: while the host is
  limited it fails a request fast, without network. The context shell does not
  consult it.
- `ContextService.limited` stays because the shell cannot write the poller's
  state (only `service.go` can). For wiring: a `Service` method that records a
  deadline in `state.Limited` would let context reads feed the poller as well and
  `limited` could then go. The sleeper is a `ContextService` option
  (`WithContextSleeper`) for the same reason: `Deps` lives in `service.go`.

## Concurrency and memory

- The owner takes the core's step and its answer in one critical section
  (`feed`), so a change another call makes right after cannot turn a published
  value into an empty outcome. `ContextOutcome` is never empty with a nil error;
  a call with nothing to say gets `ErrContextSuperseded`.
- When a call finds the target or configuration changed while another call's
  read is in flight, it steps `ScopeChanged` / `ConfigChanged` (cancelling that
  read), waits for the owner to leave and reads again. The overtaken owner gets
  `ErrContextSuperseded`, unless the core holds Blocked{untracked}, which is a
  real scope error.
- The slot map is capped (`contextMaxSlots`, 256). Idle slots that hold nothing
  worth keeping (Empty, expired Ready/Partial, a Blocked past its window) are
  dropped on every insert; when the map is still full the least recently used
  idle slot goes, by the injected clock. A slot with a read in flight is never
  evicted, so only running reads can push the map past the cap. A key is an open
  space (an MR head, an issue number), so without this each key would hold up to
  64 KiB (MR diff) or 1 MiB (job logs) for ever. An evicted slot restarts its
  counter, which is safe because only an owner (never evicted) produces results.
- When no slot exists for an untracked key the shell returns `ErrContextScope`
  without a core step (there is nothing held to drop); when one exists it feeds
  `Requested{untracked}` and the returned `Explain{untracked}` is that error.
- `ContextService` has two unexported test hooks (`onJoin`, `afterStep`), nil in
  production, so tests force interleavings without sleeping.

## Resolver errors

A `ContextResolver` wraps `ErrContextScope` for not tracked / not ready / not
linked / foreign object. The reader maps exactly that to `ErrContextScope` and
tells the core the key is untracked, which drops what was held and cancels a read
in flight. Every other resolver error is wrapped with context and keeps its kind.

## Requirements

| Capability | Live | Test | Contract |
|---|---|---|---|
| ScopeResolver | db + existing hosts linkage | in-memory tracked mapping | ownership/untracking/head changes |
| ContextReader | GitLab/GitHub GET clients | recordings / scripted fake | pagination, auth, limits, redirect authority, every failure |
| Redactor | existing safety redactor | same with sentinel tokens | no token returned/logged |
| Time/worker scope | injected clock and `ContextSleeper` (default real timer) + context | controlled clock and fake sleeper | cancellation, stale wake, no worker leak, backoff without real time |

## Boundary

| Source | Parser | Produces | Rejects |
|---|---|---|---|
| API | `ContextKey.Validate` (core) at API wiring | key | arbitrary URL, nonpositive ID, wrong tracked repo (the resolver) |
| Host JSON/log bytes | `ContextAPI` decoding and `redactContext`; `ContextPayloadMatches` (core) | sealed payload | malformed fields (`ErrContextInvalid`), excessive pages/size, unsafe links, a payload that is not the keyed object |
| Redirect | the `Getter` log redirect rules (`GetLogBounded`) | internal fetch target | credentials, unapproved authority/scheme, authentication forwarding |
| Cached entry | none: entries are never parsed from outside; the cache is in memory and only the core builds them | state | n/a |

The names `ParseContextKey`, `ParseContextEntry` and `ValidateLogRedirect` of the
first version of this table do not exist and are not planned: the first is
`ContextKey.Validate` plus the resolver, the second is not needed (no persisted
entries) and the third is covered by the Getter.

## Invariants and files

`TestPropContextFencingScopeAndRetries` is one property over random input sequences
for every kind of key (issues, MR diff for two heads, failed jobs, another repo).
After every step it checks: fencing (late or foreign results change nothing), the
attempt counter only grows and a Loading attempt is the newest, retries are
bounded, a current result never leaves the entry Loading, zero times are refused,
the cache serves only before its expiry and only for a tracked scope, every command
matches the state it came with, and scope or config changes empty the entry.
`TestPropContextGeneratorReachesEveryStateAndCommand` keeps the generator honest.
Also `TestContextHeadFenceAndCancellation`, `TestContextCacheIsHeadBound`, the
shell tests in `internal/hosts/context_shell_test.go`,
`TestContextRedirectDropsCredentials`, and (still to be written by the
test-engineer batch) `TestInvariant_4_HostContextRedacted`.
Files: `core/hosts/context*.go`, `hosts/context*.go`, provider `context*.go` and
recorded fixtures; shared capability/DTO declarations in foundations. Existing
`HostClient` read-only polling methods retain their contract. Write capabilities
for comments/create repo are separate, used only by confirmed proposal execution
and specified in the actions/new-repo designs. Confirmed writers return
`ErrWriteRejected` (400/422) or a `*FetchError` of kind unauthorized (401/403) as
definite failures with nothing to reconcile, and `ErrOutcomeUnknown` only for
3xx/5xx/429, other statuses, transport errors and lost replies. For wiring:
`PushAuthorizer.Authorize` checks only the clone origin; the repository path
binding is in `NewPushTransport(authorizer, instance, repoPath, base)`, so wiring
uses the transport and never calls `Authorize` directly.

## Open questions

No safety exception proposed. Limits and GitHub external-check fallback are
concrete choices for design approval; implementation must not silently widen them.

## Implementation and verification gate

Design status: approved by the human; see `docs/phases/phase-6.md`.
No implementation or migrations are part of this document. Shared types, schema,
queries, capability declarations, DTOs and generated files land together on main
before any implementation fan-out. Consumer-local interfaces take `context.Context`;
core `Step(state, input) (State, []Command)` has no effects. All sum types are sealed.
Time, IDs and resolved facts arrive as input. Unhandled state/input combinations
return the original state and no commands; result inputs carry an attempt/revision.

The lead derives a test map after implementation merges; test-engineer implements
it in the scheduled package handoff, including evidence that each test can fail.
Every parser listed below needs round-trip (for representable domain values),
accepted-implies-valid, one test for each rejection rule and Go fuzz coverage.
Every fake capability shares a contract suite with the live SQLite, filesystem,
platform or recorded-HTTP implementation; scripted failure and duplicate cases
are clauses of the suite. Automated tests use fictional temporary repos, fake
hosts and the mock agent only. All five required `task` checks must pass; Windows
and real-host/real-agent manual checks remain explicitly separate.
