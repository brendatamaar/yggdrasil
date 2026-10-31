package api

import (
	"context"
	"encoding/json"
	"io"
	"net/http"
	"strings"
	"testing"
	"time"

	"github.com/brendatamaar/yggdrasil/internal/agents"
	"github.com/brendatamaar/yggdrasil/internal/core"
	"github.com/brendatamaar/yggdrasil/internal/db"
)

// seedTask stores a task in the given status, as the tasks service would have
// left it.
func (e *env) seedTask(t *testing.T, status core.TaskStatus) core.Task {
	t.Helper()
	ctx := context.Background()
	now := time.Now().UTC()
	id := core.TaskID(db.NewID("tsk"))
	task := core.Task{
		ID: id, ProjectID: e.project.ID, Title: "Task " + string(id)[len(id)-6:], Prompt: "Find the order routes.\n" + secretDetail,
		Scope: core.ScopeRepo, RepoIDs: []core.RepoID{e.repo.ID}, Mode: core.ModeAsk, Agent: "mock", Status: status,
		CreatedAt: now, UpdatedAt: now,
	}
	var out core.Task
	err := e.store.Commit(ctx, func(tx *db.Tx) error {
		var err error
		out, err = tx.CreateTask(ctx, task)
		return err
	})
	if err != nil {
		t.Fatalf("seed task: %v", err)
	}
	return out
}

// setStatus moves a stored task to a new status.
func (e *env) setStatus(t *testing.T, task core.Task, status core.TaskStatus) {
	t.Helper()
	ctx := context.Background()
	err := e.store.Commit(ctx, func(tx *db.Tx) error {
		_, err := tx.UpdateTaskStatus(ctx, task.ID, status, task.Version, time.Now().UTC())
		return err
	})
	if err != nil {
		t.Fatalf("set status: %v", err)
	}
}

// seedEvents stores n text events for a task, with Seq 1..n.
func (e *env) seedEvents(t *testing.T, id core.TaskID, n int) {
	t.Helper()
	for i := 1; i <= n; i++ {
		ev := agents.Event{Seq: int64(i), Kind: agents.EventMessage, At: time.Now().UTC(), Text: "line"}
		data, err := json.Marshal(ev)
		if err != nil {
			t.Fatal(err)
		}
		if err := e.store.AppendTaskEvent(context.Background(), db.TaskEvent{TaskID: id, Seq: ev.Seq, Kind: string(ev.Kind), At: ev.At, Data: data}); err != nil {
			t.Fatal(err)
		}
	}
}

// doRaw sends a body as it is, for requests that must not be valid JSON.
func (e *env) doRaw(t *testing.T, method, path, body string) (reply, []byte) {
	t.Helper()
	req, err := http.NewRequest(method, e.ts.URL+path, strings.NewReader(body))
	if err != nil {
		t.Fatal(err)
	}
	req.AddCookie(e.cookie)
	resp, err := http.DefaultClient.Do(req)
	if err != nil {
		t.Fatal(err)
	}
	defer resp.Body.Close()
	data, err := io.ReadAll(resp.Body)
	if err != nil {
		t.Fatal(err)
	}
	return reply{StatusCode: resp.StatusCode, Header: resp.Header}, data
}
