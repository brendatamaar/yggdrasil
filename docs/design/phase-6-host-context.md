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
`Partial{Value,Unavailable,Expires}`, `Waiting{Attempt,At,Reason}`,
`Blocked{Reason}`. A cancelled request invalidates its attempt and becomes Empty.
Payloads are a sealed set: `JobLogs`, `IssueList`, `IssueDetail`, `MRContext`.
Tokens never enter these values, the core, API or event stream.

## Inputs

`Requested{Key,Now}`, `Fetched{Key,Attempt,Value,Now}`,
`FetchFailed{Key,Attempt,Class,RetryAt,Now}`, `Wake{Attempt,Now}`,
`Cancelled{Attempt}`, `ScopeChanged{Tracked,Head}`, `ConfigChanged`.
A missing optional source is a labelled Partial result, not an invented payload.

## Transition table

| From | Input / guard | To | Commands |
|---|---|---|---|
| Empty or expired Ready/Partial | Requested, tracked/supported | Loading(a+1) | ReadContext |
| Ready/Partial | Requested before expiry, same head | unchanged | ReturnCached |
| Loading | Requested for same key | unchanged | JoinRequest |
| Loading(a) | Fetched(a), still tracked/same head | Ready or Partial | PublishContext |
| Loading(a) | FetchFailed(a), 429/temporary, retries < 2 | Waiting | ScheduleWake |
| Waiting(a) | Wake(a), due | Loading(a+1) | ReadContext |
| Loading(a) | forbidden/not found/unsupported or retry exhausted | Blocked | Explain |
| Loading/Waiting | Cancelled current attempt | Empty | CancelRead |
| any | ScopeChanged removes repo/changes relevant head | Empty, invalidate attempt | CancelRead |
| Blocked | ConfigChanged or explicit Requested with valid configuration | Loading(a+1) | ReadContext |

All other pairs: unchanged, no commands. Backoff is 1s then 3s, respecting the
instance-wide rate-limit deadline if later; failed reads remain explicitly retryable
by the user after exhaustion. A cache hit is valid for 60s, keyed by head/object ID.

## Commands and cardinality

| Command | Cardinality | Idempotent | Produces |
|---|---|---|---|
| ReadContext | one bounded fetch, dedup per key | yes, GET | Fetched/FetchFailed |
| ScheduleWake | one timer owned by context | yes, key+attempt | Wake |
| CancelRead | one | yes | none |
| ReturnCached / JoinRequest | cached 60s / one joined waiter | yes | response |
| PublishContext / Explain | one | yes, key+revision | response/invalidation |

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
  → hosts.Context: resolve configured instance and validate ownership
    → hosts.ContextStep (core)
    → Getter / host-specific reader → bounded/redacted context (boundary)
    → hosts.ContextStep(result) → API DTO / action attachment
// Tests
ContextStep input sequences → properties
Context service → scripted Reader + fake time
GitLab/GitHub Reader → httptest recorded API and redirect fixtures
```

## Failures

| Node | Failure | Class | Handling |
|---|---|---|---|
| Scope/parser | untracked/foreign object/invalid key | domain outcome | refuse before network |
| Host reader | missing token, 401/403, 404, unsupported external CI | domain outcome | plain explanation; optional action can use available metadata |
| GET | timeout, connection, 5xx, rate limit | retry | bounded idempotent read policy above |
| Partial source | one log missing/oversized | escape hatch | labelled partial context, no hidden success |
| Decoder/core | impossible invariant | die | stop this operation, retain previous cache; redacted diagnostics |

## Requirements

| Capability | Live | Test | Contract |
|---|---|---|---|
| ScopeResolver | db + existing hosts linkage | in-memory tracked mapping | ownership/untracking/head changes |
| ContextReader | GitLab/GitHub GET clients | recordings / scripted fake | pagination, auth, limits, redirect authority, every failure |
| Redactor | existing safety redactor | same with sentinel tokens | no token returned/logged |
| Time/worker scope | injected clock + context/errgroup | controlled clock | cancellation, stale wake, no worker leak |

## Boundary

| Source | Parser | Produces | Rejects |
|---|---|---|---|
| API | ParseContextKey | key | arbitrary URL, nonpositive ID, wrong tracked repo |
| Host JSON/log bytes | ParseJobs/Issues/MR, bounded UTF-8/log normalizer | sealed payload | malformed fields, excessive pages/size, unsafe links |
| Redirect | ValidateLogRedirect | internal fetch target | credentials, unapproved authority/scheme, authentication forwarding |
| Cached entry | ParseContextEntry | state | invalid variant, impossible attempt/expiry |

## Invariants and files

`TestPropContextRequiresTrackedScope`, `TestPropContextLateResultIgnored`,
`TestPropContextBoundedRetries`, `TestPropContextCacheHeadBound`,
`TestInvariant_4_HostContextRedacted`, `TestContextRedirectDropsCredentials`.
Files: `core/hosts/context_*.go`, `hosts/context*.go`, provider `context*.go` and
recorded fixtures; shared capability/DTO declarations in foundations. Existing
`HostClient` read-only polling methods retain their contract. Write capabilities
for comments/create repo are separate, used only by confirmed proposal execution
and specified in the actions/new-repo designs.

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
