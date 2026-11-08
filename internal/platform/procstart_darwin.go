//go:build darwin

package platform

import (
	"context"
	"errors"
	"os/exec"
	"strconv"
	"strings"
	"time"
)

// processStart returns the start time of pid as printed by ps, which identifies
// the running instance. Darwin support is minimal (docs/AGENTS.md 5.2).
func processStart(pid int) (string, error) {
	ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
	defer cancel()
	cmd := exec.CommandContext(ctx, "/bin/ps", "-o", "stat=,lstart=", "-p", strconv.Itoa(pid)) //nolint:gosec // fixed program, numeric argument
	cmd.Env = []string{"LC_ALL=C"}
	out, err := cmd.Output()
	if err != nil {
		return "", err
	}
	line := strings.TrimSpace(string(out))
	stat, start, ok := strings.Cut(line, " ")
	if !ok || start == "" {
		return "", errors.New("process not found")
	}
	if strings.HasPrefix(stat, "Z") {
		return "", errors.New("process has ended")
	}
	return strings.Join(strings.Fields(start), " "), nil
}
