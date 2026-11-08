//go:build windows

package platform

import (
	"bytes"
	"context"
	"os/exec"
	"path/filepath"
	"strings"
	"time"
)

// launcherTimeout bounds the py launcher query.
const launcherTimeout = 15 * time.Second

// launcherPython runs `py -3 -c "import sys; print(sys.executable)"` with the
// clean env and returns the interpreter path it printed, or "" if the launcher
// is missing or fails. Asking the launcher for the real path means callers get
// one program to run (`<path> -m venv .venv`), not a program plus `-3`.
func launcherPython(env []string) string {
	py, err := lookPath("py", "", env)
	if err != nil {
		return ""
	}
	ctx, cancel := context.WithTimeout(context.Background(), launcherTimeout)
	defer cancel()
	cmd := exec.CommandContext(ctx, py, "-3", "-c", "import sys; print(sys.executable)") //nolint:gosec // py resolved from the PATH of the clean env; fixed arguments
	cmd.Env = env
	var out bytes.Buffer
	cmd.Stdout = &out
	if err := cmd.Run(); err != nil {
		return ""
	}
	lines := strings.Split(strings.TrimSpace(out.String()), "\n")
	p := strings.TrimSpace(lines[len(lines)-1])
	if !filepath.IsAbs(p) || !isRegular(p) {
		return ""
	}
	return p
}

// isStoreStub reports whether p is a Microsoft Store alias (an App Execution
// Alias in ...\WindowsApps), which opens the Store instead of running Python.
func isStoreStub(p string) bool {
	return strings.Contains(strings.ToLower(filepath.ToSlash(p)), "/microsoft/windowsapps/")
}
