# Git hosts — Model Functional Effect design

Covers:

- GH-01: self-hosted GitLab (multiple instances), GitHub and Gitea. Gitea is community-maintained.
- GH-02: MR/PR status, pipeline status, the MR inbox and links to each host.
- GH-04: optional tokens in `.env` that add detail, including GitHub fine-grained tokens. Every token is documented in `.env.example` as optional.

GH-03 (MR creation) moved to Phase 3, so **everything in this design is read-only**: GET requests only.

- Core: `internal/core/hosts`.
- Shell: `internal/hosts` (service, config, remote parsing) and `internal/hosts/{gitlab,github,gitea}` (clients).

Host data is a cached read model. It is held in memory, not persisted and not audited. Nothing it does changes user data or a remote.

## State map

```go
type Instance struct {
    ID    InstanceID      // "github.com", "git.acme.dev" (host[:port], lower case)
    Kind  Kind            // gitlab | github | gitea
    Base  URL             // https://git.acme.dev (API base derived per kind)
    Token bool            // a token is configured (the token itself never enters the core)
}

type Key interface{ isKey() }            // sealed
type RepoKey  struct{ Repo core.RepoID; Branch string }  // branch "" = detached: not polled
type InboxKey struct{ Instance InstanceID }

type Poller struct {
    Instances map[InstanceID]Instance
    Targets   map[Key]Target             // what should be polled now
    Entries   map[Key]Entry
    Limited   map[InstanceID]time.Time   // instance-wide rate limit until
    InFlight  map[InstanceID]int         // per-instance concurrency (max 4)
}
type Target struct{ Instance InstanceID; Project ProjectPath; TTL time.Duration }  // repo 60 s, inbox 120 s
type Entry struct {
    Attempt int
    Phase   Phase                         // sealed, below
}
```

`Phase` (sealed), per key:

- `Idle{Data Payload, FetchedAt time.Time, ETag string}`: has data, or `Data == nil` before the first fetch.
- `Fetching{Attempt, Prev Idle}`: request in flight. The UI keeps showing `Prev`.
- `Waiting{Until time.Time, Failures int, Prev Idle, Why WaitReason}`: backing off after a transient error, or because of a rate limit (`transient` | `rate_limited`).
- `Blocked{Why BlockReason, Since time.Time, Prev Idle}`: polling stops until config changes or the user asks again. Reasons:
  - `unauthorized`: bad or expired token;
  - `not_found`: private without a token, or wrong path;
  - `unsupported`: an instance with no kind configured.

Repos that can't be linked are not keys. They have no remote, an unknown host, or a detached HEAD. They show only "not linked" plus the reason, derived when the targets are built.

`Payload` (closed set):

- `RepoState{MR *MergeRequest, Pipeline *Pipeline}` for the current branch;
- `Inbox{Items []InboxItem}`.

```go
type MergeRequest struct {
    Number int; Title, URL, Author, Source, Target string
    State  MRState    // open | draft | merged | closed
    UpdatedAt time.Time
}
type Pipeline struct {
    Status PipelineStatus // pending | running | success | failed | canceled | skipped | unknown
    URL string; UpdatedAt time.Time
}
type InboxItem struct {
    MR      MergeRequest
    Repo    string        // "group/repo" on the host
    Role    Role          // review_requested | assigned | authored
}
```

`Links(Instance, ProjectPath, branch)` is pure. It builds the MR list, pipelines and repo links, which always work, even without a token or data.

## Inputs

| Input | From | Fields |
|---|---|---|
| `ConfigChanged` | startup; the `.env` file reloaded (on demand, through settings) | `Instances []Instance, Now` |
| `TargetsChanged` | repo-status after each refresh (branch per repo); tracking changes | `Repos []RepoTarget{Repo, Instance, Project, Branch}, Now` |
| `Tick` | shell ticker (every 15 s) | `Now` |
| `RefreshRequested` | user ("refresh"), MR inbox opened | `Keys []Key` (empty = all), `Now` |
| `Fetched` | interpreter | `Key, Attempt, Payload, ETag, Now` |
| `NotModified` | interpreter (304 on a conditional request) | `Key, Attempt, Now` |
| `FetchFailed` | interpreter | `Key, Attempt, Kind FailKind, RetryAt *time.Time, Now` |

`FailKind` is one of `transient` (timeout, connection error, 5xx), `rate_limited` (429, or 403 with a rate-limit header), `unauthorized` (401, or 403 without a rate-limit header) and `not_found` (404). Inbox targets come from `ConfigChanged`: one `InboxKey` per instance with a token.

## Transition table

`due(k, now)` is true when the key is a target and one of these holds:

- it is `Idle` with `Data == nil`;
- it is `Idle` and `now ≥ FetchedAt + TTL`;
- it is `Waiting` and `now ≥ Until`.

`start(k)`:

- requires the instance not to be `Limited` past `now` and its `InFlight` below 4;
- moves the key to `Fetching{Attempt+1}` and emits `Fetch{k, target, attempt, etag}`.

After every input, `start` runs for due keys in a stable order: inbox first, then repos ordered by repo ID.

| From | Input | Guard | To | Commands |
|---|---|---|---|---|
| any | ConfigChanged | — | replace `Instances`; drop keys of removed instances; `Blocked` keys of an instance whose token or kind changed → `Idle{Prev data}` (a retry) | `Publish(HostsChanged)`, `Fetch` for started keys |
| any | TargetsChanged | — | add new `RepoKey`s as `Idle{nil}`; drop keys no longer targeted (also those in flight; their late result is ignored) | `Publish` for dropped keys, `Fetch` for started keys |
| any | Tick | — | — | `Fetch` for started keys |
| any | RefreshRequested | per key: a target, not `Fetching` | `Waiting` or `Blocked` → `Idle{Prev}`; `Idle` → `FetchedAt` cleared, so it is due | `Fetch` for started keys |
| `Fetching{a}` | Fetched | `Attempt == a`, key still targeted | `Idle{Payload, Now, ETag}` | `Publish(KeyChanged)` if the data differs; `Fetch` for started keys |
| `Fetching{a}` | NotModified | `Attempt == a` | `Idle{Prev.Data, Now, Prev.ETag}` | `Fetch` for started keys |
| `Fetching{a}` | FetchFailed transient | `Attempt == a` | `Waiting{Now + backoff(Failures+1), Failures+1, Prev, transient}` | `Publish` (the UI shows "retrying") |
| `Fetching{a}` | FetchFailed rate_limited | `Attempt == a` | `Waiting{RetryAt or Now+60 s, …, rate_limited}`; `Limited[instance] = that time` | `Publish(HostsChanged)` |
| `Fetching{a}` | FetchFailed unauthorized | `Attempt == a` | `Blocked{unauthorized, Now, Prev}`; every other key of the instance that needs the token → `Blocked{unauthorized}` too | `Publish(HostsChanged)` |
| `Fetching{a}` | FetchFailed not_found | `Attempt == a` | `Blocked{not_found, Now, Prev}` | `Publish(KeyChanged)` |
| any | Fetched / NotModified / FetchFailed | stale attempt or untargeted key | no change | none |

`backoff(n)` = min(10 s · 3ⁿ⁻¹, 10 min): 10 s, 30 s, 90 s, 270 s, then 10 min. It is deterministic, with no jitter; the per-instance concurrency limit already spreads requests out. `Fetching` also frees one `InFlight` slot on every result.

**Any other (state, input) pair: no change, no commands.**

`FailedPipelines(Poller) set[core.RepoID]` is pure. A repo is in it when its `RepoState.Pipeline.Status == failed` for the current branch. repo-status counts use it for the WS-03 "Failed pipeline" filter.

## Invariants

| # | Invariant | Property test |
|---|---|---|
| H1 | Per instance, at most 4 keys are `Fetching`. | `TestPropInstanceConcurrency` |
| H2 | No `Fetch` is emitted for an instance while `now < Limited[instance]`. | `TestPropRespectsRateLimit` |
| H3 | A `Blocked` key never emits `Fetch` until `ConfigChanged` (for its instance) or `RefreshRequested` (for it). | `TestPropBlockedStaysQuiet` |
| H4 | The backoff is non-decreasing in `Failures`, capped at 10 min, and a success resets `Failures` (`Idle` has none). | `TestPropBackoffBounded` |
| H5 | A stale or untargeted result changes nothing. | `TestPropStaleResultIgnored` |
| H6 | Data is never lost on failure: after any `FetchFailed` the key's `Prev.Data` equals the last fetched payload. | `TestPropKeepsLastGoodData` |
| H7 | Only targeted keys have entries, and every targeted key has one. | `TestPropEntriesMatchTargets` |
| H8 | A key is fetched at most once per TTL while it keeps succeeding (no hot loop). | `TestPropNoFetchBeforeTTL` |
| H9 | Every command is a `Fetch` or a `Publish`: nothing can write to a host (read-only phase). | `TestPropReadOnlyCommands` |

## Commands

| Command | Cardinality | Idempotent | Produces |
|---|---|---|---|
| `Fetch{Key, Instance, Project, Branch, Attempt, ETag}` | one (2 to 3 GETs inside the client: MR + pipeline, or inbox queries) | yes: GET only; deduplicated by `(Key, Attempt)` | `Fetched`, `NotModified` or `FetchFailed` |
| `Publish(KeyChanged / HostsChanged)` | one | yes | — |

Cached: the payload per key, TTL 60 s for repos and 120 s for the inbox. The dedup key is the `Key`. GitHub conditional requests (`If-None-Match` with the ETag) don't count against its rate limit.

## Call graph

```go
// Production
startup (cmd/ygg newApp)
  → hosts.LoadConfig(dataDir/.env, process env)   [boundary] → []Instance + secrets (map InstanceID → token, kept in the shell only)
  → redactor.Add(each token)                      // invariant 4: tokens can never reach logs or audit
  → hostsService.Start → ConfigChanged
repo-status refresh done → for each active repo: git.RemoteURL(ctx, path, "origin")   (read-only, cached per repo until the next tracking change)
  → hosts.ParseRemote(url) [boundary] → RemoteRef{Host, Project} | reason not linked (+ credentials-in-URL flag)
  → match Host to Instance → TargetsChanged

hostsService (one goroutine owns the Poller; inputs on a channel; 15 s ticker)
  input → hosts.Step(state, input) [core] → cmds
  Fetch → worker goroutine (errgroup):
      client := clients[instance.Kind]                // gitlab | github | gitea
      ctx with 20 s timeout
      client.RepoState(ctx, inst, token, project, branch, etag)   |  client.Inbox(ctx, inst, token)
          → net/http GET, header "Authorization: Bearer <token>" (GitLab: "PRIVATE-TOKEN"), never in the URL
          → hosts/<kind>.parse* (boundary: JSON → core payload)
          → classify: 304 → NotModified; 401 → unauthorized; 403+rate header / 429 → rate_limited(RetryAt from Retry-After or X-RateLimit-Reset); 404 → not_found; 5xx/timeout → transient
      → result input to the service channel
  Publish → hub.publishNext(HostsTopic{}, …)

HTTP GET /api/hosts                → instances (kind, base, token configured: yes/no; never the token)
HTTP GET /api/repos/{id}/host      → RepoState DTO + Links + phase (fresh / retrying / blocked + reason)
HTTP GET /api/mrs                  → merged inbox across instances (deduplicated by URL), with per-instance phase
HTTP POST /api/hosts/refresh {keys}→ RefreshRequested
```

```go
// Tests
hosts.Run(initial, inputs) (core) → H1–H9
service tests: fake HostClient (scripted results, 304s, 429 with RetryAt), fixed clock
client tests: httptest.Server replaying recorded, fictional fixtures (testdata/<kind>/*.json + headers)
```

## Failures

| Node | Failure | Class | Handling |
|---|---|---|---|
| LoadConfig | malformed line, unknown kind, URL without scheme | domain outcome | instance skipped, reported on `/api/hosts` and in `ygg doctor` with the line number (never the value) |
| ParseRemote | no remote, unknown URL form, unknown host | domain outcome | "not linked" + reason; no key |
| ParseRemote | credentials in the remote URL | domain outcome | linked without them, plus a **health flag** "remote URL contains credentials" (suggest a credential helper); the value is never shown or logged |
| client | timeout, connection error, 5xx | retry | `Waiting` with backoff (GET is idempotent) |
| client | 429, or 403 with a rate-limit header | retry | `Waiting` until RetryAt; instance-wide `Limited` |
| client | 401, or 403 otherwise | domain outcome | `Blocked{unauthorized}`; UI: "the token for git.acme.dev was refused: check it in .env" |
| client | 404 | domain outcome | `Blocked{not_found}`; UI: "private repo? add a token" or "project not found on host" |
| client | 304 | domain outcome | `NotModified` |
| client | unparseable JSON | die for that fetch | treated as `transient` once and logged with the body truncated and redacted. If the parser keeps failing, the backoff caps at 10 min; that means a host API change, fixed in the client |
| core | invariant broken | die | recovered by the owner goroutine and logged; state rebuilt from config and targets |

Error translation: `net/http` and status codes → `FailKind` inside the client package. The service and core never see `*http.Response` or `url.Error`. The API reports phases, not errors.

## Requirements

| Capability | Live | Test | Contract |
|---|---|---|---|
| `HostClient` (`RepoState(ctx, inst, token, project, branch, etag) (RepoState, etag, error)`, `Inbox(ctx, inst, token) ([]InboxItem, error)`; errors are `*hosts.FetchError{Kind, RetryAt}`) | `gitlab`, `github`, `gitea` packages over `net/http` | `hoststest.Fake` (scripted per key) | `HostClientContract(t, client, server)`: open MR and success pipeline; no MR; draft MR; merged MR; failed pipeline; no pipeline; 304 with ETag; 401 → unauthorized; 404 → not_found; 429 with Retry-After → rate_limited with RetryAt; 403 with rate-limit headers → rate_limited; 500 → transient; inbox roles. Runs against the fake and against each live client pointed at an `httptest.Server` replaying recorded fixtures. A real-host run is opt-in only (`YGG_LIVE_HOST_TESTS=1` plus a token; manual, never in CI) |
| `RemoteReader` (`RemoteURL(ctx, path, name)`) | `internal/git` (new read-only op) | map fake | covered by git tests (temp repos with remotes) |
| `ConfigSource` (the `.env` file and named `YGG_HOST_*` process variables) | file read + `os.LookupEnv` for named keys only (never `os.Environ`, which archtest bans outside `internal/safety`) | in-memory | — |
| `Publisher`, `Clock` | hub, shell | recorder, fixed | — |

## Persistence and delivery

- **Nothing persisted.** Host data lives in memory, and config comes from `.env`. Migration 00003 adds **no** host tables: instances come from config, and the repo → instance link is derived from the remote URL each time.
- **Timers:** the 15 s `Tick` ticker owned by the service. `Waiting.Until` and `Limited` are checked on each tick, so a late tick only delays a retry.
- **Delivery:** one owner goroutine, a channel for inputs, workers that only send results back. Cancelling the service context cancels the HTTP requests.
- **Tokens (invariants 3 and 4):**
  - tokens live in a shell-only map, are added to the redactor at load, and are sent only in request headers;
  - they are never put in the process env, so `safety` can't pass them to children, and the clean env drops `*TOKEN*` names anyway;
  - they never go in a URL, git config, log, audit entry or API response;
  - `/api/hosts` reports only "token configured: yes/no".
- **Shell invariants (chaos):**
  - S1: no request other than GET is ever sent (enforced by the client wrapper and pinned by a test on the wrapper);
  - S2: a token never appears in any captured log line, WS message, API response or error text (test with a sentinel token across the whole service run, including failures);
  - S3: per instance, never more than 4 requests are in flight.

## Boundary

| Source | Parser | Produces | Rejects |
|---|---|---|---|
| git remote URL | `hosts.ParseRemote(raw)` | `RemoteRef{Host, Port, Project ProjectPath, HadCredentials bool}` | file paths and `file://`; schemes other than https, ssh, git+ssh, scp-like `user@host:path`; empty project path; `..` segments |
| `.env` / environment | `hosts.ParseConfig(lines)` | `[]InstanceConfig{ID, Kind, Base, TokenRef}` + secrets | unknown kind, base URL not https (http allowed only for `localhost`), duplicate instance, malformed line (reported by number) |
| GitLab JSON (MRs, pipelines, inbox) | `gitlab.parseMRs`, `parsePipelines` | core payload | missing required fields, unknown state values (mapped to `unknown` for pipelines; an MR with an unknown state is rejected) |
| GitHub JSON (pulls, check-runs + combined status, search) | `github.parse*` | core payload | same |
| Gitea JSON (pulls, commit status) | `gitea.parse*` | core payload | same |
| response headers | `hosts.ParseRetryAt(h, now)` | `*time.Time` | negative or unparseable values → nil (falls back to 60 s) |
| HTTP `POST /api/hosts/refresh` | `hosts.ParseRefresh` | keys | unknown repo or instance |

Config format (documented in `.env.example`, every line optional):

```
# github.com and gitlab.com work without any line here (public repos, low rate limits).
YGG_HOST_1=gitlab https://git.acme.dev
YGG_HOST_1_TOKEN=            # read_api scope is enough
YGG_HOST_2=github https://github.com
YGG_HOST_2_TOKEN=            # fine-grained: Pull requests (read), Commit statuses (read), Actions (read)
YGG_HOST_3=gitea https://code.example.org
```

Each parser gets:

- round-trip: `ParseRemote(Format(r)) == r` for the https and scp-like forms, and the same for config;
- accepted ⇒ valid;
- one test per rejection rule;
- a fuzz target: `FuzzParseRemote`, `FuzzParseConfig`, `FuzzParseRetryAt`, and one per JSON parser.

## Files

- **Core, `internal/core/hosts`:**
  - `types.go` (Instance, Key, Payload, MergeRequest, Pipeline);
  - `poller.go` (`Step`, `Run`, `FailedPipelines`, `Links`);
  - `poller_prop_test.go` (H1–H9), written first.
- **Shell, `internal/hosts`:**
  - `service.go`;
  - `config.go` (`LoadConfig`, `ParseConfig`);
  - `remote.go` (`ParseRemote`);
  - `fetcherr.go` (`FetchError`, `ParseRetryAt`, the GET-only client wrapper);
  - `hoststest/` (fake + `HostClientContract`).
- **Clients:** `internal/hosts/{gitlab,github,gitea}/client.go` and `parse.go`, with `testdata/` recorded fictional fixtures (`acme-shop/shop-api` on `git.acme.dev`).
- **`internal/git`:** `RemoteURL` (read-only).
- **`internal/api`:** `hosts.go`, `types_hosts.go`, and the `HostsTopic` variant.
- **Root:** `.env.example`.

## Open questions

Each question has a recommendation; the design above assumes it.

1. **Where `.env` lives:** `YGG_ENV_FILE` if set, otherwise `<data dir>/.env`. A `.env` in the clone folder is not read, because it sits in a git repo and might get committed. *Recommend the data dir*, and `ygg doctor` prints the path it reads.
2. **Config format:** numbered `YGG_HOST_n=<kind> <base URL>` plus `YGG_HOST_n_TOKEN`. github.com and gitlab.com are known without config. *Recommend as shown.*
3. **GitHub "pipeline":** the aggregate of check runs plus the combined commit status for the branch head. Any failure → `failed`; any pending → `running`; all success → `success`. *Recommend as stated.*
4. **Reloading `.env`:** on demand from settings ("reload"), not by watching the file. *Recommend as stated.*
