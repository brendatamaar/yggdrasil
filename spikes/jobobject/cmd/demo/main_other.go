//go:build !windows

// Command demo is the manual Task Manager check for spike B2. Windows only.
package main

import (
	"fmt"
	"os"
)

func main() {
	fmt.Fprintln(os.Stderr, "demo: job objects are Windows-only")
	os.Exit(1)
}
