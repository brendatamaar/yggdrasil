# Threat model

Status: draft for v0.4.0 (Phase 3). Covers SF-08 and the safety invariants in docs/AGENTS.md section 6. It says what Yggdrasil defends against, what it deliberately does not, and where the edges are. Where a guarantee depends on the agent CLI, the evidence is cited (docs/spikes.md).

## What Yggdrasil protects

1. **Your main checkouts and remotes.** No agent changes them without a confirmed proposal (invariant 2). Force push, `reset --hard`, branch deletion and file deletion do not exist in the code (invariant 1).
2. **Your files outside the task's worktree.** Writes are limited to the task's worktree(s) (invariant 5).
3. **Your secrets.** Tokens never reach child processes, URLs, git config, logs or the audit log (invariants 3, 4).
4. **Your untracked folders.** Agents and MCP see tracked repos only (invariant 6).
5. **The local server.** It binds to `127.0.0.1` and checks Host and Origin (invariant 7).

## Who is the attacker

- **A confused or prompt-injected agent.** The main case. Text the agent reads (a README, an issue, a web page, a file in a repo) can tell it to do something harmful. The agent is not trusted; the model's own permission flags are only a second layer.
- **A hostile repo.** A repo you track can ship agent config (hooks, MCP servers, agents, skills, commands, `CLAUDE.md`), scripts and build files.
- **Another local process or user** that can reach `127.0.0.1`, or a web page in your browser (DNS rebinding, cross-site requests).
- **Not in scope:** an attacker who already runs code as your OS user outside Yggdrasil, a malicious agent CLI binary, a compromised dependency of Yggdrasil, physical access.

## How the enforcement works

**Policy decisions are pure and live in Yggdrasil** (`internal/core/guard`). The agent CLI asks Yggdrasil through a hook before every tool call; the hook (`ygg hook`, a child of the agent) reads the task's policy and decides:

- paths: each path the call names is resolved to its canonical long form (symlinks and junctions followed, 8.3 names expanded, alternate data streams, device names, UNC and `\\wsl$` forms refused) and must lie inside the task's roots; writes also may not touch `.git` or agent config files (those go through `propose_agent_file`);
- commands: the command line is parsed with a strict character allowlist (no backslash, `$`, braces, globs, `~`, `!`, `,`, `@`, `=` in extra arguments, quoting tricks) and matched against the task's allowlist; the program is resolved without searching the working directory and must not live inside a write root; shells, interpreters with free arguments, `git`, network clients and destructive tools can never be allowlisted (validated in the core);
- unknown tools, MCP tools of other servers, web tools, and any sub-agent that asks for worktree isolation are denied.

**Fail closed.** A missing or invalid policy, unreadable hook config, a hash mismatch, a full hook log, an unresolvable path or any error denies the call. The CLI is also told to block when the hook itself fails (`onFailure: block`, CLI 2.1.295 or newer; older CLIs are refused at start).

**Repo config is inert for Claude Code.** The adapter starts the CLI with `--setting-sources=` (empty list), `"disableAllHooks": false` in its own settings file, and `--strict-mcp-config` with Yggdrasil's own MCP file (empty unless the run has a Yggdrasil server). Verified with the real CLI (docs/spikes.md B5): a repo's `disableAllHooks: true`, hooks, `.mcp.json` servers, agents and commands do not load; the account's connectors (Docs, Gmail, ...) are not loaded either. The repo's `CLAUDE.md` text still reaches the model as prompt context.

**Trust (SF-09)** is still enforced by admission for adapters that cannot isolate repo config: an untrusted repo that ships agent config runs only in Ask mode, and trust is bound to the hash of the exact files you were shown; any change makes the repo untrusted again.

**Modes.** Ask is read-only: tools `Read`, `Grep`, `Glob`, no commands, plus an integrity check after every Ask turn (below). Code works in a worktree. Current-branch mode edits the main checkout (opt-in, warned; one editor per checkout; builds and git actions are locked out while a task edits it). Whole-project mode uses one worktree per repo (opt-in, warned). Write modes are off unless `YGG_WRITE_MODES=1`.

**Detection after the fact.**
- Ask integrity (SF-07): Yggdrasil snapshots the working tree before and after an Ask turn (HEAD, status, working diff, content of untracked files, refs). A difference stops the task and moves it to "Needs you"; nothing is reverted. A snapshot that fails, is too large (over 50,000 untracked files or 256 MiB), or times out also moves it to "Needs you" (it cannot be shown clean).
- Hook coverage: every tool use in the agent's stream must have a hook record; a missing one fails the task. The hook log is read before the run folder is deleted; denials are audited.
- Tamper detection: the policy, settings, hook and MCP files in the run folder are hashed at start and compared when the process ends; the hook also checks the policy hash on every call.

## What Yggdrasil does not protect against (accepted limits)

1. **Allowlisted commands run repo code.** `go test`, `npm run test`, `mvn test-compile` and the like execute code the agent may have just edited (a test file, a `package.json` script, a build plugin). The allowlist limits what the agent *starts*, not what a repo's own scripts then do. That code runs as your OS user with a clean environment (no tokens), inside the task's job object, but it can read files you can read and make network connections. Allowlist only commands you are willing to run on code the agent wrote. `go run`, `cargo run` and `uv run` are refused; `npm run <script>` is allowed because it is the standard way to test and build Node repos.
2. **Tamper detection is not tamper prevention.** A process started by an allowlisted command can rewrite the run folder (policy, hook config, log). The files are marked read-only and the hook checks the policy hash on each call, but the same OS user can change attributes; the end-of-run hash comparison detects it and fails the task, it does not undo what happened in between.
3. **The Ask snapshot has blind spots.** It does not see writes to ignored files (for example `.env`, build output), changes inside `.git` (config, hooks, refs other than the hash of the ref list), exec-bit flips on untracked files, or nested repos beyond their names. Ask mode has no write tool and no commands, so these need a bypass of the hook to matter; the snapshot is the second layer.
4. **The hook resolves programs with its own PATH,** which is the clean environment's PATH. Git Bash and PowerShell may resolve aliases or functions differently from the hook. The allowlist matches the resolved executable's base name; prefer rules for programs that are not shadowed.
5. **Prompt injection through `CLAUDE.md`/`AGENTS.md` and files the agent reads** can still steer the model; it can only ask for things the policy already allows. Review proposals before confirming them: the dialog shows what will run, not just the agent's summary.
6. **Local network access.** Any process under your OS user can open the loopback port. The server rejects wrong Host/Origin headers (browsers), but not another local process; the MCP endpoint needs a per-task bearer token. Remote access mode (RA-*) is not part of v0.4.0.
7. **Windows specifics.** The 0600 file mode is not an ACL on Windows; run folders live in your temp folder under your profile. Long paths: git fails for worktree paths of about 220 characters or more even with `core.longpaths`; keep the data dir short. Case folding is ASCII-only in the guard; look-alike Unicode names compare as different.
8. **Account data in the agent's stream** (MCP server lists, memory paths, rate-limit info) is not persisted raw; the adapter does not log raw stream lines.
9. **Reading `.git/config`** is denied to agents, but a repo's remote URLs can show up in git output run by the allowlist; credentials in remote URLs are refused by the proposal executors and redacted in logs.

## Added in Phase 3 (accepted limits and decisions)

10. **UI session.** Every `/api` and `/ws` route needs the `ygg_session` cookie, minted by opening the per-launch link `http://127.0.0.1:<port>/?k=<token>`. This stops agent-run code (for example an allowlisted `npm test`) from confirming its own proposals or reading task data. The token lives in memory only and is printed to the terminal and passed to the browser opener; it is redacted from logs and the audit log. Sessions do not expire during a run (a restart ends them). Any local process that can read the terminal output, the browser address bar or the `rundll32` command line while the browser opens can obtain the token. `YGG_INSECURE_NO_SESSION=1` is dev-only and refused when write modes are on. Real login arrives with remote mode (RA-*).
11. **MCP.** The endpoint `/mcp` is outside the session gate and uses a per-task bearer token (32 random bytes, only the hash is stored, valid only while that attempt runs, constant-time compare, any `Origin` header refused). Tools see tracked repos in the task scope only, and an untracked, unknown or out-of-scope repo get the same answer. Results are redacted, absolute paths are replaced by `<path>`, and output is capped. There is no tool that confirms or executes a proposal. A call is dropped, and gets no audit entry or effect, once its attempt is no longer live; an effect already running when a session is revoked is not interrupted.
12. **Per-run MCP config files** (`<data>/runs/mcp/<task>-<attempt>/ygg-mcp.json`, 0600) remain on disk after a run. They contain only a revoked token. Cleanup is not implemented (it would be a delete; pending a human decision).
13. **Builds in Ask mode** are allowed (AG-06). The Ask integrity check (SF-07) snapshots HEAD, `git status`, the diff, untracked file content and all refs; ignored build output (`target/`, `dist/`, `node_modules/`) is outside that scope, and a build that writes a tracked or non-ignored file trips the check.
14. **The path, branch and remote rules are copied** between `core/mcp` and `core/proposals` (core domains may not import each other); a drift test and fuzz targets compare them.
15. **Current-branch and whole-project modes** let the agent edit the main checkout (opt-in, warned, one editor per checkout, no build, pull or checkout while a task edits it). Whole-project mode is Claude Code only for now (hook enforcement).

## The one reading of invariant 2

MCP `pull` is a direct tool, not a proposal (decision 2026-10-25): fast-forward only, on a clean tree, with no build or editing task holding the checkout, audited with the old and new commit. `pull` is not offered or allowed in Ask mode (invariant 9). A fast-forward pull changes the working tree of a main checkout, so this is an explicit, narrow exception to "every mutation of a main checkout is a proposal". Fetch changes only refs. Everything else that changes a main checkout or a remote goes through a proposal you confirm.

## Known gaps before write modes are on by default

Tracked in docs/phases/phase-3.md: the per-agent trust scan for a second real adapter, OpenCode's enforcement level (needs its own spike; until then it is "limited" and restricted to worktree-only Code mode), process-level isolation of allowlisted commands (not planned for v1).

## Reporting

Found a way around a guard? Open an issue marked security, or contact the maintainer, with the steps to reproduce on Windows. Do not include real tokens or private repo contents.
