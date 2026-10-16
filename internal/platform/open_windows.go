//go:build windows

package platform

import (
	"context"
	"errors"
	"fmt"
	"os/exec"
	"path/filepath"
	"syscall"

	"golang.org/x/sys/windows"
)

// errNeedShell: the process could not leave the outer job ygg runs in.
var errNeedShell = errors.New("still inside an outer job")

// OpenEditor starts editor (a program name or path) with path as its only
// argument, outside every ygg job so it survives ygg (PL-05). env must be the
// clean environment (ErrNoEnv otherwise). Inside a foreign outer job that
// forbids breakaway, Windows Explorer starts the editor instead; it then has
// Explorer's environment, which holds none of ours.
func OpenEditor(ctx context.Context, editor string, path string, env []string) error {
	if env == nil {
		return ErrNoEnv
	}
	path, err := filepath.Abs(path) // a leading "-" must not read as an option
	if err != nil {
		return err
	}
	if err := CheckPath(path); err != nil {
		return err
	}
	exe, err := lookPath(editor, "", env)
	if err != nil {
		return err
	}
	return launchOutside(ctx, exe, []string{exe, path}, env)
}

// OpenFolder shows path in Windows Explorer, outside every ygg job (PL-05).
// Explorer comes from SystemRoot of env, never from the PATH.
func OpenFolder(ctx context.Context, path string, env []string) error {
	if env == nil {
		return ErrNoEnv
	}
	path, err := filepath.Abs(path) // a leading "-" must not read as an option
	if err != nil {
		return err
	}
	if err := CheckPath(path); err != nil {
		return err
	}
	root := envGet(env, "SystemRoot")
	if root == "" {
		return errors.New("platform: SystemRoot is not set in the environment, cannot find explorer.exe")
	}
	exe := filepath.Join(root, "explorer.exe")
	if !filepath.IsAbs(exe) || !isRegular(exe) {
		return fmt.Errorf("platform: %s not found, check SystemRoot", exe)
	}
	return launchOutside(ctx, exe, []string{exe, path}, env)
}

// launchOutside starts exe so that it is in no job at all (spike B2):
// CREATE_BREAKAWAY_FROM_JOB | CREATE_SUSPENDED, check job membership before the
// process runs, and fall back to Explorer when an outer job holds it.
func launchOutside(ctx context.Context, exe string, argv, env []string) error {
	if err := ctx.Err(); err != nil {
		return err
	}
	err := startBreakaway(exe, argv, env)
	if !errors.Is(err, errNeedShell) {
		return err
	}
	return shellExecute(exe, windows.ComposeCommandLine(argv[1:]), "")
}

func startBreakaway(exe string, argv, env []string) error {
	cmd := &exec.Cmd{
		Path: exe, Args: argv, Env: env,
		SysProcAttr: &syscall.SysProcAttr{CreationFlags: windows.CREATE_BREAKAWAY_FROM_JOB | windows.CREATE_SUSPENDED},
	}
	if err := cmd.Start(); err != nil {
		// Some outer jobs refuse the breakaway flag outright.
		if errors.Is(err, windows.ERROR_ACCESS_DENIED) {
			return errNeedShell
		}
		return fmt.Errorf("start %s: %w", exe, err)
	}
	pid := cmd.Process.Pid
	h, err := windows.OpenProcess(windows.PROCESS_QUERY_LIMITED_INFORMATION, false, uint32(pid)) //nolint:gosec // pids are positive 32-bit values
	if err != nil {
		_ = cmd.Process.Kill()
		_ = cmd.Wait()
		return fmt.Errorf("open pid %d: %w", pid, err)
	}
	in, err := isProcessInJob(h, 0)
	_ = windows.CloseHandle(h)
	if err != nil || in {
		// Never ran, so killing it is harmless.
		_ = cmd.Process.Kill()
		_ = cmd.Wait()
		if err != nil {
			return err
		}
		return errNeedShell
	}
	if err := resumeProcess(pid); err != nil {
		_ = cmd.Process.Kill()
		_ = cmd.Wait()
		return err
	}
	return cmd.Process.Release()
}
