//go:build windows

package main

import (
	"fmt"
	"path/filepath"

	"golang.org/x/sys/windows"
)

// shellCommand resolves a shell name from the ?shell= query parameter.
// Shells come from the system directory, not PATH.
func shellCommand(name string) (string, []string, error) {
	sys32, err := windows.GetSystemDirectory()
	if err != nil {
		return "", nil, fmt.Errorf("system directory: %w", err)
	}
	switch name {
	case "", "powershell":
		return filepath.Join(sys32, "WindowsPowerShell", "v1.0", "powershell.exe"), []string{"-NoLogo", "-NoProfile"}, nil
	case "cmd":
		return filepath.Join(sys32, "cmd.exe"), nil, nil
	}
	return "", nil, fmt.Errorf("unknown shell %q (use powershell or cmd)", name)
}
