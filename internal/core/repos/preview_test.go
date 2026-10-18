package repos

import (
	"reflect"
	"testing"
	"time"

	"github.com/brendatamaar/yggdrasil/internal/core"
)

var now0 = time.Date(2026, 1, 1, 0, 0, 0, 0, time.UTC)

func trackedShop(t *testing.T) Workspace {
	t.Helper()
	ws, _, out := Decide(Workspace{Projects: map[core.ProjectID]Project{}}, TrackProject{
		NewProjectID: "prj_1", Name: "shop", Path: `C:\Work\shop`, Actor: "user", Now: now0,
		Repos: []Pick{{RelPath: "api", Name: "api", NewRepoID: "rep_1"}, {RelPath: "web", Name: "web", NewRepoID: "rep_2"}},
	})
	if _, ok := out.(Tracked); !ok {
		t.Fatalf("outcome = %#v", out)
	}
	return ws
}

func TestBuildPreview(t *testing.T) {
	ws := trackedShop(t)
	scan := ScanResult{Repos: []RelPath{"web", "docs/site", "api", "api"}, Skipped: 2}
	pv := BuildPreview(`c:/work/SHOP`, scan, ws) // another spelling of the same folder
	if pv.Existing == nil || *pv.Existing != "prj_1" || pv.Skipped != 2 || pv.Truncated {
		t.Fatalf("preview = %+v", pv)
	}
	if len(pv.Candidates) != 3 {
		t.Fatalf("candidates = %+v", pv.Candidates)
	}
	got := map[RelPath]Candidate{}
	for _, c := range pv.Candidates {
		got[c.RelPath] = c
	}
	if c := got["api"]; c.Known == nil || *c.Known != "rep_1" || !c.Active {
		t.Errorf("api = %+v", c)
	}
	if c := got["docs/site"]; c.Known != nil || c.Name != "site" {
		t.Errorf("docs/site = %+v", c)
	}
	if pv.Candidates[0].RelPath != "api" {
		t.Error("candidates must be sorted")
	}
	// A folder nobody tracks has no Existing.
	if other := BuildPreview(`C:\Other`, ScanResult{Repos: []RelPath{"."}}, ws); other.Existing != nil || other.Candidates[0].Name != "Other" {
		t.Errorf("other = %+v", other)
	}
	// Untracked projects count as existing, with their rows as known.
	ws2, _, _ := Decide(ws, UntrackProject{ProjectID: "prj_1", Actor: "user", Now: now0})
	pv = BuildPreview(`C:\Work\shop`, scan, ws2)
	if pv.Existing == nil || pv.Candidates[0].Known == nil || pv.Candidates[0].Active {
		t.Errorf("untracked preview = %+v", pv)
	}
}

func TestRescan(t *testing.T) {
	ws := trackedShop(t)
	rep := Rescan(ScanResult{Repos: []RelPath{"api", "mobile"}}, ws, "prj_1")
	if len(rep.New) != 1 || rep.New[0].RelPath != "mobile" || !reflect.DeepEqual(rep.Missing, []core.RepoID{"rep_2"}) {
		t.Errorf("report = %+v", rep)
	}
	// A truncated scan proves nothing about what is missing.
	if rep := Rescan(ScanResult{Repos: []RelPath{"api"}, Truncated: true}, ws, "prj_1"); len(rep.Missing) != 0 {
		t.Errorf("Missing on a truncated scan = %v", rep.Missing)
	}
	// Unknown and untracked projects give an empty report.
	if rep := Rescan(ScanResult{Repos: []RelPath{"x"}}, ws, "prj_nope"); len(rep.New)+len(rep.Missing) != 0 {
		t.Errorf("unknown project: %+v", rep)
	}
	ws2, _, _ := Decide(ws, UntrackProject{ProjectID: "prj_1", Actor: "user", Now: now0})
	if rep := Rescan(ScanResult{Repos: []RelPath{"x"}}, ws2, "prj_1"); len(rep.New)+len(rep.Missing) != 0 {
		t.Errorf("untracked project: %+v", rep)
	}
	// An untracked row is not "new".
	ws3, _, _ := Decide(ws, UntrackRepo{RepoID: "rep_2", Actor: "user", Now: now0})
	if rep := Rescan(ScanResult{Repos: []RelPath{"api", "web"}}, ws3, "prj_1"); len(rep.New) != 0 || len(rep.Missing) != 0 {
		t.Errorf("report = %+v", rep)
	}
}

func TestDecideRules(t *testing.T) {
	ws := trackedShop(t)
	pk := func(rel, name, id string) Pick {
		return Pick{RelPath: RelPath(rel), Name: name, NewRepoID: core.RepoID(id)}
	}
	type tc struct {
		name string
		in   Input
		want Outcome
	}
	for _, c := range []tc{
		{"no picks", TrackProject{NewProjectID: "p2", Name: "x", Path: `C:\x`, Now: now0}, NothingPicked{}},
		{"name taken", TrackProject{NewProjectID: "p2", Name: "shop", Path: `C:\x`, Repos: []Pick{pk(".", "x", "r9")}, Now: now0}, NameTaken{Name: "shop"}},
		{"inside", TrackProject{NewProjectID: "p2", Name: "x", Path: `C:\Work\shop\api`, Repos: []Pick{pk(".", "x", "r9")}, Now: now0}, Overlaps{Other: "prj_1"}},
		{"containing", TrackProject{NewProjectID: "p2", Name: "x", Path: `C:\Work`, Repos: []Pick{pk(".", "x", "r9")}, Now: now0}, Overlaps{Other: "prj_1"}},
		{"bad name", TrackProject{NewProjectID: "p2", Name: " ", Path: `C:\x`, Repos: []Pick{pk(".", "x", "r9")}, Now: now0}, InvalidInput{Reason: "the name is empty"}},
		{"repo ID in use", TrackRepos{ProjectID: "prj_1", Repos: []Pick{pk("new", "new", "rep_1")}, Now: now0}, InvalidInput{Reason: `the repo ID "rep_1" is empty or already in use`}},
		{"unknown project", TrackRepos{ProjectID: "nope", Repos: []Pick{pk("a", "a", "r9")}, Now: now0}, NotFound{}},
		{"no repo picks", TrackRepos{ProjectID: "prj_1", Now: now0}, NothingPicked{}},
		{"rename taken", RenameProject{ProjectID: "prj_1", Name: "shop", Now: now0}, Renamed{ProjectID: "prj_1"}},
		{"untrack unknown", UntrackProject{ProjectID: "nope", Now: now0}, NotFound{}},
	} {
		if _, cmds, out := Decide(ws, c.in); !reflect.DeepEqual(out, c.want) || len(cmds) != 0 {
			t.Errorf("%s: outcome %#v with %d commands, want %#v and none", c.name, out, len(cmds), c.want)
		}
	}
	// Name collisions inside a project fall back to the RelPath.
	ws2, _, out := Decide(ws, TrackRepos{ProjectID: "prj_1", Now: now0, Repos: []Pick{pk("libs/api", "api", "rep_3"), pk("tools/api", "api", "rep_4")}})
	if _, ok := out.(Tracked); !ok {
		t.Fatalf("outcome = %#v", out)
	}
	if n3, n4 := ws2.Projects["prj_1"].Repos["rep_3"].Name, ws2.Projects["prj_1"].Repos["rep_4"].Name; n3 != "libs/api" || n4 != "tools/api" {
		t.Errorf("names = %q, %q", n3, n4)
	}
	// Busy blocks untracking.
	busy := ws
	busy.Busy = map[core.RepoID]BusyReason{"rep_2": BusyBuildRunning}
	if _, cmds, out := Decide(busy, UntrackProject{ProjectID: "prj_1", Now: now0}); !reflect.DeepEqual(out, RepoBusy{RepoID: "rep_2", Reason: BusyBuildRunning}) || len(cmds) != 0 {
		t.Errorf("busy project: %#v", out)
	}
	if _, _, out := Decide(busy, UntrackRepo{RepoID: "rep_1", Now: now0}); !reflect.DeepEqual(out, UntrackDone{Project: "prj_1", Repos: []core.RepoID{"rep_1"}}) {
		t.Errorf("a repo that isn't busy: %#v", out)
	}
}
