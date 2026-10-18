package db

import "strings"

// relPathOf derives repos.rel_path: the repo's folder under the project's
// folder, with forward slashes, "." for the project folder itself. Paths are
// compared without case, as Windows does. A repo outside the project folder
// (which tracking never produces) falls back to its name, which is unique per
// project.
func relPathOf(projectPath, repoPath, name string) string {
	norm := func(p string) string { return strings.TrimRight(strings.ReplaceAll(p, `\`, "/"), "/") }
	pp, rp := norm(projectPath), norm(repoPath)
	if strings.EqualFold(pp, rp) {
		return "."
	}
	if len(rp) > len(pp) && strings.EqualFold(rp[:len(pp)], pp) && rp[len(pp)] == '/' {
		return rp[len(pp)+1:]
	}
	return name
}
