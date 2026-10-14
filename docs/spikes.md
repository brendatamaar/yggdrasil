# Phase 0 spikes

Findings from the Phase 0 spikes (docs/PLAN.md). Spike code lives in `spikes/<name>/`, one Go module each. It's throwaway: Phase 1 rebuilds the parts it keeps in `internal/platform`, under the full rules in docs/AGENTS.md.

Status key: **auto** = covered by the spike's automated tests on Windows; **manual** = on the human checklist at the end; **pending** = not run yet.

---

## B1 — ConPTY terminal (`spikes/conpty`) · D12, PL-02, DV-04

**Setup.** A small `PTY` interface (start with a size, Read/Write, Resize, Wait for the exit code, Close). Implemented with `aymanbagabas/go-pty` v0.2.2. A demo server on `127.0.0.1:4848` bridges the PTY to xterm.js over `coder/websocket`: binary frames carry data, JSON text frames carry control messages (`resize`, `started`, `exit`). Each shell runs in its own job object.

| Check | PowerShell | cmd |
|---|---|---|
| (a) output, keystrokes, Ctrl+C interrupts the command and not the shell | auto | auto |
| (b) resize reaches the shell | auto | auto (`mode con`) |
| (c) interactive program | auto | auto (`choice`) |
| (d) `exit 3` gives exit code 3, socket closes with "exit code 3" | auto | auto |
| (e) closing the socket kills the shell tree, no `conhost`/`OpenConsole` left | auto | auto |
| (f) unicode and colors in the browser | manual | manual |
| Browser end to end (xterm.js, window resize) | manual | manual |

**Verdict: go-pty works, but we don't adopt it (D12).** Phase 1 builds its own thin wrapper on `x/sys/windows`, with `creack/pty` on Unix. go-pty v0.2.2 has four problems:
1. It leaks the `CreateProcess` process handle on every start (`cmd_windows.go`: it calls `os.FindProcess` and never closes `pi.Process`).
2. `Close` closes the pseudo console and both pipes at once, so unread output is lost.
3. It closes the thread handle. To join a job before the shell runs, the spike had to find the thread again with a Toolhelp snapshot.
4. If `Cmd.Env` is nil it uses the parent environment (an SF-04 risk). It also pins `x/sys` v0.16.0.

**Gotchas for Phase 1:**
- **Close order:** `ClosePseudoConsole` first, then drain the output pipe to EOF, then close the pipes. Keep reading even after the socket dies, because ConPTY can block on a full output pipe.
- **Ctrl+C:** a shell inherits the "ignore Ctrl+C" flag. The spike clears it once with `SetConsoleCtrlHandler(NULL, FALSE)`. The side effect is that Ctrl+C in `ygg`'s own console then stops `ygg`, so decide this in Phase 1.
- **`.mjs` MIME type:** Windows' registry often maps `.mjs` to `text/plain`, so register `text/javascript` (relevant to `cmd/ygg` too, if Vite emits `.mjs`).
- **Locale:** the tests match English console output (`mode con`, `choice`) and were run on Windows 11 22631 only. 24H2 changed when closing a console blocks.
- **Security:** the Host check plus WebSocket Origin check stops browsers, but any local process can open a shell. DV-04's single-use socket token is required in Phase 5. Serve static assets from an explicit allowlist rather than `http.Dir`, which doesn't block Windows device names such as `CON`.

**Phase 1 wrapper:** `CreatePseudoConsole` + `PROC_THREAD_ATTRIBUTE_PSEUDOCONSOLE` and `PROC_THREAD_ATTRIBUTE_JOB_LIST` in one attribute list, then `CreateProcess`. Close every handle. Require the environment from `internal/safety`; nil is not allowed. Use the close order above. B1's `PTY` interface carries over.

---

## B2 — Job objects (`spikes/jobobject`) · PL-02, PL-05

**Setup.** Jobs with `JOB_OBJECT_LIMIT_KILL_ON_JOB_CLOSE`, a non-inheritable handle, and nested per-task jobs. Four ways of getting children in were tested:

| Approach | How | Race-free | Notes |
|---|---|---|---|
| self | `ygg` assigns itself; children inherit | yes | Editors then need `BREAKAWAY_OK` on the main job (see the decision below) |
| perchild | `CREATE_SUSPENDED` → assign → resume (os/exec + Toolhelp) | yes | os/exec hides the thread handle |
| createprocess | raw `CreateProcess` suspended → assign → `ResumeThread` | yes | Keeps the thread handle |
| joblist | `PROC_THREAD_ATTRIBUTE_JOB_LIST` (Windows 10+) | yes | In the job from creation, with no suspend/resume |

| Check | Status |
|---|---|
| Kill the `ygg` stand-in: child and grandchild die (all four approaches) | auto |
| Nested task job: cancelling the context kills only that task's tree | auto |
| Outside-job process survives `ygg` | auto (breakaway or broker) |
| Kill the demo from Task Manager: children die, Notepad stays open | manual |

**Breakaway (PL-05):** `CREATE_BREAKAWAY_FROM_JOB` only leaves jobs that allow it. Under an outer job without `BREAKAWAY_OK` (some terminals, IDEs, CI), `CreateProcess` succeeds but the process silently stays in that outer job. The spike's fallback starts the process with Explorer as its parent (`PROC_THREAD_ATTRIBUTE_PARENT_PROCESS`). Only automated runs inside an outer job were tested; running with no outer job at all is untested.

**Decision for Phase 1 (lead):**
- **joblist / createprocess, not self-assign.** If `ygg` sits in its own job, editors need `BREAKAWAY_OK`. But then any agent or build process could break away and survive `ygg` (invariants 5 and 11). With per-child assignment `ygg` stays outside its jobs, and nothing in them may break away.
- **One main job (`KILL_ON_JOB_CLOSE`, no `BREAKAWAY_OK`), with a nested job per task.** Cancelling a task terminates its job.
- **Editors and Explorer (PL-05)** start outside every `ygg` job. Inside a foreign outer job, prefer asking Explorer to launch them through COM (`IShellDispatch2.ShellExecute`) over parent-process spoofing, which antivirus/EDR tools often flag. Test against Defender.
- **Every spawn uses the clean environment**, including editors. The spike's nil-env default must not carry over.

---

## B3 — Pure-Go SQLite · pending (Wave 2)

## B4 — Agent streaming · pending (Wave 2, needs the human's go-ahead to run the real CLI)

---

## Manual checklist (human, Windows)

**B1 terminal:**
1. Run `pnpm -C web install --frozen-lockfile`, then `go run .` in `spikes/conpty`, and open `http://127.0.0.1:4848/`.
2. PowerShell: type commands, use the arrow keys, Ctrl+C a running `ping -t 127.0.0.1`. Expected: the ping stops and the shell stays.
3. Resize the browser window, then check `$Host.UI.RawUI.WindowSize`. Expected: it matches the new size.
4. Run `Read-Host`, type an answer. Expected: it's echoed back.
5. Run `Write-Host -ForegroundColor Green "é世😀"`. Expected: green text with correct glyphs.
6. Run `exit 3`. Expected: `[process exited with code 3]`.
7. Repeat steps 2–6 with `?shell=cmd`, using `mode con` and `choice /c yn`.
8. In cmd, run `start /b ping -n 300 127.0.0.1`, note the `cmd.exe`, `PING.EXE` and `conhost.exe` PIDs in Task Manager, then close the tab. Expected: all three are gone within about 1 s.
9. Ctrl+C the server. Expected: no spike `cmd.exe`, `powershell.exe` or `conhost.exe` is left.

**B2 job objects:**
1. In `spikes/jobobject`, run `go run ./cmd/demo` and note the printed PIDs.
2. In Task Manager → Details, end the `demo.exe` process.
3. Expected: every printed PID is gone within about 2 s, and the Notepad window stays open. On Windows 11 Notepad may hand off to the Store app; the visible window should survive.
4. Repeat with `-approach perchild`.
5. Close Notepad yourself.
6. After `go test` in either spike, no `*.test.exe` is left in Task Manager.
