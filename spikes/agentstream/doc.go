// Package agentstream is the Phase 0 B4 spike: a harness that runs Claude
// Code headless with stream-json output, parses its events into a draft
// normalized Event, limits sub-agents through a PreToolUse hook and cancels the
// whole process tree through a job object.
//
// Part 1 (this code) is tested only with synthetic fixtures and fake
// processes; nothing here runs the real CLI in tests. The CLI details (flags,
// event shapes, hook protocol, env var names) are checked against `claude
// --help` 2.1.299 and the Claude Code docs; docs/spikes.md (B4) lists the
// sources and what only the part 2 live run can confirm.
//
// The binary lives in cmd/agentstream:
//
//	agentstream run --prompt "..." [--model haiku] [--max-budget-usd 0.10] [--keep]
//	agentstream hook --log <file> [--deny-mode exit2|json|json-legacy]
//	agentstream scrub --in <raw.jsonl> --out <fixture.jsonl> [--replace old=new]
//
// It's throwaway: Phase 1 rebuilds what it keeps in internal/agents and
// internal/platform.
package agentstream
