package tasks

import (
	"context"
	"errors"
	"reflect"
	"testing"

	"github.com/brendatamaar/yggdrasil/internal/agents"
	"github.com/brendatamaar/yggdrasil/internal/core"
	coretasks "github.com/brendatamaar/yggdrasil/internal/core/tasks"
	"github.com/brendatamaar/yggdrasil/internal/db"
	"github.com/brendatamaar/yggdrasil/internal/db/dbtest"
)

func TestSubmissionScopeChoices(t *testing.T) {
	h := newHarness(t, []string{"shop-api", "shop-web"})
	h.start()
	ctx := context.Background()
	other := dbtest.Project(t, h.store, core.Project{Name: "other", Path: t.TempDir()})
	cases := []struct {
		name     string
		project  core.ProjectID
		pm       coretasks.ParsedMessage
		existing *db.TaskState
		want     []core.Repo
		reason   string
	}{
		{name: "new repo", project: h.project.ID, pm: coretasks.ParsedMessage{Scope: core.ScopeRepo, Repos: []core.RepoID{h.repos[1].ID}}, want: []core.Repo{h.repos[1]}},
		{name: "new project", project: h.project.ID, pm: coretasks.ParsedMessage{Scope: core.ScopeProject}, want: h.repos},
		{name: "empty project", project: other.ID, pm: coretasks.ParsedMessage{Scope: core.ScopeProject}, reason: "no_repos"},
		{name: "wrong project", project: other.ID, pm: coretasks.ParsedMessage{Scope: core.ScopeRepo, Repos: []core.RepoID{h.repos[0].ID}}, reason: "repo_not_in_project"},
		{name: "resumed uses stored order", project: h.project.ID, existing: &db.TaskState{Row: core.Task{RepoIDs: []core.RepoID{h.repos[1].ID, h.repos[0].ID}}}, want: []core.Repo{h.repos[1], h.repos[0]}},
		{name: "resumed missing repo", project: h.project.ID, existing: &db.TaskState{Row: core.Task{RepoIDs: []core.RepoID{"repo_missing"}}}, reason: "repo_untracked"},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			got, err := h.svc.scopeRepos(ctx, tc.project, tc.pm, tc.existing)
			if tc.reason != "" {
				var r *RejectedError
				if !errors.As(err, &r) || r.Reason != tc.reason {
					t.Fatalf("got %v", err)
				}
				return
			}
			if err != nil {
				t.Fatal(err)
			}
			ids := func(rs []core.Repo) map[core.RepoID]bool {
				m := map[core.RepoID]bool{}
				for _, r := range rs {
					m[r.ID] = true
				}
				return m
			}
			if !reflect.DeepEqual(ids(got), ids(tc.want)) {
				t.Fatalf("repos = %v", got)
			}
			if tc.existing != nil && !reflect.DeepEqual(got, tc.want) {
				t.Fatal("stored order changed")
			}
		})
	}
}

func TestSubmissionFollowUpUsesStoredIdentityAndProjectAskScope(t *testing.T) {
	for _, project := range []bool{false, true} {
		t.Run(map[bool]string{false: "repo", true: "project"}[project], func(t *testing.T) {
			h := newHarness(t, []string{"shop-api", "shop-web"})
			h.adapter.caps.Enforcement = agents.EnforcementHook
			h.start()
			req := h.msg("ask", "first", 0)
			status := core.TaskDone
			if project {
				req = h.projectMsg("first")
				status = core.TaskReview
			}
			first := h.send(req)
			h.waitStatus(first.Task.ID, status)
			stored := h.state(first.Task.ID).Row
			second := h.send(MessageRequest{TaskID: first.Task.ID, ProjectID: "invalid", Prompt: " second ", Mode: "ask", Scope: "invalid", Repos: []string{"invalid"}, Agent: "unavailable", Model: "--invalid", Title: "replacement"})
			if second.Task.ProjectID != first.Task.ProjectID || second.Task.Agent != first.Task.Agent || second.Task.Model != first.Task.Model || second.Task.Scope != first.Task.Scope || second.Task.Title != first.Task.Title || !reflect.DeepEqual(second.Task.RepoIDs, stored.RepoIDs) {
				t.Fatalf("identity changed: %+v", second.Task)
			}
			h.waitStatus(second.Task.ID, core.TaskDone)
			starts := h.adapter.Starts()
			if len(starts) != 2 || starts[1].Prompt != "second" || starts[1].ResumeSessionID == "" || len(starts[1].Policy.WriteRoots) != 0 {
				t.Fatalf("starts: %+v", starts)
			}
		})
	}
}

func TestSubmissionWriteRefusalPrecedesProjectLookup(t *testing.T) {
	h := newHarness(t, []string{"shop-api"})
	h.tune = func(c *Config) { c.WriteModes = false }
	h.start()
	req := h.msg("code", "edit", 0)
	req.ProjectID = "missing"
	_, err := h.svc.Message(context.Background(), req)
	var r *RejectedError
	if !errors.As(err, &r) || r.Reason != "write_modes_off" || r.Message != "Modes that edit files are switched off in this Yggdrasil setup. Ask a question instead, or turn on write modes in the setup and restart." {
		t.Fatalf("got %v", err)
	}
	req.Prompt = " "
	_, err = h.svc.Message(context.Background(), req)
	var u *coretasks.UserError
	if !errors.As(err, &u) {
		t.Fatalf("parse should precede write refusal: %v", err)
	}
}

func TestSubmissionNewTaskDefaults(t *testing.T) {
	h := newHarness(t, []string{"shop-api"})
	h.start()
	req := h.msg("ask", "  hello  ", 0)
	first := h.send(req)
	id := string(first.Task.ID)
	if first.Task.Title != "Task "+id[len(id)-6:] || first.Task.Prompt != "hello" || len(first.Task.RepoIDs) != 1 || first.Task.RepoIDs[0] != h.repos[0].ID {
		t.Fatalf("task = %+v", first.Task)
	}
	req.Title = "  Ship\ncarefully "
	second := h.send(req)
	if second.Task.Title != "Ship carefully" {
		t.Fatalf("title %q", second.Task.Title)
	}
}
