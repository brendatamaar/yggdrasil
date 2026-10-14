# Running Yggdrasil's build with Claude Code and subagents

## 1. One-time setup (Windows)

1. Create the repo folder and unzip the setup at its root. You'll get:
   - `docs/PRD.md`, `docs/AGENTS.md`, `docs/PLAN.md` (the planning docs)
   - `docs/PROMPTS.md` (this file)
   - `.claude/CLAUDE.md` (imports `docs/AGENTS.md` and adds the lead workflow)
   - `.claude/settings.json` (permissions, max 3 concurrent subagents)
   - `.claude/agents/*.md` (the five subagents)

   All Markdown docs live in `docs/`; only `README.md` (created in Phase 0) sits at the root.
2. Make the first commit on `main`:
   ```
   git init -b main
   git add .
   git commit -m "docs: planning docs and Claude Code setup"
   ```
   This matters: subagent worktrees branch from `main`, so they only see what's committed there.
3. Install what Phase 0 needs: Git, Go, Node + pnpm, [Task](https://taskfile.dev), and Claude Code signed in with your subscription.
4. Start Claude Code in the repo folder, trust the folder when asked (the allow rules in `.claude/settings.json` only apply after that), and run `/agents` to check that all five agents are listed.

Replace `<OWNER>` in the prompts below with your GitHub user or org for the Go module path (e.g. `github.com/<OWNER>/yggdrasil`).

---

## 2. Phase 0 kickoff (paste this first)

```
You are the lead for building Yggdrasil. Follow .claude/CLAUDE.md and docs/AGENTS.md exactly.

Start Phase 0 from docs/PLAN.md ("Spikes and project setup"). Go module path: github.com/<OWNER>/yggdrasil. Binary: ygg.

Step 1 - Plan only. Read docs/PLAN.md Phase 0, the docs/PRD.md requirements it references (IN-*, PL-*, D5, D12, D14), and docs/AGENTS.md sections 2-5. Write docs/phases/phase-0.md with:
- Batch A (you, sequential): repo skeleton per docs/AGENTS.md section 4, go.mod, Taskfile with all tasks from docs/AGENTS.md section 3 (stubs are fine where tools don't exist yet), Apache-2.0 LICENSE and NOTICE, .gitignore, README skeleton with the platform statement, docs/CHANGELOG.md, docs/CONTRIBUTING.md, .env.example skeleton, GitHub Actions CI (Windows required with CGO_ENABLED=0; macOS and Linux allowed to fail; license check), GoReleaser config that doesn't publish.
- Batch B (parallel, max 3 at once):
  - platform-windows: ConPTY spike with go-pty first (D12), in spikes/conpty/
  - platform-windows: job object spike, in spikes/jobobject/
  - go-backend: pure-Go SQLite spike (modernc + sqlc + goose, CGO_ENABLED=0), in spikes/sqlite/
  - go-backend: agent streaming spike in spikes/agentstream/, including the sub-agent checks listed in docs/PLAN.md. This one runs the real Claude Code CLI: ask me before it runs.
  - frontend: web/ scaffold (Vite + React + TS strict + pnpm, TanStack Query/Router, Zustand, Tailwind, Radix) serving an empty shell, embedded via go:embed into cmd/ygg
  - test-engineer: fixtures/demo-project generator script (acme-shop with shop-api, shop-web, shop-admin; history, branches)
- For each task: owner, PRD IDs, owned paths, definition of done.
Then stop and show me the plan. Don't spawn anyone yet.
```

After you approve the plan:

```
Plan approved. Do Batch A yourself and commit it to main. Then run Batch B following .claude/CLAUDE.md: max 3 subagents at once, merge one branch at a time with all task checks, then run safety-reviewer on the batch. Write every spike's findings to docs/spikes.md (what worked, what didn't, decision). Ask me before the agent streaming spike runs the real Claude Code CLI. Stop at phase close with the checklist from .claude/CLAUDE.md step 7.
```

---

## 3. Any later phase (Phases 1-11)

```
You are the lead for building Yggdrasil. Follow .claude/CLAUDE.md and docs/AGENTS.md exactly.

Start Phase <N> from docs/PLAN.md. Previous phases are tagged up to <LAST_TAG>.

Step 1 - Plan only. Read docs/PLAN.md Phase <N>, every docs/PRD.md requirement it references, the PRD decision log, docs/spikes.md, and the code you'll build on. Write docs/phases/phase-<N>.md:
- foundations first (shared interfaces, Event/core types, migrations, generated code) done by you or one agent and committed to main before any fan-out;
- then parallel batches of at most 3 subagents, where no two parallel tasks own the same file or package;
- for each task: owner agent, PRD IDs, owned paths, interfaces to use, definition of done;
- which docs/PLAN.md exit criteria each task contributes to, and what I'll have to verify by hand on Windows.
Then stop and show me the plan. Don't spawn anyone yet.
```

After approval:

```
Plan approved. Execute it following .claude/CLAUDE.md steps 2-7. Give me a short update after each batch is merged and reviewed. Stop at phase close.
```

---

## 4. Resume after a usage limit or interruption

```
Resume Phase <N>. Read docs/phases/phase-<N>.md, `git log --oneline -20`, `git worktree list` and `git branch --list`. Tell me:
- which tasks are merged, which branches finished but aren't merged, and which tasks need restarting;
- anything half-done in a worktree.
Then continue from the first unfinished step in .claude/CLAUDE.md. Don't redo merged work.
```

---

## 5. Close a phase (if it didn't stop on its own, or you want a re-check)

```
Close Phase <N>. Re-run task generate, lint, typecheck, test and test:web on main. Run safety-reviewer on `git diff <PHASE_START_COMMIT>..main`. Check every docs/PLAN.md exit criterion for Phase <N> with evidence. Give me the phase-close report from .claude/CLAUDE.md step 7 and the proposed tag. Don't tag or push.
```

Tag yourself after checking the manual list:

```
git tag -a v0.<N+1>.0 -m "Phase <N>: <name>"
```

---

## 6. Useful one-off prompts

Fix review findings:
```
Fix every Blocking finding from the last safety-reviewer report. Delegate to the owning agent if it's in their area, otherwise fix it yourself. Re-run safety-reviewer until PASS.
```

Add missing tests for a requirement:
```
Use test-engineer to add tests for <PRD IDs>. Include the invariant tests from docs/AGENTS.md section 6 that are still missing. Report any bugs as failing tests, don't fix production code.
```

Check progress against the PRD:
```
Without changing anything, list every docs/PRD.md requirement ID with status: done (with test name), partial, or not started. Group by docs/PLAN.md phase.
```

---

## 7. Tips

- **Parallelism vs limits.** `.claude/settings.json` caps concurrent subagents at 3. If you hit usage limits often, lower it to 2, or ask the lead to run batches one agent at a time.
- **Models.** Implementers use `sonnet`; the reviewer inherits your main model. Change `model:` in `.claude/agents/*.md` if you want otherwise.
- **Background work.** Press `Ctrl+B` to send a running subagent to the background. Its permission prompts still show up in the main session, labelled with its name.
- **Leftover worktrees.** Worktrees with changes stay on disk after a subagent finishes. Once the lead has merged a branch, you can remove its worktree yourself (`git worktree remove <path>`); the settings ask before Claude does it.
- **Destructive commands are denied**, both in Bash and PowerShell: force push, `reset --hard`, branch deletion, `git clean`, `rm -r`, `Remove-Item`. That's deliberate; do those yourself if ever needed.
- **Keep the import line in `.claude/CLAUDE.md`.** Claude Code only finds `AGENTS.md` on its own at the repo root, so the `@../docs/AGENTS.md` line at the top of `.claude/CLAUDE.md` is what loads the shared rules. Imports resolve relative to the file they're in, hence the `../`.
- **Other agents** (Codex, OpenCode) look for `AGENTS.md` at the root too. When you use them on this repo, point them to `docs/AGENTS.md`.
