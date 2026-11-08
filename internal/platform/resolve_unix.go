//go:build !windows

package platform

import "os"

const (
	mvnwFile    = "mvnw"
	gradlewFile = "gradlew"
	venvPython  = ".venv/bin/python"
)

func candidates(base string, _ []string) []string { return []string{base} }

func isExecutable(path string) bool {
	fi, err := os.Stat(path) //nolint:gosec // only stats; the path is a PATH candidate
	return err == nil && fi.Mode().IsRegular() && fi.Mode().Perm()&0o111 != 0
}
