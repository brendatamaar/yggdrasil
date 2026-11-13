package tasks

import (
	"reflect"
	"testing"
	"time"

	"github.com/brendatamaar/yggdrasil/internal/core"
)

func TestParseSubmissionNewAndStoredScope(t *testing.T) {
	raw := RawMessage{Prompt: " hello ", Mode: "ask", Scope: "repo", Agent: "mock", Repos: []string{"rep_one"}, Title: " x\ny "}
	p, pm, err := ParseSubmission("prj_one", raw, nil)
	if err != nil || p != "prj_one" || pm.Prompt != "hello" || pm.Title != "x y" {
		t.Fatalf("%s %+v %v", p, pm, err)
	}
	p2, pm2, err := ParseSubmission(p, FormatMessage(pm), nil)
	if err != nil || p != p2 || !reflect.DeepEqual(pm, pm2) {
		t.Fatal("new submission round trip")
	}
	stored := core.Task{ProjectID: "prj_stored", Agent: "mock", Model: "model", Scope: core.ScopeProject, RepoIDs: []core.RepoID{"rep_two", "rep_one"}}
	original := stored
	raw.Scope = "invalid"
	raw.Agent = "--bad"
	raw.Model = "--bad"
	raw.Repos = []string{"invalid"}
	p, pm, err = ParseSubmission("ignored", raw, &stored)
	if err != nil || p != stored.ProjectID || pm.Agent != stored.Agent || pm.Model != stored.Model || pm.Scope != stored.Scope || !reflect.DeepEqual(pm.Repos, stored.RepoIDs) {
		t.Fatalf("%s %+v %v", p, pm, err)
	}
	if !reflect.DeepEqual(stored, original) {
		t.Fatal("mutated stored task")
	}
	// Only Ask receives the project follow-up exception; Code cannot narrow a
	// project task silently and a project Ask still needs a stored readable repo.
	raw.Mode = "code"
	if _, _, err = ParseSubmission("ignored", raw, &stored); err == nil {
		t.Fatal("project Code accepted")
	}
	raw.Mode = "ask"
	stored.RepoIDs = nil
	if _, _, err = ParseSubmission("ignored", raw, &stored); err == nil {
		t.Fatal("empty project Ask accepted")
	}
	if _, _, err = ParseSubmission("ignored", RawMessage{Prompt: "x", Mode: "ask", Scope: "project", Agent: "mock"}, nil); err == nil {
		t.Fatal("new project Ask accepted")
	}
}

func TestSubmissionRejectsDuplicateRepos(t *testing.T) {
	raw := RawMessage{Prompt: "x", Mode: "ask", Scope: "repo", Agent: "mock", Repos: []string{"rep_one", "rep_one"}}
	_, pm, err := ParseSubmission("prj_one", raw, nil)
	if err == nil {
		t.Fatalf("%+v %v", pm, err)
	}
}

func TestSubmissionRefusalTables(t *testing.T) {
	for _, mode := range []core.TaskMode{core.ModeAsk, core.ModeCode, core.ModeCurrentBranch, core.ModeProject} {
		for _, enabled := range []bool{false, true} {
			refusal := WritingModeRefusal(mode, enabled)
			if (refusal == nil) != (enabled || mode == core.ModeAsk) {
				t.Fatalf("mode=%s enabled=%v", mode, enabled)
			}
			if refusal != nil && (refusal.Reason != "write_modes_off" || refusal.Message == "") {
				t.Fatalf("%+v", refusal)
			}
		}
	}
	for _, tc := range []struct {
		scope  core.TaskScope
		repos  []core.Repo
		reason string
	}{
		{core.ScopeProject, nil, "no_repos"},
		{core.ScopeProject, []core.Repo{{ID: "rep_one", ProjectID: "prj_one"}}, ""},
		{core.ScopeRepo, []core.Repo{{ID: "rep_one", ProjectID: "prj_one"}}, ""},
		{core.ScopeRepo, []core.Repo{{ID: "rep_one", ProjectID: "prj_other"}}, "repo_not_in_project"},
	} {
		r := ScopeRefusal("prj_one", tc.scope, tc.repos)
		if tc.reason == "" {
			if r != nil {
				t.Fatalf("%+v", r)
			}
		} else if r == nil || r.Reason != tc.reason || r.Message == "" {
			t.Fatalf("got %+v want %s", r, tc.reason)
		}
	}
}

func TestPropSubmissionPreservesStoredIdentity(t *testing.T) {
	// Stateless decisions: exhaust the small domain of scope, mode, repo order
	// and caller identity; each result must retain the shell-loaded identity.
	for _, scope := range []core.TaskScope{core.ScopeRepo, core.ScopeProject} {
		for _, mode := range []core.TaskMode{core.ModeAsk, core.ModeCode, core.ModeCurrentBranch, core.ModeProject} {
			for _, ids := range [][]core.RepoID{{"rep_one"}, {"rep_one", "rep_two"}, {"rep_two", "rep_one"}} {
				stored := core.Task{ProjectID: "prj_one", Agent: "mock", Model: "model", Scope: scope, RepoIDs: ids}
				for _, caller := range []string{"", "ignored", "--invalid"} {
					raw := RawMessage{Prompt: "x", Mode: string(mode), Scope: caller, Agent: caller, Model: caller, Repos: []string{caller}}
					p, pm, err := ParseSubmission(core.ProjectID(caller), raw, &stored)
					p2, pm2, err2 := ParseSubmission(core.ProjectID(caller), raw, &stored)
					if (err == nil) != (err2 == nil) || p != p2 || !reflect.DeepEqual(pm, pm2) {
						t.Fatal("not deterministic")
					}
					if err == nil && (p != stored.ProjectID || pm.Scope != scope || pm.Agent != stored.Agent || pm.Model != stored.Model || !reflect.DeepEqual(pm.Repos, ids)) {
						t.Fatalf("identity widened: %+v", pm)
					}
				}
			}
		}
	}
}

func TestNewSubmissionTaskDefaults(t *testing.T) {
	now := time.Date(2026, 11, 13, 0, 0, 0, 0, time.UTC)
	repos := []core.Repo{{ID: "rep_two"}, {ID: "rep_one"}}
	for _, scope := range []core.TaskScope{core.ScopeRepo, core.ScopeProject} {
		for _, tc := range []struct {
			id          core.TaskID
			title, want string
		}{{"tsk_12345678", "", "Task 345678"}, {"short", "", "Task short"}, {"tsk_12345678", "Chosen", "Chosen"}} {
			pm := ParsedMessage{Scope: scope, Mode: core.ModeAsk, Agent: "mock", Prompt: "hello", Title: tc.title}
			task := NewSubmissionTask(tc.id, "prj_one", pm, repos, now)
			wantIDs := []core.RepoID{"rep_two"}
			if scope == core.ScopeProject {
				wantIDs = append(wantIDs, "rep_one")
			}
			if task.ID != tc.id || task.ProjectID != "prj_one" || task.Title != tc.want || task.Status != core.TaskDraft || task.Prompt != pm.Prompt || task.Mode != pm.Mode || task.Agent != pm.Agent || task.Scope != scope || !task.CreatedAt.Equal(now) || !task.UpdatedAt.Equal(now) || !reflect.DeepEqual(task.RepoIDs, wantIDs) {
				t.Fatalf("%+v", task)
			}
		}
	}
}

func FuzzParseSubmission(f *testing.F) {
	f.Add("hello", "ask", "repo", "mock", "model", "rep_one")
	f.Add("hello", "project", "project", "mock", "", "")
	f.Fuzz(func(t *testing.T, prompt, mode, scope, agent, model, repo string) {
		raw := RawMessage{Prompt: prompt, Mode: mode, Scope: scope, Agent: agent, Model: model, Repos: []string{repo}}
		_, pm, err := ParseSubmission("prj_one", raw, nil)
		if err == nil {
			if pm.Mode.Validate() != nil || pm.Scope.Validate() != nil || pm.Prompt == "" || !ValidModelName(pm.Agent) || (pm.Model != "" && !ValidModelName(pm.Model)) {
				t.Fatalf("invalid accepted: %+v", pm)
			}
			_, again, err := ParseSubmission("prj_one", FormatMessage(pm), nil)
			if err != nil || !reflect.DeepEqual(pm, again) {
				t.Fatal("round trip")
			}
		}
		stored := core.Task{ProjectID: "prj_one", Scope: core.ScopeProject, Agent: "mock", RepoIDs: []core.RepoID{"rep_one", "rep_two"}}
		_, pm, err = ParseSubmission("ignored", raw, &stored)
		if err == nil && (pm.Scope != core.ScopeProject || pm.Agent != "mock" || !reflect.DeepEqual(pm.Repos, stored.RepoIDs)) {
			t.Fatal("stored scope changed")
		}
	})
}
