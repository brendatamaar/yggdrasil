//go:build !windows

package platform

import (
	"os"
	"path/filepath"
)

// DefaultShell returns the argv of the terminal shell (DV-04): $SHELL if it
// names an executable file, else /bin/sh. The environment of the shell itself
// is the caller's to build (the clean env).
func DefaultShell() []string {
	return shellFrom(os.Getenv("SHELL"))
}

func shellFrom(shell string) []string {
	if shell != "" && filepath.IsAbs(shell) && isExecutable(shell) {
		return []string{shell}
	}
	return []string{"/bin/sh"}
}
