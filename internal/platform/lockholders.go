//go:build windows

package platform

import (
	"os"
	"path/filepath"
	"strings"
	"unicode"
)

// Bounds of LockHolders.
const (
	maxHolders     = 5
	maxSampleFiles = 200
	maxSampleDepth = 3
	maxSampleDirs  = 400
)

// sampleFiles lists the files to ask about: the file itself, or for a folder
// its top-level files first and then files breadth-first at shallow depth, at
// most maxSampleFiles. Links are not entered. Best effort; errors end the
// walk quietly.
func sampleFiles(path string) []string {
	fi, err := os.Lstat(path)
	if err != nil {
		return nil
	}
	if !fi.IsDir() {
		return []string{path}
	}
	var out []string
	level := []string{path}
	dirsSeen := 0
	for depth := 0; depth <= maxSampleDepth && len(level) > 0 && len(out) < maxSampleFiles; depth++ {
		var next []string
		for _, d := range level {
			if dirsSeen++; dirsSeen > maxSampleDirs {
				return out
			}
			entries, err := os.ReadDir(d)
			if err != nil {
				continue
			}
			for _, e := range entries {
				p := filepath.Join(d, e.Name())
				switch {
				case e.Type()&os.ModeSymlink != 0:
				case e.IsDir():
					next = append(next, p)
				case len(out) < maxSampleFiles:
					out = append(out, p)
				}
			}
		}
		level = next
	}
	return out
}

// cleanHolderNames removes control characters, drops empty names and
// duplicates and keeps at most maxHolders names.
func cleanHolderNames(names []string) []string {
	var out []string
	seen := map[string]bool{}
	for _, n := range names {
		n = strings.TrimSpace(strings.Map(func(r rune) rune {
			if unicode.IsControl(r) {
				return -1
			}
			return r
		}, n))
		if n == "" || seen[strings.ToLower(n)] {
			continue
		}
		seen[strings.ToLower(n)] = true
		out = append(out, n)
		if len(out) == maxHolders {
			break
		}
	}
	return out
}
