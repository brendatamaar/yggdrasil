//go:build !windows

package agentstream

import (
	"errors"
	"fmt"
	"os/exec"
	"syscall"
)

// pgroup is the minimal non-Windows stand-in for a job: a process group.
// Unlike a job, a process that calls setsid leaves it. Experimental platforms
// only; not tested.
type pgroup struct{ pgid int }

func startInGroup(cmd *exec.Cmd) (group, error) {
	if cmd.SysProcAttr == nil {
		cmd.SysProcAttr = &syscall.SysProcAttr{}
	}
	cmd.SysProcAttr.Setpgid = true
	if err := cmd.Start(); err != nil {
		return nil, fmt.Errorf("start %s: %w", cmd.Path, err)
	}
	return &pgroup{pgid: cmd.Process.Pid}, nil
}

func (g *pgroup) terminate() error {
	if err := syscall.Kill(-g.pgid, syscall.SIGKILL); err != nil && !errors.Is(err, syscall.ESRCH) {
		return fmt.Errorf("kill process group %d: %w", g.pgid, err)
	}
	return nil
}

func (g *pgroup) active() int { return -1 }

func (g *pgroup) close() error { return g.terminate() }
