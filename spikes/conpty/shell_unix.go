//go:build !windows

package main

import "fmt"

// shellCommand resolves a shell name from the ?shell= query parameter.
func shellCommand(name string) (string, []string, error) {
	switch name {
	case "", "sh":
		return "/bin/sh", nil, nil
	}
	return "", nil, fmt.Errorf("unknown shell %q (use sh)", name)
}
