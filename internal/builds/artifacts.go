package builds

import (
	"io/fs"
	"path"
	"slices"
	"strings"

	"github.com/brendatamaar/yggdrasil/internal/core"
	corebuilds "github.com/brendatamaar/yggdrasil/internal/core/builds"
)

// maxArtifacts bounds what one repo reports.
const maxArtifacts = 1000

// ArtifactFinder lists the files a successful build left in a repo (BL-07).
// It is read only.
type ArtifactFinder interface {
	// Find returns the artifacts of repo, rooted at fsys, sorted by path. The
	// result is never nil.
	Find(fsys fs.FS, repo core.RepoID) ([]corebuilds.Artifact, error)
}

// ToolchainArtifacts finds artifacts with the glob patterns of every
// toolchain that detects the repo.
type ToolchainArtifacts struct{ Toolchains []Toolchain }

// Find implements ArtifactFinder.
func (t ToolchainArtifacts) Find(fsys fs.FS, repo core.RepoID) ([]corebuilds.Artifact, error) {
	var patterns []string
	for _, tc := range t.Toolchains {
		if d, ok := tc.Detect(fsys); ok {
			patterns = append(patterns, tc.Artifacts(d)...)
		}
	}
	return FindArtifacts(fsys, repo, patterns)
}

// FindArtifacts matches patterns (forward slashes, relative to the root of
// fsys; "*" and "?" as in path.Match within one segment, "**" for any number
// of folders) against the regular files of fsys. Patterns that leave the root
// are ignored. The result is sorted, without duplicates, never nil.
func FindArtifacts(fsys fs.FS, repo core.RepoID, patterns []string) ([]corebuilds.Artifact, error) {
	found := map[string]int64{}
	for _, p := range patterns {
		segs := strings.Split(p, "/")
		if p == "" || path.IsAbs(p) || slices.Contains(segs, "..") {
			continue
		}
		root := staticPrefix(segs)
		if root != "." && !fs.ValidPath(root) {
			continue
		}
		if st, err := fs.Stat(fsys, root); err != nil || !st.IsDir() {
			if err == nil && matchSegments(segs, strings.Split(root, "/")) && st.Mode().IsRegular() {
				found[root] = st.Size()
			}
			continue
		}
		_ = fs.WalkDir(fsys, root, func(name string, d fs.DirEntry, err error) error {
			if err != nil {
				return nil //nolint:nilerr // unreadable entries are skipped, not fatal
			}
			if d.IsDir() || !d.Type().IsRegular() || !matchSegments(segs, strings.Split(name, "/")) {
				return nil
			}
			if len(found) >= maxArtifacts {
				return fs.SkipAll
			}
			if info, ierr := d.Info(); ierr == nil {
				found[name] = info.Size()
			}
			return nil
		})
	}
	out := make([]corebuilds.Artifact, 0, len(found))
	for name, size := range found {
		out = append(out, corebuilds.Artifact{RepoID: repo, Path: name, Size: size})
	}
	slices.SortFunc(out, func(a, b corebuilds.Artifact) int { return strings.Compare(a.Path, b.Path) })
	return out, nil
}

// staticPrefix is the longest leading folder path of the pattern without
// wildcards, so the walk starts as deep as possible.
func staticPrefix(segs []string) string {
	var keep []string
	for _, s := range segs[:len(segs)-1] {
		if strings.ContainsAny(s, "*?[\\") {
			break
		}
		keep = append(keep, s)
	}
	if len(keep) == 0 {
		return "."
	}
	return strings.Join(keep, "/")
}

// matchSegments matches a name against a pattern, segment by segment.
func matchSegments(pat, name []string) bool {
	if len(pat) == 0 {
		return len(name) == 0
	}
	if pat[0] == "**" {
		for i := 0; i <= len(name); i++ {
			if matchSegments(pat[1:], name[i:]) {
				return true
			}
		}
		return false
	}
	if len(name) == 0 {
		return false
	}
	if ok, err := path.Match(pat[0], name[0]); err != nil || !ok {
		return false
	}
	return matchSegments(pat[1:], name[1:])
}
