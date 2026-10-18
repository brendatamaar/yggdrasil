package repos

import (
	"fmt"
	"sort"
	"strings"
	"time"

	"github.com/brendatamaar/yggdrasil/internal/core"
)

// Decide is the tracking step: (workspace2, commands, outcome) =
// Decide(workspace1, input). An input that is illegal for the workspace
// returns it unchanged, no commands and the outcome that says why. Decide
// never changes the maps of its argument.
func Decide(ws Workspace, in Input) (Workspace, []Command, Outcome) {
	switch in := in.(type) {
	case TrackProject:
		return trackProject(ws, in)
	case TrackRepos:
		return trackRepos(ws, in)
	case UntrackRepo:
		return untrackRepo(ws, in)
	case UntrackProject:
		return untrackProject(ws, in)
	case RenameProject:
		return renameProject(ws, in)
	}
	return ws, nil, NotFound{}
}

// Run feeds inputs to Decide in order and returns the final workspace, every
// command in order and every outcome in order. The property tests drive
// Decide through it.
func Run(ws Workspace, inputs []Input) (Workspace, []Command, []Outcome) {
	var cmds []Command
	var outs []Outcome
	for _, in := range inputs {
		var c []Command
		var o Outcome
		ws, c, o = Decide(ws, in)
		cmds = append(cmds, c...)
		outs = append(outs, o)
	}
	return ws, cmds, outs
}

// Path comparison. A CleanPath is canonical: the shell's ParseFolder makes one
// spelling per folder from the disk (on-disk case, long names, upper-case
// drive letter). So two paths are the same folder exactly when they are equal
// strings; the core does not fold case. Only the separator is normalised, so a
// prefix test reads the same for / and for \.

func pathKey(p CleanPath) string {
	return strings.TrimRight(strings.ReplaceAll(string(p), `\`, "/"), "/")
}

func samePath(a, b CleanPath) bool { return pathKey(a) == pathKey(b) }

// within reports whether child equals parent or lies inside it.
func within(child, parent CleanPath) bool {
	c, p := pathKey(child), pathKey(parent)
	return c == p || strings.HasPrefix(c, p+"/")
}

func overlaps(a, b CleanPath) bool { return within(a, b) || within(b, a) }

func isActive(l Life) bool { _, ok := l.(Active); return ok }

func projectIDs(ws Workspace) []core.ProjectID {
	ids := make([]core.ProjectID, 0, len(ws.Projects))
	for id := range ws.Projects {
		ids = append(ids, id)
	}
	sort.Slice(ids, func(i, j int) bool { return ids[i] < ids[j] })
	return ids
}

func repoIDs(p Project) []core.RepoID {
	ids := make([]core.RepoID, 0, len(p.Repos))
	for id := range p.Repos {
		ids = append(ids, id)
	}
	sort.Slice(ids, func(i, j int) bool { return ids[i] < ids[j] })
	return ids
}

// projectAt finds the project at path: the Active one if there is one, else
// the most recently untracked one (ties by ID).
func projectAt(ws Workspace, path CleanPath) (Project, bool) {
	var best Project
	var bestAt time.Time
	found := false
	for _, id := range projectIDs(ws) {
		p := ws.Projects[id]
		if !samePath(p.Path, path) {
			continue
		}
		if isActive(p.Life) {
			return p, true
		}
		if u, ok := p.Life.(Untracked); ok && (!found || u.At.After(bestAt)) {
			best, bestAt, found = p, u.At, true
		}
	}
	return best, found
}

func activeProject(ws Workspace, id core.ProjectID) (Project, bool) {
	p, ok := ws.Projects[id]
	return p, ok && isActive(p.Life)
}

func nameTaken(ws Workspace, name string, except core.ProjectID) bool {
	for id, p := range ws.Projects {
		if id != except && isActive(p.Life) && p.Name == name {
			return true
		}
	}
	return false
}

func overlapping(ws Workspace, path CleanPath, except core.ProjectID) (core.ProjectID, bool) {
	for _, id := range projectIDs(ws) {
		p := ws.Projects[id]
		if id != except && isActive(p.Life) && overlaps(p.Path, path) {
			return id, true
		}
	}
	return "", false
}

// withProject returns a workspace with p in place of its old row. The
// projects map is copied; p carries its own copy of Repos.
func withProject(ws Workspace, p Project) Workspace {
	m := make(map[core.ProjectID]Project, len(ws.Projects)+1)
	for k, v := range ws.Projects {
		m[k] = v
	}
	m[p.ID] = p
	return Workspace{Projects: m, Busy: ws.Busy}
}

func copyRepos(p Project) Project {
	m := make(map[core.RepoID]Repo, len(p.Repos)+1)
	for k, v := range p.Repos {
		m[k] = v
	}
	p.Repos = m
	return p
}

func audit(actor Actor, action, subject string, detail map[string]string) WriteAudit {
	return WriteAudit{Actor: actor, Action: action, Subject: subject, Detail: detail}
}

func finish(cmds []Command, changed []core.RepoID) []Command {
	cmds = append(cmds, PublishReposChanged{})
	if len(changed) > 0 {
		cmds = append(cmds, RefreshStatus{RepoIDs: changed})
	}
	return cmds
}

// picked is the result of applying picks to one project.
type picked struct {
	proj            Project
	rows, audits    []Command
	added, restored []core.RepoID
	invalid         string
}

func (r picked) changed() []core.RepoID {
	return append(append([]core.RepoID{}, r.added...), r.restored...)
}

// applyPicks adds or restores the picked repos of proj. Its commands carry
// proj.Version as the expected version.
func applyPicks(ws Workspace, proj Project, picks []Pick, actor Actor, now time.Time) picked {
	res := picked{proj: copyRepos(proj)}
	used := map[core.RepoID]bool{}
	for _, p := range ws.Projects {
		for id := range p.Repos {
			used[id] = true
		}
	}
	names := map[string]bool{}
	byRel := map[RelPath]core.RepoID{}
	for id, r := range proj.Repos {
		names[r.Name] = true
		byRel[r.RelPath] = id
	}
	seen := map[RelPath]bool{}
	for _, pk := range picks {
		if _, err := NewRelPath(string(pk.RelPath)); err != nil {
			res.invalid = err.Error()
			return res
		}
		if err := ValidName(pk.Name); err != nil {
			res.invalid = err.Error()
			return res
		}
		if seen[pk.RelPath] {
			continue
		}
		seen[pk.RelPath] = true
		id, exists := byRel[pk.RelPath]
		switch {
		case !exists:
			if pk.NewRepoID == "" || used[pk.NewRepoID] {
				res.invalid = fmt.Sprintf("the repo ID %q is empty or already in use", pk.NewRepoID)
				return res
			}
			used[pk.NewRepoID] = true
			name := uniqueName(names, pk.Name, pk.RelPath)
			names[name] = true
			r := Repo{ID: pk.NewRepoID, Name: name, RelPath: pk.RelPath, Life: Active{Since: now}}
			res.proj.Repos[r.ID] = r
			res.rows = append(res.rows, InsertRepo{ProjectID: proj.ID, Repo: r, ExpectVersion: proj.Version})
			res.audits = append(res.audits, audit(actor, "repo.track", "repo:"+string(r.ID),
				map[string]string{"name": r.Name, "relPath": string(r.RelPath), "project": string(proj.ID)}))
			res.added = append(res.added, r.ID)
		case !isActive(proj.Repos[id].Life):
			r := proj.Repos[id]
			r.Life, r.Trusted = Active{Since: now}, false
			res.proj.Repos[id] = r
			res.rows = append(res.rows, RestoreRepo{ProjectID: proj.ID, RepoID: id, Since: now, ExpectVersion: proj.Version})
			res.audits = append(res.audits, audit(actor, "repo.restore", "repo:"+string(id),
				map[string]string{"name": r.Name, "relPath": string(r.RelPath), "project": string(proj.ID)}))
			res.restored = append(res.restored, id)
		}
	}
	return res
}

// uniqueName keeps the pick's name unless the project already has it; then
// the RelPath is used, with a counter if even that is taken.
func uniqueName(names map[string]bool, name string, rel RelPath) string {
	if !names[name] {
		return name
	}
	if !names[string(rel)] {
		return string(rel)
	}
	for i := 2; ; i++ {
		n := fmt.Sprintf("%s (%d)", rel, i)
		if !names[n] {
			return n
		}
	}
}

func trackProject(ws Workspace, in TrackProject) (Workspace, []Command, Outcome) {
	if len(in.Repos) == 0 {
		return ws, nil, NothingPicked{}
	}
	existing, exists := projectAt(ws, in.Path)
	if exists && isActive(existing.Life) {
		res := applyPicks(ws, existing, in.Repos, in.Actor, in.Now)
		if res.invalid != "" {
			return ws, nil, InvalidInput{Reason: res.invalid}
		}
		if len(res.rows) == 0 {
			return ws, nil, AlreadyTracked{ProjectID: existing.ID}
		}
		return finishPicks(ws, existing, res, nil, nil)
	}
	if err := ValidName(in.Name); err != nil {
		return ws, nil, InvalidInput{Reason: err.Error()}
	}
	if nameTaken(ws, in.Name, "") {
		return ws, nil, NameTaken{Name: in.Name}
	}
	if other, bad := overlapping(ws, in.Path, ""); bad {
		return ws, nil, Overlaps{Other: other}
	}
	if exists {
		// An Untracked project at this path comes back with its history.
		res := applyPicks(ws, existing, in.Repos, in.Actor, in.Now)
		if res.invalid != "" {
			return ws, nil, InvalidInput{Reason: res.invalid}
		}
		rows := []Command{RestoreProject{ProjectID: existing.ID, Name: in.Name, Since: in.Now, ExpectVersion: existing.Version}}
		audits := []Command{audit(in.Actor, "project.restore", "project:"+string(existing.ID),
			map[string]string{"name": in.Name, "path": string(in.Path)})}
		res.proj.Name, res.proj.Life = in.Name, Active{Since: in.Now}
		return finishPicks(ws, existing, res, rows, audits)
	}
	if in.NewProjectID == "" {
		return ws, nil, InvalidInput{Reason: "the project ID is empty"}
	}
	if _, taken := ws.Projects[in.NewProjectID]; taken {
		return ws, nil, InvalidInput{Reason: fmt.Sprintf("the project ID %q is already in use", in.NewProjectID)}
	}
	proj := Project{ID: in.NewProjectID, Name: in.Name, Path: in.Path, Life: Active{Since: in.Now}, Repos: map[core.RepoID]Repo{}}
	res := applyPicks(ws, proj, in.Repos, in.Actor, in.Now)
	if res.invalid != "" {
		return ws, nil, InvalidInput{Reason: res.invalid}
	}
	cmds := []Command{InsertProject{Project: Project{ID: proj.ID, Name: proj.Name, Path: proj.Path, Life: proj.Life}}}
	cmds = append(cmds, res.rows...)
	cmds = append(cmds, audit(in.Actor, "project.track", "project:"+string(proj.ID),
		map[string]string{"name": proj.Name, "path": string(proj.Path)}))
	cmds = append(cmds, res.audits...)
	cmds = finish(cmds, res.changed())
	return withProject(ws, res.proj), cmds, Tracked{ProjectID: proj.ID, Added: res.added, Restored: res.restored, NewProject: true}
}

// finishPicks assembles the commands for picks applied to an existing
// project (head rows and audits come first) and bumps its version.
func finishPicks(ws Workspace, old Project, res picked, headRows, headAudits []Command) (Workspace, []Command, Outcome) {
	var cmds []Command
	cmds = append(cmds, headRows...)
	cmds = append(cmds, res.rows...)
	cmds = append(cmds, headAudits...)
	cmds = append(cmds, res.audits...)
	cmds = finish(cmds, res.changed())
	p := res.proj
	p.Version = old.Version + 1
	return withProject(ws, p), cmds, Tracked{ProjectID: old.ID, Added: res.added, Restored: res.restored}
}

func trackRepos(ws Workspace, in TrackRepos) (Workspace, []Command, Outcome) {
	proj, ok := activeProject(ws, in.ProjectID)
	if !ok {
		return ws, nil, NotFound{}
	}
	if len(in.Repos) == 0 {
		return ws, nil, NothingPicked{}
	}
	res := applyPicks(ws, proj, in.Repos, in.Actor, in.Now)
	if res.invalid != "" {
		return ws, nil, InvalidInput{Reason: res.invalid}
	}
	if len(res.rows) == 0 {
		return ws, nil, AlreadyTracked{ProjectID: proj.ID}
	}
	return finishPicks(ws, proj, res, nil, nil)
}

func untrackRepo(ws Workspace, in UntrackRepo) (Workspace, []Command, Outcome) {
	for _, pid := range projectIDs(ws) {
		proj := ws.Projects[pid]
		r, ok := proj.Repos[in.RepoID]
		if !ok {
			continue
		}
		if !isActive(proj.Life) {
			return ws, nil, NotFound{}
		}
		if !isActive(r.Life) {
			return ws, nil, UntrackDone{Project: pid}
		}
		if why, busy := ws.Busy[in.RepoID]; busy {
			return ws, nil, RepoBusy{RepoID: in.RepoID, Reason: why}
		}
		p := copyRepos(proj)
		r.Life = Untracked{At: in.Now}
		p.Repos[r.ID] = r
		p.Version = proj.Version + 1
		cmds := []Command{
			MarkRepoUntracked{ProjectID: pid, RepoID: r.ID, At: in.Now, ExpectVersion: proj.Version},
			audit(in.Actor, "repo.untrack", "repo:"+string(r.ID), map[string]string{"name": r.Name, "project": string(pid)}),
			PublishReposChanged{},
		}
		return withProject(ws, p), cmds, UntrackDone{Project: pid, Repos: []core.RepoID{r.ID}}
	}
	return ws, nil, NotFound{}
}

func untrackProject(ws Workspace, in UntrackProject) (Workspace, []Command, Outcome) {
	proj, ok := activeProject(ws, in.ProjectID)
	if !ok {
		return ws, nil, NotFound{}
	}
	var active []core.RepoID
	for _, id := range repoIDs(proj) {
		if !isActive(proj.Repos[id].Life) {
			continue
		}
		if why, busy := ws.Busy[id]; busy {
			return ws, nil, RepoBusy{RepoID: id, Reason: why}
		}
		active = append(active, id)
	}
	p := copyRepos(proj)
	p.Life = Untracked{At: in.Now}
	p.Version = proj.Version + 1
	cmds := []Command{MarkProjectUntracked{ProjectID: proj.ID, At: in.Now, ExpectVersion: proj.Version}}
	for _, id := range active {
		r := p.Repos[id]
		r.Life = Untracked{At: in.Now}
		p.Repos[id] = r
		cmds = append(cmds, MarkRepoUntracked{ProjectID: proj.ID, RepoID: id, At: in.Now, ExpectVersion: proj.Version})
	}
	cmds = append(cmds, audit(in.Actor, "project.untrack", "project:"+string(proj.ID), map[string]string{"name": proj.Name}))
	for _, id := range active {
		cmds = append(cmds, audit(in.Actor, "repo.untrack", "repo:"+string(id),
			map[string]string{"name": p.Repos[id].Name, "project": string(proj.ID)}))
	}
	cmds = append(cmds, PublishReposChanged{})
	return withProject(ws, p), cmds, UntrackDone{Project: proj.ID, Repos: active}
}

func renameProject(ws Workspace, in RenameProject) (Workspace, []Command, Outcome) {
	proj, ok := activeProject(ws, in.ProjectID)
	if !ok {
		return ws, nil, NotFound{}
	}
	if err := ValidName(in.Name); err != nil {
		return ws, nil, InvalidInput{Reason: err.Error()}
	}
	if proj.Name == in.Name {
		return ws, nil, Renamed{ProjectID: proj.ID}
	}
	if nameTaken(ws, in.Name, proj.ID) {
		return ws, nil, NameTaken{Name: in.Name}
	}
	old := proj.Name
	cmds := []Command{
		UpdateProject{ProjectID: proj.ID, Name: in.Name, ExpectVersion: proj.Version},
		audit(in.Actor, "project.rename", "project:"+string(proj.ID), map[string]string{"from": old, "to": in.Name}),
		PublishReposChanged{},
	}
	proj = copyRepos(proj)
	proj.Name = in.Name
	proj.Version++
	return withProject(ws, proj), cmds, Renamed{ProjectID: proj.ID}
}
