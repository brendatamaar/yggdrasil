# Phase 6 UI workflows — Model Functional Effect design
Profile: service

AC-06/UI-07 and the interactive surfaces for GH-05/06, AC-01–05, WS-09,
UI-06, AU-02/UI-03, NR-01–04. One frontend owner has all `web/**` in Batch 5.
Go DTOs and generated types come from foundations; never hand-edit generated files.

## Shapes and state map

Use separate small pure reducers, not one product-wide machine. Server data is
TanStack Query-owned, local dialog/permission/tab delivery state belongs to hooks
or existing UI stores. No copy of server history in Zustand.

Action dialog has six variants: `Closed`, `Loading{RequestID}`, `Preview{DTO}`,
`Submitting{RequestID,Digest}`, `Succeeded{Result}`, `Failed{Message,RetryContext}`.
Confirmation dialog has six variants: `Closed`, `Loading`, `Review{ID,Hash,Lines}`,
`Confirming{ID,Hash}`, `Decided{Result}`, `Failed{Message}`. Reuse the existing
proposal UI where possible, extending Describe/DTO rendering rather than adding a
second mutation execution button. Every main/remote mutation uses this confirmation.

Browser delivery has four permission variants: `Unsupported`, `Default`, `Denied`,
`Granted`; visibility and delivered live IDs are inputs/data of the reducer, never
server-history copies. New-repo UI renders the backend lifecycle variants; it does
not derive its own stage by testing a collection of booleans.

Empty views are explicit success states: NoProjects, EmptyProject, NoRemote,
NoCredentials, NoIssues, NoMRs, NoBuilds, NoTasks, NoStandup. Pending/Error/Partial
are distinct from these; errors never render a misleading empty-success message.

## Inputs and transitions

Action inputs: Open, Loaded(attempt), LoadFailed(attempt), Submit,
Submitted(attempt), SubmitFailed(attempt), Close. Confirmation inputs: OpenProposal,
LoadedProposal, ConfirmClicked(hash), Confirmed, Rejected, Failed, Close.
Browser inputs: CapabilityChecked, PermissionGesture, PermissionResolved,
VisibilityChanged, LiveNotice, GapHistoryLoaded, Closed.

| Reducer/from | Input / guard | To | Commands |
|---|---|---|---|
| action Closed/Failed | Open | Loading | QueryPreview |
| action Loading | Loaded matching request | Preview | none |
| action Preview | Submit | Submitting | SubmitAction(same RequestID) |
| action Submitting | success/failure current request | Succeeded/Failed | InvalidateQueries / show message |
| action any pre-submit | Close | Closed | CancelQuery |
| confirm Review | ConfirmClicked exact shown hash | Confirming | ConfirmProposal |
| confirm Confirming | result | Decided/Failed | InvalidateQueries |
| browser Default | PermissionGesture | unchanged | RequestPermission |
| browser any supported | PermissionResolved | corresponding variant | none |
| browser Granted | LiveNotice, background, unseen live ID | Granted with ID recorded | ShowBrowserNotice(title,status) |
| browser any | GapHistoryLoaded | unchanged | UpdateQuery only, no toast |
| browser any | VisibilityChanged | same permission | none |

Load failures become Failed with plain retry guidance; current mutation requests
are not cancelled by closing a dialog—retain RequestID so reopening can reconcile.
Other unhandled pairs/stale responses: unchanged, no commands. Permission request
is never an automatic mount effect. A toast API exception disables that delivery
attempt but never loses the in-app notice. IDs are deduped per active browser
session without storing code/logs/prompts in local storage.

## Commands and call graph

| Command | Cardinality | Idempotent | Produces |
|---|---|---|---|
| QueryPreview/CancelQuery/InvalidateQueries | one bounded Query operation | yes | Loaded/failure |
| SubmitAction | one explicit mutation | RequestID+digest | result/reconcile status |
| ConfirmProposal | one explicit UI mutation | immutable proposal/hash | proposal result |
| RequestPermission | one browser prompt from gesture | no; browser-controlled | PermissionResolved |
| ShowBrowserNotice | one live notice | session ID dedup | browser effect/failure |
| CopySummary | one user gesture | yes | copied/error feedback |

```ts
// Production
widget / repo row / palette
  → shared features/actions component
  → reducer(state,input) (core)
  → typed client + TanStack Query (boundary)
  → same server catalogue/preview/action endpoint
WS revision → Query invalidation → parsed notification DTO
  → notification reducer → Web Notifications API when backgrounded
// Tests
pure reducers → generated event sequences
components → fake API + fake WS + browser Notification/clipboard fakes
Playwright → built mock-data app + mock adapter/fake hosts, dark and light themes
```

Shared feature modules may be imported by widgets; widgets never import each
other. Register project/repo action palette commands through the existing command
registry with stable unique keys and cleanup on unmount. Palette commands select
a scoped source and show preview, not bypass confirmation. MR and issue contexts
must have visible scope, source identity and error/fallback messaging.

## Failures and requirements

| Node | Failure | Class | Handling |
|---|---|---|---|
| API decode | malformed/unknown DTO | domain outcome | error state, no optimistic execution |
| Query | disconnected/timeout | retry | Query reads only; mutations reconcile same key |
| API mutation | stale head/hash/scope/auth | domain outcome | preserve preview, refresh/re-propose |
| Browser | notification denied/unsupported, clipboard denied | escape hatch | in-app / selectable text fallback |
| Reducer | impossible variant | die | component error boundary, no mutation |

Capabilities: typed HTTP client/fake API contract (body limits, error shapes,
duplicate key); WS/fake socket (gaps/replay); Notification/browser fake (gesture,
background/dedup); clipboard/browser fake; Radix keyboard primitives and router.
Do not add an unneeded state/data library. Browser runner is real Playwright,
replacing the current `task test:e2e` placeholder in foundations.

## Boundary and invariants

API/WS parsers validate generated DTO variants/status/source/revision and reject
unknown payloads; permission parser maps browser supported/default/denied/granted;
form parser validates leaf/visibility/range/selection before requests, with server
validation still authoritative. Host content is plain text, never raw HTML.

`TestPropActionUIIgnoresStaleResults`, `TestPropConfirmUsesShownHash`,
`TestPropBrowserOnlyLiveBackgroundOnce`, `TestPropNoPermissionWithoutGesture`,
`TestPhase6NeedsAttentionActions`, `TestPhase6EmptyStates`,
`TestPhase6NoWidgetCrossImports`, keyboard and theme screenshots.
The integrated suite covers all seven current attention reason families, mixed
reasons, top-eight truncation and row filters, plus unsupported/tokenless hosts.
A usable diagnosis preview may remain pending a restored cwd; never label that
as a successfully started agent. A link to detail alone is not an agent action.

## Files and open questions

`web/src/features/actions`, newrepos, notifications, standup; existing repo/MR/board,
composer, proposals, LiveSync and NotificationsMenu; widget registry/Standup and
relevant row widgets; command registration. F3 owns API generation, task runner,
package/lock/config changes before U starts; U reports any contract gap instead of
editing it. No unresolved safety exception; browser behavior and real hosted
permissions still require the Windows checklist at phase close.

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
