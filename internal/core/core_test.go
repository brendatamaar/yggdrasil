package core

import (
	"errors"
	"testing"
)

func TestIDValidate(t *testing.T) {
	if err := TaskID("t_1").Validate(); err != nil {
		t.Fatalf("valid id rejected: %v", err)
	}
	for _, bad := range []string{"", "a b", "a/b", "a\\b", "a\n"} {
		if err := TaskID(bad).Validate(); !errors.Is(err, ErrInvalidID) {
			t.Errorf("TaskID(%q).Validate() = %v, want ErrInvalidID", bad, err)
		}
	}
}

func TestEnumValidate(t *testing.T) {
	if err := ModeCode.Validate(); err != nil {
		t.Errorf("ModeCode: %v", err)
	}
	if err := TaskMode("yolo").Validate(); err == nil {
		t.Error("unknown mode accepted")
	}
	if err := TaskStatus("").Validate(); err == nil {
		t.Error("empty status accepted")
	}
	if err := ProposalKind("rm_rf").Validate(); err == nil {
		t.Error("unknown proposal kind accepted")
	}
}

func TestTaskStatusTerminal(t *testing.T) {
	for _, s := range []TaskStatus{TaskDone, TaskFailed, TaskCancelled} {
		if !s.Terminal() {
			t.Errorf("%s should be terminal", s)
		}
	}
	for _, s := range []TaskStatus{TaskDraft, TaskQueued, TaskRunning, TaskNeedsYou, TaskReview} {
		if s.Terminal() {
			t.Errorf("%s should not be terminal", s)
		}
	}
}

func TestTaskValidate(t *testing.T) {
	task := Task{
		ID: "t_1", Scope: ScopeRepo, Mode: ModeAsk, Status: TaskDraft,
		RepoIDs: []RepoID{"r_1"},
	}
	if err := task.Validate(); err != nil {
		t.Fatalf("valid task rejected: %v", err)
	}
	self := task
	id := task.ID
	self.ParentTaskID = &id
	if err := self.Validate(); err == nil {
		t.Error("task that is its own parent accepted")
	}
	none := task
	none.RepoIDs = nil
	if err := none.Validate(); err == nil {
		t.Error("task without repos accepted")
	}
}

// validator is what every enum and ID type offers.
type validator interface{ Validate() error }

func TestEnumsAcceptEveryConstantAndRejectOthers(t *testing.T) {
	groups := map[string]struct {
		good []validator
		bad  []validator
	}{
		"TaskMode":        {[]validator{ModeAsk, ModeCode, ModeCurrentBranch, ModeProject}, []validator{TaskMode(""), TaskMode("yolo")}},
		"TaskScope":       {[]validator{ScopeRepo, ScopeProject}, []validator{TaskScope(""), TaskScope("all")}},
		"TaskStatus":      {[]validator{TaskDraft, TaskQueued, TaskRunning, TaskNeedsYou, TaskReview, TaskDone, TaskFailed, TaskCancelled}, []validator{TaskStatus(""), TaskStatus("paused")}},
		"WorktreeStatus":  {[]validator{WorktreeActive, WorktreeMerged, WorktreeRemoved}, []validator{WorktreeStatus(""), WorktreeStatus("gone")}},
		"ProposalKind":    {[]validator{ProposalCommit, ProposalPush, ProposalMergeRequest, ProposalInstall, ProposalPublish, ProposalAgentFile, ProposalDeleteFiles, ProposalSubtasks}, []validator{ProposalKind(""), ProposalKind("rm_rf")}},
		"ProposalStatus":  {[]validator{ProposalPending, ProposalConfirmed, ProposalRejected, ProposalExecuted, ProposalFailed}, []validator{ProposalStatus(""), ProposalStatus("maybe")}},
		"ChangeSetStatus": {[]validator{ChangeSetOpen, ChangeSetShipped, ChangeSetClosed}, []validator{ChangeSetStatus(""), ChangeSetStatus("lost")}},
		"BuildStatus":     {[]validator{BuildQueued, BuildRunning, BuildSucceeded, BuildFailed, BuildCancelled, BuildInterrupted}, []validator{BuildStatus(""), BuildStatus("stuck"), BuildStatus("skipped")}},
		"StepStatus":      {[]validator{StepQueued, StepRunning, StepSucceeded, StepFailed, StepCancelled, StepSkipped, StepInterrupted}, []validator{StepStatus(""), StepStatus("stuck")}},
		"StepOutcome":     {[]validator{OutcomeSucceeded, OutcomeFailed, OutcomeStartFailed, OutcomeCancelled, OutcomeInterrupted}, []validator{StepOutcome(""), StepOutcome("bogus")}},
	}
	for name, g := range groups {
		for _, v := range g.good {
			if err := v.Validate(); err != nil {
				t.Errorf("%s %q rejected: %v", name, v, err)
			}
		}
		for _, v := range g.bad {
			if err := v.Validate(); err == nil {
				t.Errorf("%s %q accepted", name, v)
			}
		}
	}
}

func TestAllIDTypesValidate(t *testing.T) {
	for name, mk := range map[string]func(string) validator{
		"ProjectID": func(s string) validator { return ProjectID(s) }, "RepoID": func(s string) validator { return RepoID(s) },
		"WorktreeID": func(s string) validator { return WorktreeID(s) }, "TaskID": func(s string) validator { return TaskID(s) },
		"ChangeSetID": func(s string) validator { return ChangeSetID(s) }, "ProposalID": func(s string) validator { return ProposalID(s) },
		"BuildID": func(s string) validator { return BuildID(s) }, "AuditID": func(s string) validator { return AuditID(s) },
	} {
		if err := mk("x_1").Validate(); err != nil {
			t.Errorf("%s: valid id rejected: %v", name, err)
		}
		for _, bad := range []string{"", "a b", "a/b", `a\b`, "a\n"} {
			if err := mk(bad).Validate(); !errors.Is(err, ErrInvalidID) {
				t.Errorf("%s(%q) = %v, want ErrInvalidID", name, bad, err)
			}
		}
	}
}

func TestProjectValidate(t *testing.T) {
	ok := Project{ID: "p_1", Name: "acme-shop"}
	if err := ok.Validate(); err != nil {
		t.Fatalf("valid project rejected: %v", err)
	}
	for name, mutate := range map[string]func(*Project){
		"empty id":        func(p *Project) { p.ID = "" },
		"id with a space": func(p *Project) { p.ID = "p 1" },
		"empty name":      func(p *Project) { p.Name = "" },
		"blank name":      func(p *Project) { p.Name = " \t " },
	} {
		p := ok
		mutate(&p)
		if err := p.Validate(); err == nil {
			t.Errorf("%s: accepted", name)
		}
	}
}

func TestRepoValidate(t *testing.T) {
	ok := Repo{ID: "r_1", ProjectID: "p_1", Name: "shop-api", Path: `C:\work\shop-api`}
	for _, path := range []string{`C:\work\shop-api`, `c:/work/shop-api`, "/home/me/shop-api", `\\?\C:\work\shop-api`, `\\server\share\shop-api`} {
		r := ok
		r.Path = path
		if err := r.Validate(); err != nil {
			t.Errorf("path %q rejected: %v", path, err)
		}
	}
	for name, mutate := range map[string]func(*Repo){
		"empty id":          func(r *Repo) { r.ID = "" },
		"empty project id":  func(r *Repo) { r.ProjectID = "" },
		"bad project id":    func(r *Repo) { r.ProjectID = "p/1" },
		"empty name":        func(r *Repo) { r.Name = "  " },
		"empty path":        func(r *Repo) { r.Path = "" },
		"blank path":        func(r *Repo) { r.Path = "   " },
		"relative path":     func(r *Repo) { r.Path = `work\shop-api` },
		"dot path":          func(r *Repo) { r.Path = "." },
		"drive without dir": func(r *Repo) { r.Path = "C:" },
		"drive relative":    func(r *Repo) { r.Path = `C:work` },
		"NUL in path":       func(r *Repo) { r.Path = "/x\x00y" },
	} {
		r := ok
		mutate(&r)
		if err := r.Validate(); err == nil {
			t.Errorf("%s: accepted", name)
		}
	}
}
