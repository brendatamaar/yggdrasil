# Phase 6 notifications — Model Functional Effect design
Profile: service

UI-06, D11; `internal/core/notify` / `internal/notify`, existing task and verification
publishers. In-app server history is TanStack Query data; browser/read UI state is
local. Replace duplicate task-status toast generation in LiveSync during UI wiring.

## Shapes and state map

`Notice{ID,TaskID,TaskTitle,Status,SourceRevision,CreatedAt}`. Displayable status is
a validated closed enum: NeedsYou, VerificationPassed, VerificationFailed,
ReadyForReview, Done, Failed, Cancelled. Notifications never take arbitrary body
text or generic raw event maps. ID/time are metadata, title/status the entire text.

Producer state has three variants: `Empty`, `Current{Revisions,Recent}`,
`Closed`. Revisions are per `(task,source)`; verification source includes run ID
and version, so an old verification cannot overwrite a new run. Keep 200 notices;
maintain a separate durable producer high-water mark, not dedup by retained notice
history. Trimming history never re-enables a replayed old notice.

## Inputs and transition table

`TaskTransition{Task,Title,Status,Revision,Now,ID}`,
`VerificationTransition{Task,Title,RunID,Status,Revision,Now,ID}`,
`Recovered{Recent,HighWater}`, `Stop`.

| From | Input / guard | To | Commands |
|---|---|---|---|
| Empty | Recovered | Current | none |
| Empty/Current | notifiable newer transition | Current with new high-water | PersistNoticeAndAudit, PublishNotice |
| Current | uninteresting newer status | Current with high-water updated | PersistHighWater |
| Current | stale/duplicate transition | unchanged | none |
| Empty/Current | Stop | Closed | none |

Other pairs unchanged/no commands. Needs-you is one transition, not every board
poll or pending-proposal counter update. Verification queued/running is not a
result; each new terminal result revision produces at most one notice.

## Commands and call graph

| Command | Cardinality | Idempotent | Produces |
|---|---|---|---|
| PersistNoticeAndAudit / HighWater | one transaction | yes, unique source revision | committed notice |
| PublishNotice | one invalidation with cursor | yes, notice ID | subscriber refresh |

```go
// Production
task/verification commit → scoped publisher adapter → validated producer input
  → notify.Step (core) → Store.Commit(high-water + notice + audit)
  → existing WS hub cursor → Query invalidation → notification history
// Tests
Step sequences → properties
producer → SQLite + recording hub, fake clock/IDs
browser reducer → controlled permission/visibility/Notification capability
```

Publish follows successful commit. A crash between commit and publication is
recovered by notification-history query on reconnect, with a bounded cursor page.
No durable exactly-once OS toast guarantee is claimed across distinct tabs/devices;
per-tab/session delivery IDs are retained for dedup, and historical gap recovery
fills the in-app list without replaying old browser toasts. A live newer event
may toast only once while backgrounded. Browser permission/toast are UI effects,
not durable backend commands. No external notification service is introduced.

## Failures and requirements

| Node | Failure | Class | Handling |
|---|---|---|---|
| Parser | invalid revision/status or unknown task | domain outcome | reject producer input |
| Store | transient write conflict | retry | reload/re-step, never publish before commit |
| Hub | disconnected/gap | escape hatch | query history/high-water on reconnect |
| Browser | denied/unsupported/DND | escape hatch | in-app remains available; no repeated permission prompts |
| Reducer | impossible variant | die | isolate operation, no raw event fallback |

`NoticeStore` SQLite/fake contract: unique source key, atomic high-water/notice,
retention without replays, cursor/gap query. `Publisher` existing hub/recording fake:
commit-before-event and privacy. `Notification` browser/fake: permission gesture,
background-only, denied/unsupported, duplicate live revision and gap suppression.

## Boundary, invariants and files

Task/verification adapter parser validates enums and monotone versions from durable
sources (add revision to existing task event contract in F1). DB parser validates
source/run/task IDs and statuses. API parser accepts cursor/limit (max 200), no
arbitrary body field. Browser parser uses generated DTO and rejects malformed input.

`TestPropNoticeNewerOnly`, `TestPropNoticePayloadMinimal`,
`TestPropNoticeRetentionKeepsHighWater`, `TestNoticeCommitBeforePublish`,
`TestBrowserBackgroundOnly`, `TestBrowserGapDoesNotReplayToast`.
Files: `core/notify/*.go`, `notify/*.go`, F2 notice queries, F3 DTO/topic/revision
contracts; wiring in cmd/api, browser reducer/hooks and NotificationsMenu in U.

## Open questions

Per-tab toast deduplication with no history replay is the guarantee; cross-tab
single-toast leadership is outside UI-06. Minimal content remains mandatory.

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
