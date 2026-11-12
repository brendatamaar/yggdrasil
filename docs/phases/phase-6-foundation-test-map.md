# Phase 6 foundation verification map

The plan and seven designs are approved. Foundations merged at `09bc226`; their
feature state machines remain inert and proposal variants remain disabled. This
map covers the actual persisted contracts and review repairs before feature fan-out.
Test-engineer owns the new regression tests; the lead owns production repairs.

| Invariant / boundary | Tests to implement | Required failure demonstration |
|---|---|---|
| Outbox claim result is fenced by returned claim version | Expire claim A, reclaim B using same worker ID; A cannot complete or release B; B can complete, and duplicate completion refuses. Exercise actual SQLite through Tx methods. | Omitting version predicate must fail both stale-claim assertions. |
| Recovery receipt is valid proof for exactly one variant | Each variant round-trips; invalid/absent IDs, object hashes, file name and hash, remote URL scheme/credentials/query/fragment, unknown/foreign fields including zero-valued foreign fields refuse on decode; invalid values refuse on encode. Add accepted-implies-valid and fuzz. | Empty remote evidence and dir+head must fail when validation is bypassed. |
| Stage recovery cannot skip/reorder stages | Check each legal init/publish receipt prefix; missing, extra, reordered and wrong-file receipt variants refuse encode/decode. Stopped/uncertain states have matching init/publish resume stage, positive attempt and immutable spec. | A finalize-tracking state without completed receipts must expose removed prefix validation. |
| Reservation scope is immutable | Tampered row Project/Repo/Leaf vs spec rejected; SaveReservation rejects changed init scope/hash and cross-repo publishing/resume specs; valid versioned save retains original binding. | Removing scope comparison must make a cross-repo case fail. |
| Only parsed valid action input enters store | BeginActionRequest rejects invalid ID/kind/source/agent/model even with a matching Digest. | Matching digest alone must not accept an invalid request. |
| Browser integration tests cannot reuse real data | Direct config without sandbox variable refuses; reuseExistingServer always false; Node launcher creates new temporary dir, clears inherited YGG configuration, uses real test runner. | Configuration regression or collision cannot silently attach to another running instance. |

## Baseline check repairs (test-engineer assignment)

The first complete low-concurrency Go run reproduced inherited failures in:
`TestLoadConfig` (Windows-only synthetic absolute path on Linux),
`TestRunThroughACmdShimInAFolderWithSpaces` (Windows executable test in generic
file), `TestCodeModeEndToEnd_ProposalCommitConfirmedThroughTheAPI` and
`TestPhase4ServicesAreWiredAndMergeLocalWaitsForConfirm` (temporary repositories
rely on a global author), and the unreadable-config ancestor test's cleanup mode.
Use platform-correct fixture paths, place the .cmd test under a Windows build tag,
configure fictional repo-local authors in test fixtures, and restore original
permission mode in cleanup. Never add runtime.GOOS outside platform, set HOME,
change real/global Git identity, weaken an invariant, blanket-skip Linux tests,
raise timeouts to hide correctness bugs, or change production code from this task.

Investigate any deterministic-order or audit test failure and report a failing test
when it exposes a production defect. The lead must fix production; do not mask it.

## Known follow-up before action dispatch

The foundation's loaded ActionRecord.Request currently contains an identity
projection, omitting Agent/Model. This cannot be used as a replayable request.
Before enabling action dispatch, persist/restore the complete immutable request or
use an explicit identity type and require a full outbox payload to validate and
match the stored digest. This is an open Should-fix from foundation review, not a
verified dispatch implementation. The lead must resolve the shared contract before
Batch A starts; agents may not invent a competing representation.

Run all five required checks after repairs; use low concurrency for this constrained
machine and report disk/build failures separately. Show each new test can fail with
a temporary controlled mutation/negative control, restore all mutations, commit only
tests/helpers/fixtures. No real repos, hosts, model calls or agent CLIs.

## Additional functional recheck regressions

The next recheck found three additional Must-fix boundary defects. Extend the map:

- Uncertain with nil Stage/ResumeNone must fail both validators and state storage;
  every accepted uncertain state must be decodable (round-trip invariant).
- EffectRecord is another receipt boundary: validate on insert, settle and load.
  Reject succeeded remote proof `{}`, metadata/body kind disagreement, and
  stage/receipt disagreement. Comment posting needs its own typed receipt, not
  a pretend new-repo remote proof.
- Receipt prefixes must be internally consistent and bound to the immutable spec:
  file hashes equal the confirmed README/ignore content; remote and origin URLs
  agree and name the confirmed destination. Reject contradictory evidence, not
  merely missing or reordered variants.
- Reject known foreign state/resume JSON fields, including explicit zero values.

The current test-engineer was steered with these findings and asked to finish
with failing production regressions if needed, then release package ownership.
Lead production repairs follow that handoff; no parallel writes to the package.

## Typed effect-proof follow-up
The three Must-fix defects and foreign-field rejection now pass their regression
groups on main. The repair introduced a distinct core/proposals.CommentReceipt
and DB EncodeCommentReceipt/DecodeCommentReceipt; test-engineer must cover its
round-trip, independent accepted-implies-valid oracle, each missing/invalid field,
unknown field/trailing data and fuzz seeds. Exercise a comment effect's insert,
settle and load with matching marker/host ID, and reject mismatched marker/ID,
wrong stage/kind, unfinished effects carrying proof, duplicate terminal settlement,
and failure or unknown results that attempt to turn back into an intent.
Restore all controlled mutations and report incomplete expensive race/fuzz runs.
