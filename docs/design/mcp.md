# MCP server — Model Functional Effect design
Profile: service

Covers MCP-01 to MCP-06 and SF-02 (only tracked repos are visible), invariants 2, 4, 6, 7, 8, 11.

The MCP server is how a running agent reads the workspace, starts builds, fetches and pulls, and proposes changes. It is a trust boundary: the caller is an agent that may have read hostile text.

Core: `internal/core/mcp` (pure; no JSON tags on state types).
Shell: `internal/mcp` (server, session registry, tool implementations), `internal/api/mcp.go` (mounts `/mcp`), adapters (write the MCP config for their CLI).

Most of this feature is stateless request/response. The decisions that are worth pure code are: which tools a task is offered, how a call is parsed into a typed value, and the call-rate limit. The session registry is a shell capability with a contract suite.

## State map

| Piece | State |
|---|---|
| Session (shell registry) | `Active{Task, Attempt, ExpiresWithRun}` or absent. Issued when a run starts, revoked when it ends. No other states. |
| Limiter (core) | `Bucket{Tokens, Last time.Time}` per session |
| Offered tools (core) | none: a function of `Context` |

```go
type Context struct {
    Task        core.TaskID
    Mode        core.TaskMode          // of the current turn
    Scope       []core.RepoID          // repos the task may act on
    Project     []core.RepoID          // tracked repos of the task's project (not untracked)
    HasWorktree map[core.RepoID]bool
    Caps        agents.Capabilities    // via a core-side copy
}
type Tool string // closed set, below
```

## Tools

| Tool | MCP | Reads or acts on | Offered when |
|---|---|---|---|
| `list_repos` | 01 | project metadata (name, id, default branch) | always |
| `repo_status` | 01 | branch, dirty counts, ahead/behind of a project repo | always |
| `remote_status` | 01 | MR/PR and pipeline status of a project repo (cached host data) | always |
| `recent_commits` | 01 | log of a **scope** repo | always |
| `changed_files` | 01 | changed paths of a **scope** repo (its worktree if any) | always |
| `get_diff` | 01 | diff of a **scope** repo, size-capped | always |
| `build_info` | 02 | detected toolchains and commands of a project repo | always |
| `build_order` | 02 | dependency order of the project's repos | always |
| `list_build_presets` | 02 | presets | always |
| `list_builds` | 02 | recent builds (id, status, repos) | always |
| `build_log` | 02 | log of a build whose repos are all in **scope** | always |
| `start_build` | 02 | enqueue a build of **scope** repos (task's worktree if it has one, else the main checkout) | always (AG-06 allows builds in Ask) |
| `run_build_preset` | 02 | run a preset whose repos are all in **scope** | always |
| `list_dev_servers` | 03 | none running (stub until Phase 5) | always |
| `dev_server_log` | 03 | "no such dev server" until Phase 5 | always |
| `fetch` | 04 | `git fetch` of a **scope** repo | always |
| `pull` | 04 | fast-forward-only pull of a **scope** repo's main checkout | not in Ask mode (invariant 9); otherwise always, see open question 1 |
| `propose_commit` | 05 | creates a `Commit` proposal | the repo has a task worktree, or mode is `current_branch` |
| `propose_push` | 05 | `Push` | same |
| `propose_merge_request` | 05 | `MergeRequest` | same, and the remote is GitLab |
| `propose_install` | 05 | `Install` | always |
| `propose_agent_file` | 05 | `AgentFile` (the agent-neutral name for `propose_claude_file`) | always |
| `propose_delete_files` | 05 | `DeleteFiles` (moves to `_to_delete/`) | always |

Not registered in Phase 3 (decision 6): `propose_publish`, `propose_subtasks`. `list_dev_servers` and `dev_server_log` answer from a `DevServers` interface whose Phase 3 implementation reports none.

**Builds in Ask mode (AG-06):** PRD AG-06 lists "triggering builds" as part of Ask mode, so `start_build` and `run_build_preset` are offered and authorized in Ask mode; this is intended, not an oversight of invariant 9. Build output is not covered by SF-07: the integrity snapshot (`internal/git/snapshot.go`) fingerprints HEAD, `git status --porcelain=v2` with untracked files, the tracked diff against HEAD, the content of untracked files that are not ignored, and all refs. Ignored files are excluded, so artifacts a build writes into the checkout (`target/`, `dist/`, `node_modules/` and the like, normally gitignored) land outside the snapshot's scope and do not trip the check. A build that writes a non-ignored file does change the snapshot and is reported as an integrity violation by the existing check; no behaviour changes for this.

**Visibility rule (SF-02, invariant 6):** the visible set is the task's project repos that are tracked and not untracked. *Metadata* tools (names, status counts, build order, presets, host status) may name any visible repo. *Content* tools (commits, changed files, diffs, build logs) and *action* tools (builds, fetch, pull, proposals) are limited to the task's scope, which is the same scope the guard gives the agent for reading files. Every repo argument is resolved against the visible set by name or ID; anything else is "no such repo" (the same answer for untracked, unknown and out-of-scope, so the agent cannot probe).

## Inputs

| Input | Fields |
|---|---|
| `Call` (parsed, typed, one variant per tool) | e.g. `GetDiff{Repo, Path?, MaxBytes}` |
| `Tick` for the limiter | `Now` (from the shell) |

Results are values built by the shell and passed back to the agent: `Result{Text string, Truncated bool}` or `ToolError{Message}`.

## Transition table

### `Offered(Context) []Tool` and `Authorize(Context, Tool) Decision`

| Condition | Result |
|---|---|
| tool is in the table above and its "Offered when" holds | offered / allowed |
| tool needs a worktree (`propose_commit/push/merge_request`) and the repo has none and mode ≠ `current_branch` | not offered; `Authorize` returns `Denied{NoWorktree}` |
| tool unknown | `Denied{Unknown}` |
| any argument repo not visible or (for content/action tools) not in scope | `Denied{NoSuchRepo}` |
| `Context.Task` has no live run | `Denied{NoSession}` |

### `StepLimiter(Bucket, Tick) (Bucket, Allowed)`

| From | Input | Guard | To | Result |
|---|---|---|---|---|
| `Bucket` | `Tick{Now}` | refilled tokens ≥ 1 | tokens − 1 | `Allowed` |
| `Bucket` | `Tick{Now}` | otherwise | refilled | `Limited{RetryAfter}` |

Refill is `rate × elapsed`, capped at `Burst`. Defaults: burst 30, rate 10 calls/s per session, and at most 4 calls in flight (a shell semaphore). Any other input: no change.

### Session registry (shell)

| Operation | Effect |
|---|---|
| `Issue(task, attempt)` | new random token (32 bytes, base64url); only its sha256 is kept; added to the redactor |
| `Lookup(token)` | constant-time compare; returns `Active` or not found |
| `Revoke(task)` | called when the run finishes (also on panic and on recovery); later lookups fail |

## Invariants

| # | Invariant | Test |
|---|---|---|
| M1 | A tool is offered iff its rule holds; `Authorize` agrees with `Offered` for every (context, tool) | `TestPropOfferedMatchesAuthorize` |
| M2 | No offered tool, with any arguments, can name a repo outside the visible set; content and action tools cannot name one outside scope | `TestPropRepoArgsResolveInsideScope` |
| M3 | The answer for an untracked, an unknown and an out-of-scope repo is identical | `TestPropNoRepoOracle` |
| M4 | Every `propose_*` result is a proposal in `pending`; no tool can confirm, reject or execute | `TestToolsHaveNoConfirm` (the tool set is closed; asserted over the registered list) |
| M5 | The limiter never allows more than `Burst + rate × elapsed` calls in any window | `TestPropLimiterBound` |
| M6 | A revoked or unknown token never authenticates; a token is valid only for its own task | `TestSessionContract` (registry contract) |
| M7 | A tool result never contains a string the redactor would mask, nor an absolute path of the machine | `TestPropResultsAreRedactedAndRelative` |
| M8 | Every tool that changes state (`start_build`, `run_build_preset`, `fetch`, `pull`, `propose_*`) writes an audit entry in the transaction that changes the state | `TestMutatingToolsAudited` |
| M9 | Result text is capped; the cap is flagged as `Truncated` | `TestPropResultCap` |

## Commands

The pure part emits no commands. The shell does the work through existing services, each of which already follows the model:

| Tool group | Service called | Notes |
|---|---|---|
| repos, status, history | `repos` status/detail readers (Phase 2) | read-only |
| builds | `builds.Service` | `Enqueue` returns a build ID; no waiting inside the call |
| fetch / pull | `repos` actions service (git-actions design) | same guards as the UI: refused when a build or an editing task holds the main checkout (`mainlock`); audited |
| propose_* | `proposals.Service.Create` | the only way an agent changes anything |
| host status | `hosts` cache | no network call from a tool |

## Call graph

```go
// Production
agent CLI (child of the task job) → POST http://127.0.0.1:<port>/mcp   // streamable HTTP, Authorization: Bearer <token>
  → api middleware: Host check (DNS rebinding); /mcp is exempt from the browser-Origin rule
     but a request that carries an Origin header is refused (an MCP client is not a browser)
  → mcp.Authenticate(token) → session → Context (tasks service: task, mode, scope, worktrees; repos: visible set)
  → limiter: StepLimiter(bucket, Tick{Now}) → Limited ⇒ MCP error "slow down", nothing else runs
  → mcp.ParseCall(name, args)            // core: typed Call, unknown fields rejected
  → mcp.Authorize(Context, tool)         // core
  → tool implementation → service (table above) → result
  → redact(result) → cap → relative paths only → respond
  → mutating tools: Audit entry committed with the state change (done by the service)

// Run start / end
tasks.interpret StartRun
  → sessions.Issue(task, attempt) → token, redactor.Add(token)
  → adapter.MCPConfig(token, url) → file in the run's temp dir (never on a command line)
     Claude Code: `--mcp-config <file> --strict-mcp-config`
tasks.interpret RunFinished / recovery
  → sessions.Revoke(task)
```

```go
// Tests
ParseCall / Offered / StepLimiter                 // core, no shell
tool implementations → fake services (reposfake, buildstest, proposals fake) + dbtest
HTTP: httptest server + the real middleware; sessions in memory
```

## Failures

| Node | Failure | Class | Handling |
|---|---|---|---|
| Authenticate | no/invalid/revoked token | domain outcome | HTTP 401 with an empty body; no detail; rate-limited per source by the middleware |
| Host header wrong | | domain outcome | 403 (existing guard) |
| `Origin` header present | | domain outcome | 403 |
| ParseCall | unknown tool, unknown field, wrong type, value over a limit | domain outcome | MCP `invalid params` with a plain message ("path must be relative") |
| Authorize | repo not visible / not in scope / no worktree | domain outcome | tool result `isError` with the identical message for all repo-visibility cases |
| Limiter | over rate | domain outcome | tool result `isError` "Too many calls; wait a moment" |
| Service errors | not found, conflict (build running), invalid | domain outcome | `apperr` user message in the tool result; internal errors never leave the process |
| Service | unexpected error | die | generic "That tool failed. See the Yggdrasil log."; logged with the call ID |
| Tool handler | panic | die | recovered; generic message; the session stays valid |
| Long-running tool | client disconnects | escape hatch | the request context cancels; services stop (builds already run on their own context; the call only enqueued) |
| Run ends while a call is in flight | | escape hatch | `Revoke`; the in-flight call finishes against its own context and its result is dropped |

## Requirements

| Capability | Live | Test | Contract |
|---|---|---|---|
| `Sessions` (`Issue`, `Lookup`, `Revoke`) | `internal/mcp` registry | same type (no I/O) | `SessionsContract`: token works for its task only; revoked fails; hash only is stored; two tasks get different tokens; lookup is constant-time shaped (no early exit on prefix, checked by a timing-free structure test); `Revoke` of an unknown task is a no-op |
| `Workspace` (visible repos, scope, worktrees for a task) | built from `repos` + `tasks` | `mcptest.FakeWorkspace` | `WorkspaceContract`: untracked repos never appear; scope ⊆ visible; a repo untracked mid-run disappears on the next call |
| `Repos`, `Builds`, `Actions`, `Proposals`, `Hosts`, `DevServers` | the services named above | existing fakes + `mcptest.FakeDevServers` | their own existing suites |
| `Redactor` | `internal/safety` | fixed secrets | existing |
| `Clock` | real | fixed | none |

## Boundary

| Source | Parser | Produces | Rejects |
|---|---|---|---|
| HTTP request | middleware + `mcp.Authenticate` | `Context` | bad Host, any Origin, no/bad bearer, body over 1 MiB |
| Tool name + JSON arguments | `mcp.ParseCall` | typed `Call` | unknown tool; unknown fields; repo not a name/ID shape; `MaxBytes` over the cap; relative-path rules from the proposals design for paths; build step lists over 50 |
| Adapter MCP config | `adapter.MCPConfig` | file | token or URL containing quotes/newlines (never happens; asserted) |

Boundary properties: round trip (`ParseCall(FormatCall(c)) == c`), accepted ⇒ valid, one test per rejection rule, and fuzz targets (`FuzzParseCall`: accepted ⇒ no repo or path escapes; `FuzzAuthorizationHeader`).

## Files

Core (`internal/core/mcp`): `doc.go`, `tool.go` (closed `Tool` set, `Context`), `offered.go` (`Offered`, `Authorize`), `call.go` (sealed `Call` variants, `ParseCall`, `FormatCall`), `limiter.go`, `offered_prop_test.go` (M1, M2, M3, M4), `limiter_prop_test.go` (M5), `call_test.go`, `fuzz_test.go`.

Shell:
- `internal/mcp`: `server.go` (official Go MCP SDK, streamable HTTP handler), `sessions.go`, `workspace.go`, `tools_read.go`, `tools_build.go`, `tools_git.go`, `tools_propose.go`, `redact.go`, `mcptest/`, shell tests (M6–M9, concurrent calls, revoke during a call, revoked token after the run ends).
- `internal/api/mcp.go`: mounts `/mcp` with its own middleware chain.
- `internal/agents/claudecode`: writes the MCP config (small, owned by C1/M1 hand-off in the plan).
- Dependency: `github.com/modelcontextprotocol/go-sdk` (pure Go; confirm the licence with `task license` before the dependency is added and justify it in the PR). If its HTTP handler cannot take per-request authentication, wrap it with our middleware; if it still cannot, the fallback is a small JSON-RPC handler of our own for the three methods needed (`initialize`, `tools/list`, `tools/call`).

## Open questions

1. **`pull` and invariant 2.** Invariant 2 says every mutation of a main checkout is a proposal. A fast-forward-only pull changes a main checkout's working tree. MCP-04 and the Phase 2 decision ("agents only get fetch/pull through MCP") allow it directly; the user-initiated UI pull is already direct. Recommendation: keep `pull` as a direct tool but only (a) fast-forward-only, (b) on a clean tree, (c) when no build or editing task holds the main checkout, (d) audited with the old and new commit; and record this as the one explicit reading of invariant 2 in the threat model. Alternative: make `pull` a proposal (safer, changes MCP-04). **This one needs your decision because it sits on a safety invariant.**
2. **Visibility split:** metadata for the whole project, content and actions for the task's scope. Recommendation: as written.
3. **Proposal tools by mode:** commit/push/MR need a worktree (or current-branch mode); agent file, delete and install are offered everywhere. Recommendation: as written.
4. **`--strict-mcp-config`** for Claude Code: only Yggdrasil's MCP server loads in a task, so the user's own global MCP servers are not available to tasks. Recommendation: yes (safer; a repo's `.mcp.json` is trust-gated anyway). Say if you want user-level servers allowed.
5. **Limits:** 30 burst, 10/s, 4 in flight, 64 KiB per result, 1 MiB request. Recommendation: as written; tune after use.
6. **SDK vs own handler:** official SDK first (the stack names it), own minimal handler as the fallback. Recommendation: as written.

## Decisions (approved 2026-10-25)

(1) `pull` stays a direct tool with the four conditions (ff-only, clean tree, no build or editor holds the checkout, audited with old and new commit); the threat model records it as the one explicit reading of invariant 2. Ask mode is excluded from this exception: `pull` is neither offered nor authorized there, because it changes a working tree; `fetch` stays (it updates remote refs only). (2)-(6) recommendations accepted as written.

## Review gate 1 fixes (2026-10-25)

- **Limiter (C5, S2).** `StepLimiter` denies a zero `Tick.Now` and changes nothing; a backwards tick refills nothing and never moves `Last` back. `TestPropLimiterBound` checks M5 over random forward and backward schedules.
- **M4 test name.** The test is `TestToolsHaveNoConfirm`, as in the table above.
- **Enforcement (S6).** `mcp.Caps.Enforcement` is the typed `mcp.Enforcement` (`hook`, `limited`) with `Validate`; the shell converts from `agents.Enforcement` (core packages do not import each other).
- Offered, Authorize, ParseCall and FormatCall are implemented in the core (`call.go`, `check.go`; `CheckCall` holds the rules ParseCall applies). Defaults: `limit` 20, `max_bytes` 64 KiB.
