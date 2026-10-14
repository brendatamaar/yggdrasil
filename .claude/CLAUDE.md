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
   Show the breakdown to the human and wait for approval before spawning anyone.

2. **Foundations before fan-out.** Shared contracts (interfaces, `Event` types, core types, migrations, Taskfile, generated code) are done first, by you or by one agent, then **committed to `main`**.
   Subagent worktrees branch from the default branch, **not from your current HEAD**. Anything uncommitted on `main` is invisible to them.

3. **Parallel batches.** Spawn at most **3 implementation subagents at once**. Each brief must contain:
   - goal and PRD IDs;
   - files/packages it owns, and what it must not touch;
   - interfaces and types it must use (with paths);
   - definition of done (which `task` commands must pass);
   - "commit on your branch and end with the report format from your agent file".

4. **Integrate one branch at a time.** For each finished subagent: read its report, review its diff, `git merge --no-ff <branch>` into `main`, then run `task generate`, `task lint`, `task typecheck`, `task test`, `task test:web`. Fix breakage before merging the next branch. If two branches conflict on a shared contract, stop and resolve the contract yourself.

5. **Review gate.** After each batch is merged, run `safety-reviewer` on `git diff <batch-start>..main`. Fix every **Blocking** finding (delegate or fix yourself), then re-run the reviewer until PASS.

6. **Bookkeeping.** Tick docs/PLAN.md checkboxes, update `docs/CHANGELOG.md`, and record decisions or deviations in `docs/phases/phase-<n>.md`. If a requirement must change, update docs/PRD.md and say so.

7. **Phase close.** Check every exit criterion in docs/PLAN.md for the phase. Then stop and give the human:
   - what was done, with PRD IDs;
   - exit criteria: met / not met, with evidence (test names, commands run);
   - a **manual verification checklist** for anything only a human on Windows can confirm;
   - the proposed tag (e.g. `v0.1.0`).
   Do **not** tag, push, or start the next phase until the human says so.

## Hard limits for this repo

- Never push, tag, force anything, delete branches, or delete files without the human asking. Proposals and confirmations are the product's philosophy; follow them while building it.
- Never run an agent CLI or model API in automated tests. Real agent CLIs are allowed only in Phase 0 spikes and in manual verification, and only when the human has agreed in that session.
- If a subagent reports that a task needs a safety invariant weakened, stop and ask the human.
- If you're unsure what a requirement means, check the docs/PRD.md decision log, then ask. Don't guess on safety, data model or public interfaces.
- Keep the human informed with short updates between batches, not a running commentary.

## Token budget

The human uses a Claude subscription with usage limits. Prefer:
- `sonnet` subagents for implementation (already set in their files);
- small, well-scoped briefs over "build the whole phase";
- reading only the PRD sections a task needs, not the whole file each time.
If you hit a usage limit mid-batch, write the state of each in-flight task to `docs/phases/phase-<n>.md` so the work can resume cleanly.
