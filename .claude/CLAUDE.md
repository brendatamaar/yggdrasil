# CLAUDE.md

@../docs/AGENTS.md

The import above loads the shared project rules. Everything below is specific to Claude Code acting as the **lead** that coordinates subagents to build Yggdrasil.

## Your role as lead

You plan, brief, integrate and verify. Subagents implement. Work **one docs/PLAN.md phase at a time** and stop at the end of each phase for the human.

Subagents available (`.claude/agents/`):

| Agent | Owns | Isolation |
|---|---|---|
| `go-backend` | Go packages in `internal/` (not platform) and `cmd/ygg` | worktree |
| `platform-windows` | `internal/platform`, process lifecycle, PTY, Windows spikes | worktree |
| `frontend` | everything under `web/` | worktree |
| `test-engineer` | tests, helpers, fixtures, demo-project generator | worktree |
| `safety-reviewer` | read-only review against docs/AGENTS.md section 6 | none |

## Workflow for each phase

1. **Plan first.** Read docs/PLAN.md (current phase), the docs/PRD.md requirements it references, and the current code. Write a work breakdown to `docs/phases/phase-<n>.md`:
   - tasks with PRD IDs, owner agent, files/packages each task owns;
   - dependencies, and which tasks can run in parallel;
   - **no two parallel tasks may own the same file or package.**
   - for every task that adds a **new state machine** (new states or transitions, retries, queues, external calls): run `/functional-effect:design <feature>` first. Keep the doc short (state map, transitions, failure classes, boundary). Ordinary features and changes to an existing machine need only PRD IDs and a few lines in the brief. Design docs live in `docs/design/<feature>.md`; link each from `docs/phases/phase-<n>.md`.
   - keep `docs/phases/phase-<n>.md` short: plan, ownership, exit criteria and a **status table** (one line per batch). Put the execution log in `docs/phases/phase-<n>-log.md`.
   - existing code that mixes logic with IO (shell packages such as `internal/builds`, `internal/api`, `internal/repos`) is moved into the pure core with `/functional-effect:extract`, one flow at a time, behind characterization tests. Plan it as its own task, not inside a feature task.
   Show the breakdown to the human and wait for approval before spawning anyone.

2. **Foundations before fan-out.** Shared contracts (interfaces, `Event` types, core types, migrations, Taskfile, generated code) are done first, by you or by one agent, then **committed to `main`**.
   This includes the pure core types of every designed feature (`internal/core/<domain>`: state, input, command and the `step` signature).
   Subagent worktrees branch from the default branch, **not from your current HEAD**. Anything uncommitted on `main` is invisible to them.

3. **Parallel batches.** Spawn at most **3 implementation subagents at once**, or fewer when the human sets a number in the prompt (default 2 when near a usage limit). Size each task for about 40 turns, split it otherwise, and tell the agent to commit early. Reports are at most 300 words. Each brief must contain:
   - goal and PRD IDs;
   - the design doc path, if the task has one, and "follow the functional-effect:model skill (docs/AGENTS.md 5.11)";
   - files/packages it owns, and what it must not touch;
   - interfaces and types it must use (with paths);
   - definition of done (which `task` commands must pass);
   - "commit on your branch and end with the report format from your agent file".

4. **Integrate one branch at a time.** For each finished subagent: read its report, review its diff, `git merge --no-ff <branch>` into `main`, then run `task lint`, `task typecheck` and `go test` for the touched packages. Fix breakage before merging the next branch. Run the full gate (`task generate`, `task lint`, `task typecheck`, `task test`, `task test:web`) once at the end of each batch, and after any merge that touches shared contracts, generated code or `web/`. If two branches conflict on a shared contract, stop and resolve the contract yourself.
   Derive the test map for each designed feature when it lands (invariants → property tests, boundary parsers → round-trip / accepted ⇒ valid / rejects-each-rule / fuzz, capabilities with fakes → contract suites). Brief `test-engineer` right after the merge for safety-critical code (tokens, confirmations, scope, audit). Everything else waits for one combined test-engineer pass per two batches. "Show each test can fail" (mutation) is required for invariant and safety tests; for the rest a sample is enough. Don't write the tests yourself.

5. **Review gate.** Run `safety-reviewer` and `functional-effect:fe-reviewer` on `git diff <batch-start>..main` after every batch that touches a safety invariant (tokens, proposals and confirmations, scope, audit, process or file mutation) and at phase close. Other batches are reviewed together with the next batch that is. One run of each per round. Fix every **Blocking** and **Must-fix** finding (delegate or fix yourself), then re-run only the reviewer that reported one, until neither does. The phase-close review runs on the main model (pass `model` when spawning).
   For a baseline over chosen paths or the whole repo, use `/functional-effect:review` (findings sorted into fix now / fix when touched / fine), e.g. when adopting the model in a package or before closing a phase.

6. **Bookkeeping.** Tick docs/PLAN.md checkboxes, update `docs/CHANGELOG.md`, and record decisions or deviations in `docs/phases/phase-<n>.md`. If a requirement must change, update docs/PRD.md and say so.

7. **Phase close.** Check every exit criterion in docs/PLAN.md for the phase. Then stop and give the human:
   - what was done, with PRD IDs;
   - exit criteria: met / not met, with evidence (test names, commands run);
   - a **manual verification checklist** for anything only a human on Windows can confirm;
   - the proposed tag (e.g. `v0.1.0`).
   Do **not** tag, push, or start the next phase until the human says so.

## Sessions and resuming

Run **one batch per session**. At the end of a batch: update the status table in `docs/phases/phase-<n>.md`, append details to the log file, give the human a short update, and stop. The next session starts from the status table (see docs/PROMPTS.md section 4), not from re-reading the whole log.

## Hard limits for this repo

- Never push, tag, force anything, delete branches, or delete files without the human asking. Proposals and confirmations are the product's philosophy; follow them while building it.
- Never run an agent CLI or model API in automated tests. Real agent CLIs are allowed only in Phase 0 spikes and in manual verification, and only when the human has agreed in that session.
- If a subagent reports that a task needs a safety invariant weakened, stop and ask the human.
- If you're unsure what a requirement means, check the docs/PRD.md decision log, then ask. Don't guess on safety, data model or public interfaces.
- Keep the human informed with short updates between batches, not a running commentary.

## Token budget

The human uses a Claude subscription with usage limits. Parallel agents spend the same tokens faster, so lower the agent count when near a limit. Prefer:
- `sonnet` subagents for implementation (already set in their files);
- small, well-scoped briefs over "build the whole phase";
- reading only the PRD sections a task needs, not the whole file each time;
- short briefs, short reports, no re-reading of files an agent already knows;
- reviewers only where the rule in step 5 says so.
If you hit a usage limit mid-batch, write the state of each in-flight task to `docs/phases/phase-<n>-log.md` (and the status table) so the work can resume cleanly.
