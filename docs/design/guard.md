# Guard — Model Functional Effect design
Profile: service

Covers:

- AG-06 to AG-09: what each mode may do. AG-10: which mode a turn may use.
- SF-06: path guard and command allowlist, enforced by Yggdrasil. Invariants 5, 6, 10, 11.
- SF-07: Ask-mode integrity check. Invariant 9.
- SF-09: repo trust for repo-supplied agent config. Invariant 10, D17.

Core: `internal/core/guard` (no JSON tags; wire DTOs live in `internal/api`).
Shell: `internal/safety` (path resolution calls, scan, snapshot, policy file), `internal/agents/<adapter>` (turns a tool call into a `Call`), `internal/tasks` (interprets `StopTask`).

Four pure pieces, each its own file, none with hidden state:

| Piece | Kind | Used by |
|---|---|---|
| `Decide(Policy, Call) Verdict` | stateless decision | the hook process, once per tool call |
| `Admit(Request) Admission` | stateless decision | the tasks service, when a turn is created |
| `Integrity` + `StepIntegrity` | state machine, per Ask turn | the tasks service |
| `Trust` + `StepTrust` | state machine, per repo | the trust API and the tasks service |

## State map

**Decide / Admit** have no state. Their inputs are values.

**Integrity** (one per Ask turn; also used for any turn the policy marks read-only):

| State | Data |
|---|---|
| `Idle` | nothing |
| `Arming` | turn ID, `Before` requested |
| `Armed` | turn ID, `Before Snapshot` |
| `Checking` | turn ID, `Before`, `After` requested |
| `Clean` | turn ID (terminal) |
| `Violated` | turn ID, `Changes []Change` (terminal) |
| `Unverifiable` | turn ID, why (terminal; counts as a violation) |

**Trust** (one per repo):

| State | Data |
|---|---|
| `NoConfig` | the repo ships no agent config (nothing to trust) |
| `Untrusted` | `Found Scan` (hash, files, what each would run), `Why` (`NeverTrusted` / `ConfigChanged{Was}` / `Revoked`) |
| `Trusted` | `Hash`, `At` |

A repo with no agent config needs no trust: `NoConfig` allows every mode. A repo with config is `Untrusted` until the user trusts the exact hash they were shown.

## Inputs

**Decide** takes a `Call` (already parsed and resolved by the shell):

```go
type Call struct {
    Tool     Tool        // closed set: Read, Search, Write, Exec, Subagent, Other(name)
    Paths    []PathRef   // every path the call names, resolved by the shell
    Globs    []string    // glob patterns (Search, Glob)
    Argv     []string    // Exec: the parsed command line
    ParseErr bool        // Exec: the command line had shell syntax we refuse
    Isolation string     // Subagent: "" or the agent-native isolation it asked for
}
type PathRef struct {
    Canon    string  // long form, symlinks and junctions resolved, absolute
    OK       bool    // false: the shell could not resolve it (treat as outside)
    Raw      string  // as the agent wrote it, for the audit entry only
}
```

**Policy** (built by the tasks service, written to the hook's config file):

```go
type Policy struct {
    Mode       core.TaskMode
    ReadRoots  []string      // canonical, absolute
    WriteRoots []string      // empty in Ask mode
    Allow      []CommandRule // the command allowlist for this task
    FoldCase   bool          // Windows: compare paths case-insensitively
    Enforcement Enforcement  // hook | limited (from the adapter)
}
type CommandRule struct {
    Program  string   // base name without extension: "go", "npm", "mvnw"
    Args     []string // fixed leading arguments: ["run","test"]
    Tail     TailRule // None | Paths (extra args allowed; each must be a path inside ReadRoots or a plain word without "-")
}
```

**Admit** takes `Request{Mode, Previous *core.TaskMode, OptIn bool, Trust TrustView, Enforcement, AdapterCaps}`.

**StepIntegrity** inputs: `TurnStarts{Turn, Mode}`, `SnapshotTaken{Turn, Phase, Snap}`, `SnapshotFailed{Turn, Phase, Reason}`, `TurnEnds{Turn}`.

**StepTrust** inputs: `Scanned{Repo, Scan, Now}` (the shell read the repo), `UserTrusts{Repo, Hash, Now}`, `UserRevokes{Repo, Now}`.

## Transition table

### Decide (first matching row wins; evaluated top to bottom)

| # | Condition | Verdict |
|---|---|---|
| D1 | Tool is not in the closed set, or `Policy.Mode` is unknown | Deny `UnknownTool` |
| D2 | any `PathRef.OK == false` | Deny `PathUnresolved` |
| D3 | Tool is `Read` or `Search`; every path (and every glob after cleaning) is inside a `ReadRoot` | Allow |
| D4 | Tool is `Read` or `Search` otherwise | Deny `OutsideRepo` |
| D5 | Tool is `Write` and `Mode == ask` | Deny `AskIsReadOnly` |
| D6 | Tool is `Write` and a path has a `.git` segment or names agent config (`AgentConfigName`) | Deny `ProtectedPath` (agent config changes are proposals, invariant 2) |
| D7 | Tool is `Write`, every path inside a `WriteRoot` | Allow |
| D8 | Tool is `Write` otherwise | Deny `OutsideWorktree` |
| D9 | Tool is `Exec` and `Mode == ask` | Deny `AskIsReadOnly` |
| D10 | Tool is `Exec` and `ParseErr` | Deny `ShellSyntax` (pipes, `&&`, redirects, `$()`, backticks, newlines) |
| D11 | Tool is `Exec`; `Call.Program` is set, `OK`, and in no `WriteRoot`; and some `CommandRule` matches `Argv` (`argv[0]` is the exact bare name or the name plus `.exe`, fixed args, tail rule) | Allow |
| D12 | Tool is `Exec` otherwise (`Program` nil or inside a write root, no rule matches). `Program` not `OK` is `PathUnresolved` | Deny `NotAllowlisted` |
| D13 | Tool is `Subagent` with `Isolation != ""` | Deny `AgentWorktreeBlocked` (AG-22) |
| D14 | Tool is `Subagent` | Allow (depth and concurrency are K1's and the hook's caps) |
| D15 | anything else | Deny `UnknownTool` |

A `Deny` carries a `Reason` code and a plain-language message with no path in it (the model and the transcript see it).

Paths are compared with `FoldCase`. A path equals a root or is below it; `Canon` is already free of `..`.

### Admit

| Mode asked | Condition | Result |
|---|---|---|
| any | any view in `Trust []TrustView` (one per repo the mode touches) is not `None`/`Trusted`, or the list is empty | Admit only `ask`; every other mode `Refused{RepoUntrusted}` |
| `ask` | always | Admit |
| `code` | `Enforcement` is `hook` or `limited` | Admit (limited = worktree-only, which is what `code` is) |
| `current_branch` | `OptIn` and `Enforcement == hook` | Admit with `Warning{EditsMainCheckout}` |
| `current_branch` | not `OptIn` | `Refused{NeedsOptIn}` |
| `current_branch` | `Enforcement == limited` | `Refused{LimitedEnforcement}` |
| `project` | `OptIn` and `Enforcement == hook` | Admit with `Warning{EditsAllRepos}` |
| `project` | otherwise | `Refused` as above |
| switch (`Previous != nil`) | new mode not in `{ask, *Previous-if-writing}`, i.e. a turn can go Ask ↔ the task's write mode and nothing else | `Refused{ModeSwitchNotAllowed}` |

### StepIntegrity

| From | Input | Guard | To | Commands |
|---|---|---|---|---|
| `Idle` | `TurnStarts{t, mode}` | `mode` is read-only | `Arming{t}` | `TakeSnapshot{t, Before}` |
| `Idle` | `TurnStarts{t, mode}` | mode writes | `Idle` | none (no check for a writing turn) |
| `Arming{t}` | `SnapshotTaken{t, Before, s}` | | `Armed{t, s}` | none (the run may start) |
| `Arming{t}` | `SnapshotFailed{t, Before}` | | `Unverifiable{t}` | `StopTask{t, CannotVerify}`, `Audit` |
| `Armed{t,b}` | `TurnEnds{t}` | | `Checking{t,b}` | `TakeSnapshot{t, After}` |
| `Checking{t,b}` | `SnapshotTaken{t, After, a}` | `a == b` | `Clean{t}` | `Audit` |
| `Checking{t,b}` | `SnapshotTaken{t, After, a}` | `a != b` | `Violated{t, Diff(b,a)}` | `StopTask{t, TreeChanged}`, `FlagTask`, `Audit` |
| `Checking{t,b}` | `SnapshotFailed{t, After}` | | `Unverifiable{t}` | `StopTask{t, CannotVerify}`, `Audit` |

Any other (state, input) pair, including any input for a different turn ID: no change, no commands. There is no revert command (SF-07: no automatic revert).

`Snapshot` is `{Head string, Status string, DiffHash string}`: the commit, a hash of `git status --porcelain=v2 -z`, a hash of the working diff. `Diff(b, a)` lists changed paths the shell attached to `After` (`Snapshot.Paths`), so the user sees what changed.

### StepTrust

| From | Input | Guard | To | Commands |
|---|---|---|---|---|
| `NoConfig` | `Scanned{s}` | `s.Empty()` | `NoConfig` | none |
| `NoConfig` | `Scanned{s}` | not empty | `Untrusted{s, NeverTrusted}` | `Audit{TrustRequired}` |
| `Untrusted{f}` | `Scanned{s}` | `s.Empty()` | `NoConfig` | none |
| `Untrusted{f}` | `Scanned{s}` | not empty | `Untrusted{s, same Why}` | none (refreshes what the user will see) |
| `Untrusted{f}` | `UserTrusts{h}` | `h == f.Hash` | `Trusted{h, now}` | `PersistTrust{repo, h}`, `Audit{Trusted, files}` |
| `Untrusted{f}` | `UserTrusts{h}` | `h != f.Hash` | `Untrusted{f}` | `Audit{TrustStale}` (the user trusted something other than what the repo has now) |
| `Trusted{h}` | `Scanned{s}` | `s.Hash == h` | `Trusted{h}` | none |
| `Trusted{h}` | `Scanned{s}` | `s.Empty()` | `NoConfig` | `ClearTrust{repo}`, `Audit` |
| `Trusted{h}` | `Scanned{s}` | `s.Hash != h` | `Untrusted{s, ConfigChanged{h}}` | `ClearTrust{repo}`, `Audit{TrustLost}` |
| `Trusted` | `UserRevokes` | | `Untrusted{current scan, Revoked}` | `ClearTrust`, `Audit` |
| `NoConfig` | `UserTrusts` / `UserRevokes` | | `NoConfig` | none |

The tasks service runs `Scanned` before every turn that is not Ask, and when a repo is opened in the UI.

## Invariants

Property tests (random inputs, checked after every step or on every output):

| # | Invariant | Test |
|---|---|---|
| G1 | A `Write` is never allowed outside every `WriteRoot`, in any mode, for any path form | `TestPropWriteNeverOutsideRoots` |
| G2 | In `ask` mode no `Write` and no `Exec` is allowed | `TestPropAskNeverWritesOrExecs` |
| G3 | An `Exec` is allowed only when a rule matches `Argv` and `ParseErr` is false | `TestPropExecOnlyAllowlisted` |
| G4 | A path with `OK == false` never gives `Allow` | `TestPropUnresolvedDenied` |
| G5 | Writes under `.git` or to agent config files are denied in every mode | `TestPropProtectedPathsDenied` |
| G6 | A path that is a sibling by prefix (`C:\work\repo-evil` vs root `C:\work\repo`) is outside | `TestPropPrefixSiblingOutside` |
| G7 | `Decide` is deterministic and does not change its inputs | `TestPropDecideIsPure` |
| G8 | `Deny` messages contain no `Canon` or `Raw` text | `TestPropDenyMessageHoldsNoPath` |
| G9 | `Subagent` with any isolation is denied | `TestPropSubagentIsolationDenied` |
| A1 | An `Untrusted` repo never admits a mode other than `ask` | `TestPropUntrustedOnlyAsk` |
| A2 | `current_branch` and `project` are never admitted with `limited` enforcement or without opt-in | `TestPropWideModesNeedHookAndOptIn` |
| A3 | A switch never leaves `{ask, previous write mode}` | `TestPropSwitchStaysNarrow` |
| I1 | `Violated`/`Unverifiable` always come with a `StopTask`; `Clean` has none | `TestPropIntegrityStopsOnChange` |
| I2 | Equal snapshots never give `Violated`; unequal always do | `TestPropIntegrityEqualIffClean` |
| I3 | States `Clean`, `Violated`, `Unverifiable` are terminal: no input changes them | `TestPropIntegrityTerminal` |
| I4 | No step emits a command that writes to the tree (the command set has none) | `TestIntegrityCommandsAreReadOnly` |
| T1 | `Trusted{h}` is reached only from `Untrusted` whose `Found.Hash == h` | `TestPropTrustOnlyTheShownHash` |
| T2 | A scan with a different hash from `Trusted` always leaves `Trusted` and emits `ClearTrust` | `TestPropChangedConfigLosesTrust` |
| T3 | Trusted ⇒ no scan seen since has a different hash (replay check) | `TestPropTrustMatchesLastScan` |

## Commands

| Command | Cardinality | Idempotent | Produces |
|---|---|---|---|
| `TakeSnapshot{Turn, Phase}` | one | yes (read-only) | `SnapshotTaken` / `SnapshotFailed` |
| `StopTask{Turn, Reason}` | one | yes with key `(turn, reason)` | none (tasks core gets `IntegrityViolated`) |
| `FlagTask{Turn, Changes}` | one | yes with key turn | none |
| `PersistTrust{Repo, Hash}` | one | yes (set to the same value) | none |
| `ClearTrust{Repo}` | one | yes | none |
| `Audit{Entry}` | one | yes with key (subject, action, at) | none; committed in the same transaction as the state it belongs to |

`Decide` and `Admit` return values, not commands. The caller writes the audit entry for a deny (action `guard.deny`, reason code, tool, no raw path).

## Call graph

```go
// Production: one tool call, in the agent's hook process
claude runs `ygg hook --event PreToolUse`             // child of the agent, inside the task job
  → read YGG_HOOK_CONFIG (short id)                    // missing/unreadable ⇒ deny
  → parsePolicy (boundary)                             // Policy file → guard.Policy
  → adapter.toCall(hookInput) (boundary)               // tool name + input → guard.Call
      → safety.ParseCommandLine(line)                  // pure helper in core/guard
      → platform.ResolvePath(root-aware) per path      // long form, links resolved
  → guard.Decide(policy, call)                         // core
  → exit 2 + reason, or exit 0
  → append log line (redacted)                         // hook log; the tasks service audits denies later

// Production: a turn starts and ends (Ask integrity)
tasks service: NewTurn
  → guard.Admit(request)                               // core
  → StepIntegrity(TurnStarts)                          // core
  → interpret TakeSnapshot(Before) → git status + diff hash (platform.Start via internal/git)
  → StepIntegrity(SnapshotTaken)
  → agents.Execute (run)
  → StepIntegrity(TurnEnds) → TakeSnapshot(After) → StepIntegrity(SnapshotTaken)
  → interpret StopTask / FlagTask / Audit              // one Store.Commit with the task state

// Production: trust
GET /api/repos/{id}/trust   → safety.Scan(dir, adapter.ConfigPaths()) → StepTrust(Scanned) → DTO
POST /api/repos/{id}/trust {hash} → StepTrust(UserTrusts) → PersistTrust + Audit in one Commit
```

```go
// Tests
guard.Decide(policy, call)               // table + property tests, no shell
StepIntegrity / StepTrust over input lists
interpret TakeSnapshot → safetytest.FakeSnapshotter (scripted snapshots)
interpret Scan        → safetytest.FakeScanner
```

## Failures

| Node | Failure | Class | Handling |
|---|---|---|---|
| hook: config file missing, unreadable or invalid | | die (deny) | exit 2, reason "The task's rules could not be loaded, so the call is blocked." Fail closed; `onFailure: "block"` backs it up |
| hook: path cannot be resolved | | domain outcome | `PathRef.OK=false` → D2 deny |
| hook: tool input is not valid JSON or has an unexpected shape | | domain outcome | `Call` with `Tool=Other` → D15 deny |
| hook: command line has shell syntax | | domain outcome | `ParseErr` → D10 deny |
| hook: process killed / timeout | | escape hatch | the CLI's `onFailure: "block"` blocks the call |
| `TakeSnapshot` git failure (lock, timeout) | | retry (read-only, idempotent), then domain outcome | retry once after 1 s (not for `ErrSnapshotTooLarge`, which goes straight to `SnapshotFailed`); second failure → `SnapshotFailed` → `Unverifiable` (stops the task, fail closed) |
| `Scan` file unreadable | | domain outcome | treated as config present with an unknown hash, repo stays `Untrusted` with `Why` set; message says which file |
| `PersistTrust` version conflict | | retry | reload, rerun `StepTrust` (the shell loop from the builds design) |
| `PersistTrust` other store error | | die | 500 to the API; no trust recorded |

Errors are translated at the shell edge: the hook prints only the `Reason` message; the API returns `apperr` classes; nothing carries a path to the model.

## Requirements

| Capability | Live | Test | Contract |
|---|---|---|---|
| `PathResolver` (`ResolveRoot(path) (Canon, error)`; `FoldCase() bool`) | `platform` (P1: long form, symlink/junction resolution, refuses ADS, device names, UNC/WSL) | `platformtest.FakePaths` (a map plus scripted errors) | `PathResolverContract`: `..` collapse, 8.3 = long, junction out of root resolves outside, missing leaf resolved via deepest existing parent, ADS and `CON` refused, case-fold flag. Real runs on Windows with temp dirs; fake runs everywhere |
| `Snapshotter` (`Snapshot(dir) (Snapshot, error)`) | `internal/git` (status + diff hash + HEAD) | `safetytest.FakeSnapshotter` (scripted sequences) | `SnapshotterContract`: identical tree ⇒ equal; edit, new untracked file, deleted file, mode change, new commit, CRLF flip each ⇒ unequal; ignored files ⇒ equal; error on a non-repo. Real git in `t.TempDir()` |
| `ConfigScanner` (`Scan(dir, paths) (Scan, error)`) | `internal/safety` (reads files, sha256 over sorted path+content) | `safetytest.FakeScanner` | `ScannerContract`: same files ⇒ same hash regardless of order; content, rename, added file each change the hash; empty set ⇒ `Empty()`; unreadable file ⇒ error naming it |
| `TrustStore` (`Load`, `Save` via `Tx`) | `internal/db` (`repos.trusted`, `repos.trust_hash`) | `dbtest` in-memory SQLite | existing store contract plus: save then load round trip; version conflict returns `ErrConflict` |

The hook process builds its own `PathResolver` from `platform`; nothing else runs in it.

## Boundary

| Source | Parser | Produces | Rejects |
|---|---|---|---|
| Policy file (JSON, written by the tasks service) | `guard.ParsePolicy` | `Policy` | unknown mode; relative or empty root; root with `..`; `WriteRoots` in `ask`; rule with empty program or a program holding a path separator; unknown tail rule; no `ReadRoots` |
| Claude Code hook input | `claudecode.toCall` | `Call` | not JSON; missing `tool_name`; `Read` without a path; unknown tool → `Other` (denied) |
| OpenCode tool event (later) | `opencode.toCall` | `Call` | same |
| Command line string (the agent's Bash/PowerShell input) | `guard.ParseCommandLine` | `Argv` or `ParseErr` | `&`, `\|`, `;`, `<`, `>`, backtick, `$(`, `%VAR%`, `^`, newline, unbalanced quote, empty |
| Scan of the repo's agent config | `safety.Scan` | `Scan{Hash, Files, WouldRun}` | file over 1 MiB; unreadable; not UTF-8 (hash still computed, `WouldRun` marks it opaque) |
| Request to switch mode / trust (API) | `api` DTO parsers | `Request`, `UserTrusts` | unknown mode; hash not 64 hex |

Boundary properties for each: round trip (`ParsePolicy(FormatPolicy(p)) == p`), accepted ⇒ valid (`Policy.Validate()`), one test per rejection rule, and a fuzz target (`FuzzParsePolicy`, `FuzzParseCommandLine` with the invariant "accepted ⇒ no shell metacharacter in any argv element and the join reparses to the same argv", `FuzzDecidePaths` with "Allow ⇒ every Canon has a root as path prefix at a separator boundary").

## Files

Core (`internal/core/guard`):
- `doc.go`, `types.go` (Policy, Call, PathRef, Verdict, Reason, CommandRule, Request, Admission)
- `decide.go`, `admit.go`, `command.go` (ParseCommandLine, rule matching), `policy.go` (ParsePolicy, FormatPolicy, Validate)
- `integrity.go` (state, input, command, `StepIntegrity`), `trust.go` (`StepTrust`, `Scan`)
- Property tests: `decide_prop_test.go` (G1–G9), `admit_prop_test.go` (A1–A3), `integrity_prop_test.go` (I1–I4), `trust_prop_test.go` (T1–T3), `policy_test.go`, `command_test.go`, `fuzz_test.go`.

Shell:
- `internal/safety`: `scan.go`, `snapshot.go` (uses `internal/git`), `policyfile.go`, `safetytest/` (fakes + contract suites).
- `internal/platform`: P1 `ResolveRoot*` and `PathResolverContract`.
- `internal/agents/claudecode`: `toCall` replaces `checkToolCall`; hook loads `Policy`.
- `internal/tasks`: interprets `TakeSnapshot`, `StopTask`, `FlagTask`.

## Open questions

1. **Allowlist tail arguments.** Default rules come from the toolchain's verify and build commands (for example `go vet ./...`, `npm run test`). Recommendation: a rule matches the fixed leading arguments exactly; extra arguments are refused unless the rule has `Tail=Paths`, which allows plain words and paths inside `ReadRoots` but nothing starting with `-`. That blocks `--config`/`--exec` style escapes and `npm run test -- --reporter=./evil.js`. Cost: agents can't pass flags like `-run TestX` until the user adds a rule. Alternative: allow flags for known-safe programs per toolchain. OK with the strict default?
2. **Where the user edits the allowlist.** Recommendation: per repo in settings (stored in `settings`), seeded from the toolchain defaults; no per-task override in Phase 3.
3. **Ask turns in a Code task's worktree** read the worktree only (not other repos of the project). Recommendation: yes, the same `ReadRoots` as the task's scope.
4. **Dev-tool processes inside allowlisted commands** (`npm run test` runs package scripts that can do anything). Recommendation: accept and record in the threat model: the allowlist limits what the agent can start, not what a repo's own scripts do. The task job still kills the tree, the clean environment holds no tokens, and the repo must be trusted. Agree?
5. **Protected agent-config names** (D6): `.claude/**`, `.mcp.json`, `CLAUDE.md`, `AGENTS.md`, `opencode.json`, `.opencode/**`, `GEMINI.md`. Writing to them is denied; the agent uses `propose_agent_file`. Anything to add or remove?

## Decisions (approved 2026-10-25)

All five recommendations accepted: (1) strict allowlist tails, `Tail=Paths` only; (2) allowlist per repo in settings, seeded from toolchain defaults, no per-task override; (3) Ask turns read the task's scope only; (4) repo scripts are out of the allowlist's reach, recorded in the threat model; (5) protected names as listed.

## Review gate 1 fixes

Changes made after the first review gate (all in `internal/core/guard`, tests first):

- **B1 command lines.** `ParseCommandLine` is a character allowlist: every word, bare or quoted, holds only `[A-Za-z0-9_@%+=:,./-]` (quoted words may also hold spaces). Backslash, `$`, braces, globs, parentheses, `!`, `~`, `#`, ANSI-C quotes, tabs inside quotes, non-ASCII and control characters are refused, as is a second `%` anywhere. `tailArgOK` judges the word through the same `wordAllowed` check before the leading `-`, `@`, `~` rule, so `go test \-exec=evil.sh` is refused. Quotes only group; they are never part of a word. A property test and a fuzz target compare accepted lines with an independent model of bash expansion (backslash, `$055`, braces, quote removal, globs, variables): accepted implies the shell word equals the judged word and no tail word is a flag.
- **B2 programs.** A rule matches only `argv[0]` equal to the bare name or the name plus `.exe` (no `.bat`, `.cmd`, `.com`). New `Call.Program *PathRef` is the executable the shell resolved with platform resolution (the working directory is never searched). D11 needs it set, `OK`, absolute, ordinary and in no `WriteRoot`; nil gives `NotAllowlisted`, not `OK` gives `PathUnresolved`.
- **B3 case folding.** Segment comparison and the protected-name check fold ASCII only (`asciiEqualFold`); non-ASCII segments match byte-exactly, so U+212A (Kelvin) and U+017F (long s) no longer match `k` and `s`.
- **M2 integrity.** A snapshot with an empty `Head`, `Status` or `DiffHash` is `Unverifiable`, never `Clean`. New input `TurnAborted{Turn}`: `Arming` goes back to `Idle` (the run never started); `Armed` and `Checking` become `Unverifiable` with an audit entry and **no** `StopTask` (the turn is already dead; the tree was not checked). `TurnStarts` for a new turn ID starts fresh from `Clean`, `Violated` and `Unverifiable` (`ask`: `Arming`; other modes: `Idle`). The same ID in a terminal state changes nothing. The shell holds `RunFinished` until the After snapshot is checked (core/tasks).
- **M3 trust.** New explicit starting state `Unknown{Repo}`, viewed as untrusted. Every state carries `Repo`; an input for another repo changes nothing. New input `ScanFailed{Repo, Why}` gives `Untrusted{WhyScanIncomplete}` from any state (audit `scan_incomplete`, no free text, persisted trust left for the next complete scan). `NoConfig` is only reached by a complete scan.
- **M5 admit.** `Request.Trust` is `[]TrustView`, one per repo the mode touches; a non-Ask mode needs every view `None` or `Trusted`, and an empty list is refused.
- **Policy and paths.** `Policy.Validate` refuses a drive root, `/`, and a UNC root without host, share and one more part. Protected names add `.codex/**`, `.gemini/**`, `.cursor/**`, `.husky/**`, `.githooks/**`, `CLAUDE.local.md`, `.gitmodules`; they are exported as `ProtectedDirNames` and `ProtectedFileNames` for the cross-check test in `core/proposals`. A glob part with a `[..]` class that can match `..` (for example `.[.]`) is refused. Read and Search of `.git/config` and anything under `.git/hooks` give `Deny ProtectedPath`.

## Review gate 1, round 2

- **Trust.** While `Untrusted` has `Why=ScanIncomplete`, `UserTrusts` is refused (`trust_stale` audit, no state change): only a complete `Scanned` re-arms trust. `ScanFailed` records `Why.Prior` (and `Was`); the next complete scan recomputes `Why` from it (`NeverTrusted`, `ConfigChanged{Was}`; `Revoked` stays `Revoked`).
- **Command words.** `,` (PowerShell array operator) and `@` (splatting) are no longer allowed word characters. `=` stays.
- **Integrity supersede (S2).** In `Arming`, `Armed` and `Checking`, `TurnStarts` for a different turn resolves the old turn as `Unverifiable` (commands: `Audit`, `FlagTask`, no `StopTask`: it is already replaced) and starts the new turn (Ask: `Arming` + `TakeSnapshot{Before}`; other modes: `Idle`). Property: every Ask turn that ran gets a `Clean`, `Violated` or `Unverifiable` verdict. A `TurnStarts` for the same turn changes nothing.
- **Integrity ends.** `TurnEnds` in `Arming` (the run ended before the Before snapshot returned) gives `Unverifiable` with `StopTask{CannotVerify}` and `Audit`. `TurnAborted` in `Armed`/`Checking` gives `Unverifiable` with `Audit` and `FlagTask` (no `StopTask`), so the user sees "could not verify".
- **Trust scan coverage.** Adapters may implement `agents.ConfigNameProvider{ConfigNames() []string}`: file names that configure the agent at any depth (Claude Code: `CLAUDE.md`, `CLAUDE.local.md`, `AGENTS.md`). `ConfigPaths` for Claude Code also lists `.claude/rules` and `.claude/output-styles`.

### Shell carry-ins

What the core cannot do and the shell must:

1. Map `Unverifiable` and `Violated` to a user-visible `needs_you` task status.
2. Time out a lost snapshot reply and feed `TurnAborted` (the core has no timers).
3. Hold `RunFinished` until the After snapshot is checked.
4. Call `platform.HasShortNameSegment` on proposal paths and refuse `~N` segments from volumes where short names are not expanded.
5. `Request.Trust` must contain one view per repo the mode touches; the core cannot check coverage.
6. Scan the repo for `ConfigNames` at any depth, besides `ConfigPaths`.

## Gate 1 round 3 notes (2026-10-26)

- Command words no longer allow `,` and `@`, and tail words no longer allow `=` (a `.cmd`/`.bat` wrapper splits an unquoted argument at `=`).
- A failed scan after `Trusted` leaves `Untrusted{ConfigChanged}` even if the next complete scan has the same hash: the user re-trusts (fails safe). Core state and the stored trust disagree until the next `Scanned`; the shell should `ClearTrust` on `ScanFailed`.
- `ConfigNameProvider.ConfigNames()` has no caller yet: the S2 trust scanner must walk the repo for those names at any depth.

## Review gate 2 notes (2026-10-26, Claude Code adapter)

Added to "Shell carry-ins" (the tasks service does these):

7. **A `tool_use` without a hook-log record fails the task.** The adapter's run exposes `agents.ToolUseReporter.SeenToolUses()` (IDs of every tool call in the stream) and `agents.DenyReporter.Denies()` (refusals of the hook, tool name and guard reason message only, no paths). After `Wait`, the tasks service compares the stream's tool-use IDs with the enforcement log; a tool call the hook never saw means enforcement was bypassed, so the task fails and is flagged. Each deny is audited as `guard.deny`. The hook log itself is deleted with the run folder.
8. The adapter refuses to start when the policy is wider than the folders it was given (non-project modes: write roots inside `Dir`; project mode: inside `Dir` or `ExtraDirs`; read roots inside `Dir` plus `ExtraDirs`), and when the MCP config file (bearer token) lies inside a policy root. The adapter copies the MCP file into its run folder and removes it afterwards.
9. Repo config is inert for Claude Code (`--setting-sources ""`, `--strict-mcp-config`, `disableAllHooks:false`; `docs/spikes.md` B5), so the adapter no longer refuses untrusted repos; `Admit` keeps the SF-09 policy. `StartOptions.ConfigTrusted` is advisory.
10. The hook enforces sub-agent depth only for `MaxDepth` 1 or 2 (its input has no parent link); deeper caps rely on the CLI's `CLAUDE_CODE_MAX_SUBAGENT_SPAWN_DEPTH`.

## Gate 2 fixes

- **Trust scan and links (SF-09).** Any link, junction or irregular entry met while walking is `ErrScan` when its path is a config path, a folder on the way to one (`.claude` for `.claude/settings.json`), something below one, or named like a config name. Junctions are found with `platform.IsReparsePoint` (they are not `ModeSymlink`). After the walk every segment of every config path is checked again. `safety.LinkBlocksConfig` is the shared predicate; the fake scanner uses it, and `LinkScannerContract` runs against the fake and the real scanner (real junction via `mklink /J` on Windows, no skip). Unreadable config files, the file-count cap, `.git` exclusion and case folding have contract cases.
- **Scan limits.** The walk does not enter `node_modules`, `target`, `build`, `dist`, `.venv` or `vendor` unless a config path runs through the folder. Config files that only match by name inside those folders are not seen (third-party code, not the repo's own agent setup). The 5000-file and 500000-entry caps stay; `MaxConfigFiles` is a variable only so the contract can lower it.
- **Allowlist (SF-05).** Rule validation lives in the core (`guard.CommandRule.Validate`, `core/guard/rule.go`), so `ParsePolicy` and `Policy.Validate` refuse the same rules as the settings parser; `safety.ValidateRule` is a thin wrapper. By base name (case-insensitive, extension dropped) it refuses shells and script hosts (incl. `env`, `xargs`, `find`, `start`, `explorer`, `wsl`), `git`, network clients (incl. `gh`, `glab`, `docker`) and destructive or system tools (incl. `tar`, `7z`, `copy`, `move`, `certutil`); interpreters and runners (`node`, `npx`, `python`, ...) only with fixed arguments and `Tail=none`; package managers and build tools (`npm`, `go`, `mvn`, ...) only with a fixed first argument that is not `install`, `add`, `i`, `publish`, `exec`, `dlx`, `x`, `remove`, `uninstall`, `update` or `upgrade`; `mvnw` and `gradlew` are refused as "not supported yet" because D11 refuses a program inside a write root.
- **Snapshot (SF-07).** Untracked hashing is capped at 50000 files and 256 MiB; over the cap `Snapshot` returns `git.ErrSnapshotTooLarge` (plain `UserMsg`), which the shell must treat as `Unverifiable`. The file reader stops on context cancel. The read-only calls pass `-c core.fsmonitor=false`. The hash of `git for-each-ref` is folded into `Status`, so a new or moved branch or tag changes the fingerprint.
- **Snapshot blind spots (accepted).** Exec-bit flips on untracked files are not seen (the content hash does not include the mode); ignored files (for example `.env`) are excluded by design; a nested untracked repo is hashed by its folder name only.
- **Worktrees.** `Error.UserMessage` never holds a path (detail stays in `Err`); audit detail is built with `json.Marshal`; an active row whose path is not under `DataDir/wt` is refused instead of reused; `StoreContract` runs against the fake and `DBStore`. Carry-in: `Ensure` takes no lock; the tasks service must serialize calls per task.

## Gate 2 round 2 fixes (adapter)

- **Hook coverage (N2).** Before the run folder goes, the adapter reads the hook log once and keeps every tool_use ID the hook judged (allow or deny, sub-agent calls included, bounded 100000) as `HookedToolUses`, and the stream IDs without a record as `UnhookedToolUses` (`agents.HookCoverageReporter`). The tasks service fails the task when `UnhookedToolUses` is not empty.
- **Programs outside policy roots (N3).** `Start` refuses when the hook executable or the resolved `claude` binary lies inside any read or write root (`platform.ResolveRoot`, `platform.FoldCase`). The message names no path.
- **Tamper detection (S1).** SHA-256 of `policy.json`, `settings.json`, `hook.json` and `mcp.json` at `Start`; compared once the process has ended and before the folder is removed. A change or a missing file sets `Tampered()` (`agents.TamperReporter`), emits one `EventError` with a fixed message and ends the run as failed. Carry-in: the tasks service must also fail the task when `Tampered()` is true.
- **Run folder (S2).** The run folder (which holds `hook.json`, so `YGG_HOOK_CONFIG`) must be outside every policy root, or `Start` refuses. Removal retries once after 200 ms; a second failure logs a warning without paths and the run still closes.
- **Caps (S6).** `Denies()` keeps the first 100; `DenyCount()` is the true count (`agents.DenyCountReporter`). The hook log is never read past 1 MiB and the hook refuses to append to a log at that size, so enforcing calls are denied (fail closed); IDs beyond the readable part count as unhooked.
- **cmd shim (S4).** A Windows test runs a `.cmd` through `platform.Start` with the adapter's real flags and checks `"--setting-sources="` arrives as one piece. A `platform.ErrBatchArg` at start gives a message that names the kind of folder to move (project, user profile or temp), never the path.
- **Admission (S5).** `agents.StartOptions.Admitted` is set by the tasks service after `guard.Admit` allowed the run; `claudecode.Start` refuses without it, the mock ignores it. Carry-in: the tasks service must set it only from `guard.Admit`'s result, never as a constant (`internal/api/tasks.go` sets it for the existing Ask-only path, which has an explicit Ask check instead of `Admit`).
