package api

import (
	"context"
	"net/http"
	"sync"
	"testing"
	"time"

	"github.com/brendatamaar/yggdrasil/internal/core"
)

// killRecorder is a KillTask that records the task IDs it gets and the status
// the store held at that moment.
type killRecorder struct {
	mu       sync.Mutex
	ids      []string
	statuses []core.TaskStatus
	store    func(id string) core.TaskStatus
}

func (k *killRecorder) kill(id string) error {
	st := k.store(id)
	k.mu.Lock()
	defer k.mu.Unlock()
	k.ids = append(k.ids, id)
	k.statuses = append(k.statuses, st)
	return nil
}

func (k *killRecorder) calls() ([]string, []core.TaskStatus) {
	k.mu.Lock()
	defer k.mu.Unlock()
	return append([]string(nil), k.ids...), append([]core.TaskStatus(nil), k.statuses...)
}

func newKillEnv(t *testing.T, delay time.Duration) (*env, *killRecorder) {
	t.Helper()
	rec := &killRecorder{}
	e := newEnv(t, "subagents", delay, func(d *Deps) { d.KillTask = rec.kill })
	rec.store = func(id string) core.TaskStatus {
		task, err := e.store.GetTask(context.Background(), core.TaskID(id))
		if err != nil {
			return core.TaskStatus("unreadable")
		}
		return task.Status
	}
	return e, rec
}

// The task's processes are stopped before the final status is stored, so a
// client never sees done or cancelled while they live.
func TestTaskProcessesAreKilledOnceBeforeTheFinalStatus(t *testing.T) {
	for name, tc := range map[string]struct {
		cancel bool
		final  core.TaskStatus
	}{
		"normal finish": {false, core.TaskDone},
		"cancel":        {true, core.TaskCancelled},
	} {
		t.Run(name, func(t *testing.T) {
			delay := time.Duration(0)
			if tc.cancel {
				delay = 300 * time.Millisecond
			}
			e, rec := newKillEnv(t, delay)
			task := e.create(t)
			if tc.cancel {
				e.waitStatus(t, task.ID, core.TaskRunning)
				resp, data := e.do(t, http.MethodPost, "/api/tasks/"+string(task.ID)+"/cancel", nil)
				if resp.StatusCode != http.StatusAccepted {
					t.Fatalf("cancel = %d %s", resp.StatusCode, data)
				}
			}
			e.waitStatus(t, task.ID, tc.final)
			ids, statuses := rec.calls()
			if len(ids) != 1 || ids[0] != string(task.ID) {
				t.Fatalf("KillTask calls = %v, want exactly one for %s", ids, task.ID)
			}
			if statuses[0].Terminal() {
				t.Errorf("task was already %s when its processes were killed", statuses[0])
			}
		})
	}
}

// A task the core calls invalid is a 400 with the plain error body, not a 500.
func TestCreateTaskThatTheCoreRejectsIsABadRequest(t *testing.T) {
	e := newEnv(t, "plain", 0)
	e.srv.newTaskID = func() core.TaskID { return "not a valid id" }
	resp, data := e.do(t, http.MethodPost, "/api/tasks", e.createReq())
	if resp.StatusCode != http.StatusBadRequest {
		t.Fatalf("create = %d %s, want 400", resp.StatusCode, data)
	}
	tasks, err := e.store.ListTasks(context.Background(), e.project.ID)
	if err != nil || len(tasks) != 0 {
		t.Errorf("tasks = %+v, %v; a rejected task must not be stored", tasks, err)
	}
}
