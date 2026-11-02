package main

import (
	"context"
	"errors"
	"fmt"
	"regexp"
	"strconv"

	"github.com/brendatamaar/yggdrasil/internal/platform"
)

// Git 2.38 added `git merge-tree --write-tree`, which lets a local merge
// (RV-05) find conflicts without touching the main checkout.
const (
	mergeTreeMajor = 2
	mergeTreeMinor = 38
)

var gitVersionRe = regexp.MustCompile(`(\d+)\.(\d+)(?:\.\d+)?`)

// parseGitVersion reads "git version 2.43.0.windows.1" into (2, 43).
func parseGitVersion(line string) (major, minor int, err error) {
	m := gitVersionRe.FindStringSubmatch(line)
	if m == nil {
		return 0, 0, fmt.Errorf("no version number in %q", line)
	}
	major, err = strconv.Atoi(m[1])
	if err != nil {
		return 0, 0, fmt.Errorf("version %q: %w", m[0], err)
	}
	minor, err = strconv.Atoi(m[2])
	if err != nil {
		return 0, 0, fmt.Errorf("version %q: %w", m[0], err)
	}
	return major, minor, nil
}

// gitMergeTreeAdvice judges a `git --version` line. nil means 2.38 or newer.
func gitMergeTreeAdvice(line string) error {
	major, minor, err := parseGitVersion(line)
	if err != nil {
		return advice("the Git version couldn't be read, so Yggdrasil can't tell whether local merges can use the fast conflict check. Local merge will still work.")
	}
	if major > mergeTreeMajor || (major == mergeTreeMajor && minor >= mergeTreeMinor) {
		return nil
	}
	return advice(fmt.Sprintf("Git %d.%d is older than 2.%d. Local merge will use the slower merge-then-abort path. Update Git for Windows from https://git-scm.com to get the faster check.", major, minor, mergeTreeMinor))
}

// gitMergeTreeProbe is an informational check (never required): an old Git
// still works, only slower.
func gitMergeTreeProbe(env []string) probe {
	return probe{name: "git merge-tree", check: func(ctx context.Context) (string, error) {
		line, err := versionOf(ctx, env, "git", []string{"--version"})
		if err != nil {
			if errors.Is(err, platform.ErrNotFound) {
				return "", advice("Git was not found, see the git check above.")
			}
			return "", advice("Git didn't run, see the git check above.")
		}
		if aerr := gitMergeTreeAdvice(line); aerr != nil {
			return "", aerr
		}
		return "Git 2.38 or newer: fast conflict check for local merge", nil
	}}
}
