package api

import (
	"context"
	"net/http"
	"testing"

	"github.com/brendatamaar/yggdrasil/internal/agents/mock"
	"github.com/brendatamaar/yggdrasil/internal/repos"
)

// checkHookAdapter runs a hook inside Check, which the task handler calls
// between its first tracked check and the commit.
type checkHookAdapter struct {
	*mock.Adapter
	hook func()
}

func (checkHookAdapter) Name() string { return "hook" }
func (a checkHookAdapter) Check(context.Context) error {
	a.hook()
	return nil
}

// An untrack that commits after the handler's first check must not let the
// task in (invariant 6): the write transaction checks again.
func TestTaskCreationRacesWithUntrack(t *testing.T) {
	var e *env
	e = newEnv(t, "plain", 0, func(d *Deps) {
		svc, err := repos.NewTrackingService(repos.Deps{Store: d.Store, Redactor: d.Redactor})
		if err != nil {
			t.Fatal(err)
		}
		d.Tracking = svc
		d.Adapters["hook"] = checkHookAdapter{Adapter: mock.New(mock.Options{}), hook: func() {
			resp, data := e.do(t, http.MethodPost, "/api/repos/"+string(e.repo.ID)+"/untrack", nil)
			if resp.StatusCode != http.StatusOK {
				t.Errorf("untrack in the race = %d %s", resp.StatusCode, data)
			}
		}}
	})
	req := e.createReq()
	req.Agent = "hook"
	resp, data := e.do(t, http.MethodPost, "/api/tasks", req)
	if resp.StatusCode != http.StatusNotFound {
		t.Fatalf("task created while the repo was untracked = %d %s, want 404", resp.StatusCode, data)
	}
	tasks, err := e.store.ListTasks(context.Background(), e.project.ID)
	if err != nil || len(tasks) != 0 {
		t.Errorf("tasks after the refused creation = %v, %v", tasks, err)
	}
}
