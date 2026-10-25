// Package agentstest is the contract suite for agents.Adapter (AG-01, AG-02,
// AG-21; docs/AGENTS.md 5.5 and 5.11). Every adapter, the mock and the real
// ones alike, runs the same clauses, so the mock stays an honest stand-in for
// the real adapters and a new adapter cannot drift from the contract.
//
// The suite starts no process and calls no model API: the factory hands it an
// adapter that replays a recorded or scripted scenario (mock fixtures, a
// claudecode adapter over a fake CLI that prints a recorded stream, ...).
//
// # Clauses
//
//   - event_order: Execute numbers events 1..n; the run starts with a started
//     status; every event has a time; a tool result follows its tool call.
//   - event_payloads: each kind carries its payload (tool name, file edit with a
//     relative path and a known op, command line, usage, error text, ...).
//   - tree: ParentID links form a tree that agents.BuildTree builds without
//     orphans (nested scenario).
//   - final_status: the last event is a final status that matches what the
//     scenario expects, and Result.Status agrees.
//   - usage: Result.Usage equals an independent sum (Cumulative replaces,
//     sub-agent usage counts); cumulative totals never shrink; cost only
//     appears when Capabilities.CostReporting says so.
//   - cancel: cancelling the context ends the run, closes Events, Wait returns
//     and Execute reports RunCancelled.
//   - refusals: Execute refuses a nil Env and a resume on an adapter without
//     Capabilities.Resume, and never starts the adapter in those cases.
//   - capabilities: Enforcement and Subagents hold known values and agree with
//     the other flags and with what the events show.
//   - check: a missing prerequisite gives a plain-language error with a
//     UserMessage, no path and no stack.
//   - secrets: a sentinel secret placed in Env appears in no event, error or
//     log line.
//   - raw_lines: raw stream lines are not forwarded or logged.
//
// # Calling the suite from an adapter package
//
// The suite does not import adapter packages (internal/archtest keeps
// internal/agents free of them). The adapter's own test file wires it in. Put
// this in internal/agents/claudecode/contract_test.go (the same shape works
// for the opencode adapter):
//
//	func TestAdapterContract(t *testing.T) {
//		agentstest.AdapterContract(t, func(k agentstest.Kind) (agentstest.Subject, error) {
//			switch k {
//			case agentstest.KindPlain, agentstest.KindEdits, agentstest.KindError,
//				agentstest.KindNested, agentstest.KindCancel, agentstest.KindMissingPrereq:
//				// Build the adapter over its fake CLI / recorded stream for k.
//				return agentstest.Subject{
//					Adapter: newAdapterReplaying(t, k), // implements agents.Adapter
//					Options: agents.StartOptions{Dir: t.TempDir(), Mode: core.ModeAsk, Policy: askPolicy()},
//					// RawSentinel: a string present in the raw stream (an account
//					// email, say) that must never reach an Event or a log line.
//					RawSentinel: "ada@acme-shop.example",
//				}, nil
//			}
//			return agentstest.Subject{}, agentstest.ErrUnsupported
//		})
//	}
//
// Kinds an adapter cannot replay return ErrUnsupported and their clauses are
// skipped; KindPlain is required. For KindMissingPrereq return an adapter
// whose Check fails (CLI not installed). For KindCancel return an adapter that
// is still running when the context is cancelled. Subject.Cleanup, when set,
// runs after each scenario. The suite sets TaskID and Env itself (Env carries
// a sentinel secret), so Options needs only Dir, Mode, Policy and the like.
package agentstest
