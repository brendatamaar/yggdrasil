//go:build !windows

package platform

import (
	"context"
	"fmt"
	"os/exec"
	"runtime"
	"syscall"
)

// OpenEditor starts editor with path as its only argument, in its own session
// so it outlives ygg (PL-05). env must be the clean environment.
func OpenEditor(ctx context.Context, editor string, path string, env []string) error {
	if env == nil {
		return ErrNoEnv
	}
	if err := CheckPath(path); err != nil {
		return err
	}
	exe, err := lookPath(editor, "", env)
	if err != nil {
		return err
	}
	return launchDetached(ctx, exe, []string{exe, path}, env)
}

// OpenFolder shows path in the desktop's file manager, outside ygg.
func OpenFolder(ctx context.Context, path string, env []string) error {
	if env == nil {
		return ErrNoEnv
	}
	if err := CheckPath(path); err != nil {
		return err
	}
	opener := "xdg-open"
	if runtime.GOOS == "darwin" {
		opener = "open"
	}
	exe, err := lookPath(opener, "", env)
	if err != nil {
		return err
	}
	return launchDetached(ctx, exe, []string{exe, path}, env)
}

func launchDetached(ctx context.Context, exe string, argv, env []string) error {
	if err := ctx.Err(); err != nil {
		return err
	}
	cmd := &exec.Cmd{Path: exe, Args: argv, Env: env, SysProcAttr: &syscall.SysProcAttr{Setsid: true}}
	if err := cmd.Start(); err != nil {
		return fmt.Errorf("start %s: %w", exe, err)
	}
	// Reap it when it exits; the goroutine ends with the process.
	go func() { _ = cmd.Wait() }()
	return nil
}
