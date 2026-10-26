package tasks

import (
	"context"
	"encoding/json"
	"errors"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/brendatamaar/yggdrasil/internal/agents"
	"github.com/brendatamaar/yggdrasil/internal/core"
	corerepos "github.com/brendatamaar/yggdrasil/internal/core/repos"
	"github.com/brendatamaar/yggdrasil/internal/db"
)

// These tests cover, in the tasks package, what the API tests covered before
// the service moved here (safety review of gate 3, Must-fix 3).

// ---- invariant 4: tokens never reach the store or the hub ----

func TestEventsAreRedactedBeforeStoreAndHub(t *testing.T) {
	const token = "ghp_abcdefghijklmnopqrstuvwxyz0123456789"
	h := newHarness(t, []string{"shop-api"})
	h.adapter.events = []agents.Event{
		{Kind: agents.EventMessage, Role: agents.RoleAssistant, Text: "the key is " + token},
		{Kind: agents.EventToolCall, Tool: &agents.ToolCall{Name: "Read", Input: json.RawMessage(`{"note":"Bearer abc123def456ghi789","k":"` + token + `"}`)}},
		{Kind: agents.EventToolResult, Text: "GITHUB_TOKEN=" + token},
		{Kind: agents.EventFileEdit, File: &agents.FileEdit{Path: "a.env", Op: "modify", Diff: "+TOKEN=" + token}},
		{Kind: agents.EventCommand, Command: &agents.CommandRun{Line: "curl -H 'Authorization: Bearer abcdefgh12345678'", Output: token}},
		{Kind: agents.EventError, Error: "failed with " + token},
	}
	h.start()
	id := h.send(h.msg("ask", "look", 0)).Task.ID
	h.waitStatus(id, core.TaskDone)

	leaks := []string{token, "abc123def456ghi789", "abcdefgh12345678"}
	rows, err := h.store.ListTaskEvents(context.Background(), id, 0)
	if err != nil {
		t.Fatal(err)
	}
	stored := ""
	for _, row := range rows {
		stored += string(row.Data) + "\n"
	}
	h.hub.mu.Lock()
	payloads, _ := json.Marshal(h.hub.payloads[id])
	h.hub.mu.Unlock()
	for name, text := range map[string]string{"stored rows": stored, "hub payloads": string(payloads)} {
		for _, leak := range leaks {
			if strings.Contains(text, leak) {
				t.Errorf("%s contain %q: %s", name, leak, text)
			}
		}
		if !strings.Contains(text, "[REDACTED]") {
			t.Errorf("%s: nothing was redacted: %s", name, text)
		}
	}
	// Every field the brief names is checked on its own.
	for _, field := range []string{`"text":"the key is`, `"diff":"+TOKEN=`, `"output":"`, `"input":{`} {
		if !strings.Contains(stored, field) {
			t.Errorf("the stored events lack %s (the test no longer covers that field)", field)
		}
	}
}

// ---- invariant 6: task creation racing with untrack ----

func TestMessageRacingWithUntrackCreatesNothing(t *testing.T) {
	h := newHarness(t, []string{"shop-api"})
	ctx := context.Background()
	h.adapter.onCheck = func() {
		ws, err := h.store.GetWorkspace(ctx)
		if err != nil {
			t.Error(err)
			return
		}
		ver := ws.Projects[h.project.ID].Version
		err = h.store.Commit(ctx, func(tx *db.Tx) error {
			return tx.Workspace().MarkRepoUntracked(ctx, corerepos.MarkRepoUntracked{
				ProjectID: h.project.ID, RepoID: h.repos[0].ID, At: time.Now().UTC(), ExpectVersion: ver,
			})
		})
		if err != nil {
			t.Error(err)
		}
	}
	h.start()
	_, err := h.svc.Message(ctx, h.msg("ask", "look", 0))
	if err == nil || !errors.Is(err, db.ErrNotFound) {
		t.Fatalf("got %v, want a not-found refusal", err)
	}
	if list, _ := h.store.ListTasks(ctx, h.project.ID); len(list) != 0 {
		t.Errorf("a task was created for an untracked repo: %+v", list)
	}
	if n := len(h.adapter.Starts()); n != 0 {
		t.Errorf("%d run(s) started", n)
	}
}

// ---- the cause of a failed run is logged, not shown ----

type causeError struct {
	msg   string
	cause error
}

func (e causeError) Error() string       { return e.msg }
func (e causeError) Unwrap() error       { return e.cause }
func (e causeError) UserMessage() string { return e.msg }

func TestRunFailureCauseIsLoggedNotShown(t *testing.T) {
	h := newHarness(t, []string{"shop-api"})
	logs := &syncBuf{}
	h.tune = logTo(logs)
	h.adapter.events = []agents.Event{{Kind: agents.EventMessage, Role: agents.RoleAssistant, Text: "working"}}
	h.adapter.waitErr = causeError{"Claude Code stopped before it finished (exit code 1).", errors.New("claude stderr: error: unknown option '--frobnicate'")}
	h.start()
	id := h.send(h.msg("ask", "look", 0)).Task.ID
	h.waitStatus(id, core.TaskFailed)
	if out := logs.String(); !strings.Contains(out, "agent run failed") || !strings.Contains(out, "unknown option") {
		t.Errorf("the log lacks the cause:\n%s", out)
	}
	if strings.Contains(transcript(t, h, id), "unknown option") {
		t.Errorf("the CLI's stderr reached the page: %s", transcript(t, h, id))
	}
}

// ---- no task starts once the service is closing ----

func TestNoTaskStartsOnceTheServiceIsClosing(t *testing.T) {
	h := newHarness(t, []string{"shop-api"})
	h.start()
	if err := h.svc.Close(); err != nil {
		t.Fatal(err)
	}
	_, err := h.svc.Message(context.Background(), h.msg("ask", "look", 0))
	var rj *RejectedError
	if !errors.As(err, &rj) || rj.Reason != "shutting_down" {
		t.Fatalf("got %v, want shutting_down", err)
	}
	if list, _ := h.store.ListTasks(context.Background(), h.project.ID); len(list) != 0 {
		t.Errorf("a task was created during shutdown: %+v", list)
	}
	if n := len(h.adapter.Starts()); n != 0 {
		t.Errorf("%d run(s) started", n)
	}
}

// ---- an unavailable agent is refused, and its internal error is not shown ----

type internalError struct{}

func (internalError) Error() string { return `open C:\Users\someone\secret.db: access denied` }

func TestUnavailableAgentIsRefused(t *testing.T) {
	h := newHarness(t, []string{"shop-api"})
	h.start()
	h.adapter.checkErr = causeError{msg: "The agent is not signed in. Sign in with its own app first."}
	_, err := h.svc.Message(context.Background(), h.msg("ask", "look", 0))
	var rj *RejectedError
	if !errors.As(err, &rj) || rj.Reason != "agent_unavailable" || !strings.Contains(rj.Message, "not signed in") {
		t.Fatalf("got %v", err)
	}
	// An error without a message meant for people is never shown.
	h.adapter.checkErr = internalError{}
	_, err = h.svc.Message(context.Background(), h.msg("ask", "look", 0))
	if !errors.As(err, &rj) || rj.Reason != "agent_unavailable" || strings.Contains(rj.Message, "someone") || strings.Contains(rj.Message, "secret.db") {
		t.Fatalf("internal error leaked: %v", err)
	}
	if list, _ := h.store.ListTasks(context.Background(), h.project.ID); len(list) != 0 {
		t.Errorf("a refused message left %d task(s)", len(list))
	}
	if n := len(h.adapter.Starts()); n != 0 {
		t.Errorf("%d run(s) started", n)
	}
}

// ---- the processes are killed before the final status is published ----

type orderLog struct {
	mu     sync.Mutex
	events []string
}

func (o *orderLog) add(s string) {
	o.mu.Lock()
	o.events = append(o.events, s)
	o.mu.Unlock()
}

func TestKillHappensBeforeTheFinalStatusIsPublished(t *testing.T) {
	h := newHarness(t, []string{"shop-api"})
	order := &orderLog{}
	h.tune = func(c *Config) {
		c.Killer = func(task string) error {
			order.add("kill")
			return h.killer.Kill(task)
		}
		c.Hub = spyHub{fakeHub: h.hub, onStatus: func(m StatusMessage) { order.add("status:" + string(m.Status)) }}
	}
	h.start()
	id := h.send(h.msg("ask", "look", 0)).Task.ID
	h.waitStatus(id, core.TaskDone)
	waitFor(t, 5*time.Second, func() bool {
		order.mu.Lock()
		defer order.mu.Unlock()
		for _, e := range order.events {
			if e == "status:done" {
				return true
			}
		}
		return false
	}, "the final status")
	order.mu.Lock()
	defer order.mu.Unlock()
	lastKill, done := -1, -1
	for i, e := range order.events {
		switch e {
		case "kill":
			lastKill = i
		case "status:done":
			done = i
		}
	}
	if lastKill < 0 || lastKill > done {
		t.Errorf("order = %v: the last kill must come before the final status", order.events)
	}
}
