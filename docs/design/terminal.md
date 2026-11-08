# Terminal — Model Functional Effect design
Profile: service

Covers: DV-04 (web terminal dock, single-use socket token), DV-05 (tracked folders only, clean env), decision 4 in `docs/phases/phase-5.md` (human use only, not reachable from MCP or agents, open/close audited, token 30 s TTL, in memory).

Core: `internal/core/terminal`. Shell: `internal/terminal` (service, token source, PTY pump). HTTP and WebSocket handlers stay thin in `internal/api`.

One pure piece: `Step(Registry, Input) (Registry, []Command)`. The `Registry` holds all tokens and sessions because the limits (pending tokens, open sessions) are global. Entries are independent variants; there is no per-entity file.

## State map

**Token** (keyed by `TokenHash`, the SHA-256 of the random token; the raw token never enters the core):

| State | Data |
|---|---|
| `Issued` | `Folder` (tracked folder reference), `ExpiresAt` |
| `Consumed` | `At`, `ExpiresAt` (kept until expiry so a replay is told apart from an unknown token) |

An expired token is simply removed by `Wake{Expire}`; there is no `Expired` entry state (an expired but not yet removed `Issued` entry is refused and consumed by the redeem check).

**Session** (keyed by `SessionID`):

| State | Data |
|---|---|
| `Opening` | `Folder`, `Since` |
| `Open` | `Folder`, `Pid`, `Size`, `Since` |
| `Closing` | `Folder`, `Cause` (ClientClosed / ShellExited{Code} / UserClosed / AppShutdown / OpenFailed), `Since` |
| `Closed` | `Cause`, `At` (removed from the registry right after its audit) |

**Registry**: `Tokens map[TokenHash]Token`, `Sessions map[SessionID]Session`, `Limits{MaxPending=8, MaxSessions=4}`, `Phase` (Serving / Draining).

## Inputs

| Input | Fields |
|---|---|
| `IssueRequested` | `Hash`, `Folder` (already resolved from a repo/worktree ID by the shell), `Now`, `Reply` ID |
| `RedeemRequested` | `Hash`, `Session` (new ID), `Size`, `Check` (FolderStillTracked / FolderUntracked, from the shell at redeem time), `Now`, `Reply` ID |
| `Opened` | `Session`, `Pid`, `Now` |
| `OpenFailed` | `Session`, `Now` |
| `Resized` | `Session`, `Size` |
| `ClientClosed` | `Session`, `Now` |
| `ShellExited` | `Session`, `Code`, `Now` |
| `CloseRequested` | `Session`, `Now` (UI close button) |
| `Shutdown` | `Now` |
| `Wake` | `Kind` (Expire), `Now` |

## Transition table

| From | Input | Guard | To | Commands |
|---|---|---|---|---|
| no token | `IssueRequested` | pending `< MaxPending` and `Phase = Serving` | `Issued{Folder, Now+30 s}` | `ScheduleWake{Expire, Now+30 s}`, `Reply{Issue, Granted}` |
| no token | `IssueRequested` | otherwise | unchanged | `Reply{Issue, Refused{TooManyPending / ShuttingDown}}` |
| no token | `IssueRequested` | hash already present | unchanged | `Reply{Issue, Refused{Collision}}` |
| `Issued` | `RedeemRequested` | `Now < ExpiresAt`, `Check = Tracked`, sessions `< MaxSessions`, `Serving`, folder equals token folder | token `Consumed`, new `Opening` | `OpenPTY{Session, Folder, Size}`, `Reply{Redeem, Granted}` |
| `Issued` | `RedeemRequested` | `Now >= ExpiresAt` | token removed | `Reply{Redeem, Refused{Expired}}` |
| `Issued` | `RedeemRequested` | `Check = Untracked` | token `Consumed` | `Reply{Redeem, Refused{NotTracked}}` |
| `Issued` | `RedeemRequested` | session limit reached or `Draining` | token `Consumed` | `Reply{Redeem, Refused{TooManySessions / ShuttingDown}}` |
| `Consumed` | `RedeemRequested` | | unchanged | `Reply{Redeem, Refused{Reused}}` |
| none | `RedeemRequested` | | unchanged | `Reply{Redeem, Refused{Unknown}}` |
| `Opening{s}` | `Opened{s}` | | `Open{s}` | `Audit{terminal.open: session, folder}` |
| `Opening{s}` | `OpenFailed{s}` | | `Closed{OpenFailed}` | `Audit{terminal.open_failed}` |
| `Open{s}` | `Resized` | size within limits | `Open` with size | `ResizePTY{s, size}` |
| `Open{s}` | `ClientClosed` / `CloseRequested` / `ShellExited` | | `Closing{s, cause}` (ShellExited goes straight to `Closed`) | `ClosePTY{s}` (not for ShellExited), `Audit{terminal.close: cause, exit code}` |
| `Opening{s}` | `ClientClosed` / `CloseRequested` | | `Closing{s, cause}` | `ClosePTY{s}`, `Audit{terminal.close}` |
| `Closing{s}` | `ShellExited{s}` | | `Closed` (removed) | none (audit already written once) |
| any session | `Shutdown` | | `Closing{AppShutdown}` for every Opening/Open; `Phase = Draining` | `ClosePTY` each, `Audit` each, `Reply{...}` none |
| any | `Wake{Expire}` | | tokens with `ExpiresAt <= Now` removed | none; if tokens remain `ScheduleWake{Expire, next}` |
| any | input for an unknown or closed `Session` | | unchanged | none |
| any other (state, input) pair | | | unchanged | none |

A session gets its `Audit{terminal.close}` exactly once, on the first transition into `Closing` or `Closed`.

## Invariants

| # | Invariant | Property test |
|---|---|---|
| T1 | Single use: for one `Hash`, at most one `Opening` session is ever created, however many redeems arrive | `TestPropTokenSingleUse` |
| T2 | Expiry: `Reply{Redeem, Granted}` implies `Now < ExpiresAt` of that token | `TestPropNoGrantAfterExpiry` |
| T3 | A redeem of an `Issued` token never leaves it `Issued`: it becomes `Consumed` or is removed, whatever the outcome (refused redeems burn the token) | `TestPropRedeemBurnsToken` |
| T4 | `len(Sessions in Opening/Open) <= MaxSessions` and pending tokens `<= MaxPending` after every step | `TestPropLimits` |
| T5 | Every `OpenPTY` carries the folder stored in the token, and `Check = Tracked` held for that input | `TestPropOpenOnlyTokenFolder` |
| T6 | Each session that reached `Open` has exactly one `terminal.open` and exactly one `terminal.close` audit; one that failed to open has one `terminal.open_failed` | `TestPropSessionAuditedOnce` |
| T7 | `Closing` is entered together with `ClosePTY` (except when the shell already exited) and leaves only to `Closed` | `TestPropClosingEnds` |
| T8 | After `Shutdown`: no session in `Opening/Open`, no `OpenPTY`, no new grant | `TestPropShutdownDrains` |
| T9 | Unknown or closed `Session` inputs change nothing | `TestPropUnknownSessionIgnored` |
| T10 | No raw token appears in any state, command, reply or audit entry (the core only has `TokenHash`); the audit carries session ID and folder ID, never keystrokes, command lines or paths | `TestNoSecretInOutputs` (reflection over generated values), `TestAuditHasNoInput` |
| T11 | `Resize` outside 1..500 cols/rows never reaches `ResizePTY` | `TestPropResizeBounded` |
| T12 | Expire removes exactly the tokens with `ExpiresAt <= Now`, never a live `Opening` session | `TestPropExpireOnlyTokens` |

## Commands

| Command | Cardinality | Idempotent | Produces |
|---|---|---|---|
| `OpenPTY{Session, Folder, Size}` | one | **no**, protected by T1 | `Opened` / `OpenFailed`, then `ShellExited` |
| `ResizePTY{Session, Size}` | one | yes | none |
| `ClosePTY{Session}` | one | yes (platform `Close` is idempotent) | `ShellExited` |
| `ScheduleWake{Expire, At}` | one | yes | `Wake` |
| `Reply{Issue / Redeem, result}` | one | yes (per `Reply` ID) | the HTTP or WebSocket answer |
| `Audit{Entry}` | one | yes with key (subject, action, at) | none |

## Call graph

```go
// Issue: POST /api/terminal   (Host/Origin checked by the existing middleware, invariant 7)
api handler
  → ParseIssue(body)                          // {repoId | worktreeId}, never a path
  → Service.Issue(ctx, target)
      → resolve to a tracked directory (DV-05, invariant 6); refuse otherwise
      → token := TokenSource.New()            // 32 random bytes, base64url
      → Step(IssueRequested{Hash: sha256(token)})
      → Reply Granted → respond {token, expiresIn: 30}   // the only place the raw token exists

// Redeem: GET /ws/terminal?token=...   (Host/Origin checked before the upgrade)
api handler
  → ParseToken(query) → Hash
  → re-resolve the folder (Check)
  → Step(RedeemRequested)  → Reply Granted → upgrade WebSocket → OpenPTY
interpret OpenPTY
  → env := safety.EnvBuilder.Build            // clean env, no tokens
  → platform.StartPTY(ctx, spec{Dir, Shell: platform.DefaultShell(), Size})
  → Step(Opened{pid}) ; pump goroutines (owned by the session context):
        PTY → WebSocket binary frames ; WebSocket frames → PTY
        client text frame {type:"resize"} → ParseFrame → Step(Resized)
  → on read error / close: Step(ClientClosed | ShellExited)
interpret ClosePTY → PTY.Close() in the spikes B1 order, keep reading until EOF so Close cannot block
```

Tests: core by `Run`; the shell with `platformtest.FakePTY`, a counter `TokenSource`, a fake clock, and a fake socket. A real ConPTY test is Windows-tagged.

## Failures

| Node | Failure | Class | Handling |
|---|---|---|---|
| `TokenSource` | random source error | die | request fails (500, plain message); no token is stored |
| Folder resolution | untracked, removed, `\\wsl$` | domain outcome | refused at issue; at redeem `Check = Untracked` |
| `platform.StartPTY` | shell missing, ConPTY unavailable | domain outcome | `OpenFailed`; socket closed with a plain reason |
| PTY read/write | broken pipe | domain outcome | `ShellExited`/`ClientClosed` |
| WebSocket | upgrade rejected (Host/Origin) | domain outcome | 403 before the token is looked at (the token is not burned by a bad origin) |
| `ClosePTY` | error | escape hatch | logged; the job object ends the tree at shutdown |
| pump goroutine | panic | die | recovered, `ShellExited` fed |
| hub / socket | slow client | escape hatch | bounded write queue; the session closes if it overflows |
| Ctrl+C in the child | `SetConsoleCtrlHandler(NULL, FALSE)` fails in ConPTY | escape hatch | **stop and ask the human** (decision 4); never apply it in `ygg` itself |

## Requirements

| Capability | Live | Test | Contract |
|---|---|---|---|
| `PTYStarter` (`platform.StartPTY`) | `platform` | `platformtest.FakePTY` | existing PTY contract: write echoes back, read ends with EOF after exit, resize accepted, `Close` idempotent and kills the tree, environment required (nil refused) |
| `TokenSource` | `crypto/rand` | counter | 32 bytes, unique across 10 000 draws (live); fake returns known values |
| `Folders` (resolve ID to a tracked dir, check still tracked) | repos / worktrees | fake | removed repo → untracked; worktree on a different repo refused |
| `Socket` (read frames, write frames, close) | `coder/websocket` adapter | in-memory pair | ordering, close once, write after close errors |
| `Audit` | store | in-memory | entry has no free text from the client |
| `Clock` | real | fixed | none |

## Boundary

| Source | Parser | Produces | Rejects |
|---|---|---|---|
| `POST /api/terminal` body | `terminal.ParseIssue` | `Target` | both or neither of `repoId`/`worktreeId`; non-ID strings; any `path`-like field (unknown fields rejected) |
| `?token=` | `terminal.ParseToken` | `TokenHash` | not exactly 43 base64url characters; extra query parameters carrying paths |
| client frames | `terminal.ParseFrame` | `Input{Data}` or `Resize{Size}` | text frames other than `resize`; sizes outside 1..500; frames over 64 KiB; unknown JSON fields |
| settings | `terminal.ParseShell` | shell choice | not in the platform's list (PowerShell, `$SHELL`); the setting is a name, never a command line |

Each gets: round trip where it formats (`ParseToken(Format(h)) == h`), accepted ⇒ valid, one test per rejection rule, and a fuzz target for `ParseFrame` and `ParseToken`.

## Persistence

No new tables for tokens or sessions: tokens live in memory only (a restart of `ygg` invalidates them, by design), sessions end with the process. Open and close go to the existing audit log (`terminal.open`, `terminal.close`, `terminal.open_failed`) with session ID, repo or worktree ID, cause and exit code. Keystrokes and command lines are never stored.

## Open questions

1. **Limits.** `MaxSessions = 4` and `MaxPending = 8`. Recommendation: keep; both are constants in one place.
2. **Token binding.** The plan said "bound to the folder and the session"; there is no login in local mode, so the token is bound to the folder and to the Host/Origin check. Recommendation: yes. Remote mode (RA-*) would add the login session later.
3. **Origin failure does not burn the token** (the handler rejects before the registry sees it). Recommendation: yes, to avoid a cross-site page burning a legitimate user's token; the token is unguessable and expires in 30 s anyway.
4. **Default shell flags.** PowerShell is started with `-NoLogo` and the profile enabled. Recommendation: keep the user's profile.
