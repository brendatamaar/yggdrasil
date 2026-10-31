package mcp

import (
	"math/rand"
	"slices"
	"testing"

	"github.com/brendatamaar/yggdrasil/internal/core"
)

var allRepos = []core.RepoID{"r1", "r2", "r3", "r4", "r5"}

func randCtx(r *rand.Rand) Context {
	modes := []core.TaskMode{core.ModeAsk, core.ModeCode, core.ModeCurrentBranch, core.ModeProject}
	c := Context{Task: "t1", Live: r.Intn(8) != 0, Mode: modes[r.Intn(len(modes))], HasWorktree: map[core.RepoID]bool{}}
	for _, id := range allRepos[:4] { // r5 is never tracked in the project
		if r.Intn(3) != 0 {
			c.Project = append(c.Project, id)
			if r.Intn(2) == 0 {
				c.Scope = append(c.Scope, id)
			}
		}
		if r.Intn(2) == 0 {
			c.HasWorktree[id] = true
		}
	}
	return c
}

// M1: a tool is offered iff Authorize (with no repo arguments) allows it.
func TestPropOfferedMatchesAuthorize(t *testing.T) {
	for seed := int64(1); seed <= 500; seed++ {
		c := randCtx(rand.New(rand.NewSource(seed)))
		off := Offered(c)
		for _, tool := range AllTools() {
			d := Authorize(c, tool)
			if slices.Contains(off, tool) != d.Allowed {
				t.Fatalf("seed %d tool %s: offered=%v decision=%+v ctx=%+v", seed, tool, slices.Contains(off, tool), d, c)
			}
			if d.Allowed != (d.Reason == DenyNone) {
				t.Fatalf("decision %+v inconsistent", d)
			}
		}
		if !c.Live && len(off) != 0 {
			t.Fatalf("seed %d: dead session offered %v", seed, off)
		}
	}
}

func TestOfferedTable(t *testing.T) {
	live := Context{Task: "t", Live: true, Mode: core.ModeCode, Scope: []core.RepoID{"a"}, Project: []core.RepoID{"a", "b"}, HasWorktree: map[core.RepoID]bool{}}
	if slices.Contains(Offered(live), ToolProposeCommit) {
		t.Error("propose_commit offered without a worktree")
	}
	if d := Authorize(live, ToolProposeCommit); d.Reason != DenyNoWorktree {
		t.Errorf("want NoWorktree, got %+v", d)
	}
	for _, tool := range []Tool{ToolListRepos, ToolFetch, ToolProposeInstall, ToolProposeAgentFile, ToolProposeDelete, ToolStartBuild} {
		if !slices.Contains(Offered(live), tool) {
			t.Errorf("%s not offered", tool)
		}
	}
	wt := live
	wt.HasWorktree = map[core.RepoID]bool{"a": true}
	for _, tool := range []Tool{ToolProposeCommit, ToolProposePush, ToolProposeMR} {
		if !slices.Contains(Offered(wt), tool) {
			t.Errorf("%s not offered with a worktree", tool)
		}
	}
	// a worktree outside the scope does not count
	out := live
	out.HasWorktree = map[core.RepoID]bool{"b": true}
	if slices.Contains(Offered(out), ToolProposeCommit) {
		t.Error("worktree of an out-of-scope repo enables propose_commit")
	}
	cb := live
	cb.Mode = core.ModeCurrentBranch
	if !slices.Contains(Offered(cb), ToolProposeCommit) {
		t.Error("propose_commit not offered in current_branch mode")
	}
	dead := live
	dead.Live = false
	if d := Authorize(dead, ToolListRepos); d.Reason != DenyNoSession {
		t.Errorf("want NoSession, got %+v", d)
	}
	if d := Authorize(live, Tool("nope")); d.Reason != DenyUnknown {
		t.Errorf("want Unknown, got %+v", d)
	}
	bad := live
	bad.Mode = "yolo"
	if len(Offered(bad)) != 0 {
		t.Error("invalid mode offered tools")
	}
}

// M2: with any repo argument, an allowed call names a visible repo, and for
// content and action tools one in scope.
func TestPropRepoArgsResolveInsideScope(t *testing.T) {
	for seed := int64(1); seed <= 300; seed++ {
		c := randCtx(rand.New(rand.NewSource(seed)))
		for _, tool := range AllTools() {
			for _, id := range append(slices.Clone(allRepos), "", "zz") {
				d := Authorize(c, tool, id)
				if !d.Allowed {
					continue
				}
				if !slices.Contains(c.Project, id) {
					t.Fatalf("seed %d: %s allowed for repo %q outside the visible set", seed, tool, id)
				}
				if ClassOf(tool) != ClassMetadata && !slices.Contains(c.Scope, id) {
					t.Fatalf("seed %d: %s allowed for repo %q outside the scope", seed, tool, id)
				}
			}
		}
	}
}

// M3: untracked, unknown and out-of-scope repos get the identical answer.
func TestPropNoRepoOracle(t *testing.T) {
	checked := 0
	for seed := int64(1); seed <= 300; seed++ {
		c := randCtx(rand.New(rand.NewSource(seed)))
		c.Live = true
		var outOfScope core.RepoID
		for _, id := range c.Project {
			if !slices.Contains(c.Scope, id) {
				outOfScope = id
			}
		}
		for _, tool := range AllTools() {
			if ClassOf(tool) != ClassContent && ClassOf(tool) != ClassAction {
				continue
			}
			if !slices.Contains(Offered(c), tool) {
				continue
			}
			untracked := Authorize(c, tool, "r5")
			unknown := Authorize(c, tool, "nope")
			unresolved := Authorize(c, tool, "")
			if untracked != unknown || unknown != unresolved || untracked.Allowed {
				t.Fatalf("seed %d %s: %+v %+v %+v", seed, tool, untracked, unknown, unresolved)
			}
			if outOfScope != "" {
				if got := Authorize(c, tool, outOfScope); got != untracked {
					t.Fatalf("seed %d %s: out-of-scope %+v differs from %+v", seed, tool, got, untracked)
				}
				checked++
			}
			if untracked.Reason != DenyNoSuchRepo {
				t.Fatalf("reason %q", untracked.Reason)
			}
		}
	}
	if checked == 0 {
		t.Fatal("the out-of-scope case was never exercised")
	}
}

func TestMetadataToolsNameProjectRepos(t *testing.T) {
	c := Context{Task: "t", Live: true, Mode: core.ModeCode, Scope: []core.RepoID{"a"}, Project: []core.RepoID{"a", "b"}}
	if d := Authorize(c, ToolRepoStatus, "b"); !d.Allowed {
		t.Errorf("metadata tool denied for visible repo: %+v", d)
	}
	if d := Authorize(c, ToolGetDiff, "b"); d.Allowed {
		t.Error("content tool allowed outside scope")
	}
	if d := Authorize(c, ToolRepoStatus, "zz"); d.Reason != DenyNoSuchRepo {
		t.Errorf("%+v", d)
	}
	// every repo of a multi-repo call must pass
	if d := Authorize(c, ToolStartBuild, "a", "b"); d.Allowed {
		t.Error("multi-repo call with an out-of-scope repo allowed")
	}
	if d := Authorize(c, ToolStartBuild, "a"); !d.Allowed {
		t.Errorf("%+v", d)
	}
	// propose_commit needs the worktree of the named repo
	c.HasWorktree = map[core.RepoID]bool{"a": true}
	c.Scope = []core.RepoID{"a", "b"}
	if d := Authorize(c, ToolProposeCommit, "b"); d.Reason != DenyNoWorktree {
		t.Errorf("%+v", d)
	}
	if d := Authorize(c, ToolProposeCommit, "a"); !d.Allowed {
		t.Errorf("%+v", d)
	}
}

func TestClassOfCoversEveryTool(t *testing.T) {
	for _, tool := range AllTools() {
		if ClassOf(tool) == ClassNone {
			t.Errorf("%s has no class", tool)
		}
	}
	if ClassOf("x") != ClassNone {
		t.Error("unknown tool has a class")
	}
}

// M9: the result and request caps are the documented ones.
func TestPropResultCapConstants(t *testing.T) {
	if MaxResultBytes != 64*1024 || MaxRequestBytes != 1<<20 || MaxDiffBytes != 64*1024 || MaxLogBytes != 64*1024 || MaxListLimit != 200 {
		t.Fatal("caps changed from the design (decision 5)")
	}
	if MaxDiffBytes > MaxResultBytes || MaxLogBytes > MaxResultBytes {
		t.Fatal("a tool cap exceeds the result cap")
	}
}

// Invariant 9: in Ask mode pull is never offered and never authorized.
func TestPropAskModeNeverPulls(t *testing.T) {
	for seed := int64(1); seed <= 500; seed++ {
		c := randCtx(rand.New(rand.NewSource(seed)))
		c.Mode = core.ModeAsk
		if slices.Contains(Offered(c), ToolPull) {
			t.Fatalf("seed %d: pull offered in Ask mode", seed)
		}
		for _, id := range append(slices.Clone(allRepos), "") {
			if d := Authorize(c, ToolPull, id); d.Allowed {
				t.Fatalf("seed %d: pull allowed in Ask mode for %q", seed, id)
			}
		}
		if c.Live {
			if d := Authorize(c, ToolPull); d.Reason != DenyAskMode {
				t.Fatalf("seed %d: %+v", seed, d)
			}
		}
	}
}
