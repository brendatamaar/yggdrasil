package repos

import (
	"sort"
	"strings"

	"github.com/brendatamaar/yggdrasil/internal/core"
)

// DefaultRepoName is the name offered for a repo found at rel under path: the
// folder's own name. For the folder itself (".") it is the last segment of
// path.
func DefaultRepoName(path CleanPath, rel RelPath) string {
	s := string(rel)
	if rel == "." {
		s = strings.ReplaceAll(string(path), `\`, "/")
	}
	s = strings.TrimRight(s, "/")
	if i := strings.LastIndex(s, "/"); i >= 0 {
		s = s[i+1:]
	}
	if ValidName(s) != nil {
		return "repo"
	}
	return s
}

// sortedRels returns the scan's repos without duplicates, in order.
func sortedRels(scan ScanResult) []RelPath {
	seen := map[RelPath]bool{}
	out := make([]RelPath, 0, len(scan.Repos))
	for _, r := range scan.Repos {
		if !seen[r] {
			seen[r] = true
			out = append(out, r)
		}
	}
	sort.Slice(out, func(i, j int) bool { return out[i] < out[j] })
	return out
}

func repoByRel(p Project) map[RelPath]Repo {
	m := make(map[RelPath]Repo, len(p.Repos))
	for _, r := range p.Repos {
		m[r.RelPath] = r
	}
	return m
}

// BuildPreview sets what a scan of path found against the workspace: which
// candidates are known rows, which are Active, and whether path is already a
// project.
func BuildPreview(path CleanPath, scan ScanResult, ws Workspace) Preview {
	pv := Preview{Path: path, Truncated: scan.Truncated, Skipped: scan.Skipped, Candidates: []Candidate{}}
	proj, exists := projectAt(ws, path)
	var rows map[RelPath]Repo
	if exists {
		id := proj.ID
		pv.Existing = &id
		rows = repoByRel(proj)
	}
	for _, rel := range sortedRels(scan) {
		c := Candidate{RelPath: rel, Name: DefaultRepoName(path, rel)}
		if row, ok := rows[rel]; ok {
			id := row.ID
			c.Known, c.Name, c.Active = &id, row.Name, isActive(row.Life)
		}
		pv.Candidates = append(pv.Candidates, c)
	}
	return pv
}

// Rescan compares a project's rows with a new scan of its folder. New lists
// repos with no row at all (an Untracked row is not new: tracking the folder
// again restores it). Missing lists Active rows the scan did not find; a
// truncated scan proves nothing, so then Missing is empty. An unknown or
// Untracked project gives an empty report.
func Rescan(scan ScanResult, ws Workspace, project core.ProjectID) RescanReport {
	rep := RescanReport{New: []Candidate{}, Missing: []core.RepoID{}}
	proj, ok := activeProject(ws, project)
	if !ok {
		return rep
	}
	rows := repoByRel(proj)
	found := map[RelPath]bool{}
	for _, rel := range sortedRels(scan) {
		found[rel] = true
		if _, known := rows[rel]; !known {
			rep.New = append(rep.New, Candidate{RelPath: rel, Name: DefaultRepoName(proj.Path, rel)})
		}
	}
	if scan.Truncated {
		return rep
	}
	for _, id := range repoIDs(proj) {
		if r := proj.Repos[id]; isActive(r.Life) && !found[r.RelPath] {
			rep.Missing = append(rep.Missing, id)
		}
	}
	return rep
}
