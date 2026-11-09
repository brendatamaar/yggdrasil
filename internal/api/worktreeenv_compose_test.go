package api

import (
	"context"
	"encoding/json"
	"fmt"
	"sync"
	"testing"

	"github.com/brendatamaar/yggdrasil/internal/core"
	"github.com/brendatamaar/yggdrasil/internal/worktreeenv"
)

// composeEnv is a WorktreeEnvService that also implements ComposeAPI.
type composeEnv struct {
	fakeEnv
	cmu     sync.Mutex
	actions []string
	err     error
}

func (c *composeEnv) ComposeAction(_ context.Context, w core.WorktreeID, a ComposeAction, hash string) (WorktreeEnvDTO, error) {
	c.cmu.Lock()
	c.actions = append(c.actions, fmt.Sprintf("%s:%s:%s", w, a, hash))
	c.cmu.Unlock()
	return WorktreeEnvDTO{WorktreeID: w, Compose: ComposeDown}, c.err
}

func TestComposeEndpointsCallTheService(t *testing.T) {
	f := &composeEnv{}
	e := newEnv(t, func(d *Deps) { d.Phase5 = Phase5Services{WorktreeEnv: f} })
	for _, op := range []string{"up", "stop", "down", "confirm"} {
		resp, data := e.do(t, "POST", "/api/worktrees/wt_1/compose/"+op, nil)
		var out WorktreeEnvDTO
		if resp.StatusCode != 200 || json.Unmarshal(data, &out) != nil || out.Compose != ComposeDown || out.Profile == nil {
			t.Errorf("%s = %d %s", op, resp.StatusCode, data)
		}
	}
	if resp, _ := e.do(t, "POST", "/api/worktrees/wt_1/compose/confirm", ComposeConfirmRequest{Hash: "abc"}); resp.StatusCode != 200 {
		t.Errorf("confirm with hash = %d", resp.StatusCode)
	}
	want := []string{"wt_1:up:", "wt_1:stop:", "wt_1:down:", "wt_1:confirm:", "wt_1:confirm:abc"}
	if fmt.Sprint(f.actions) != fmt.Sprint(want) {
		t.Fatalf("actions = %v", f.actions)
	}
	// the action comes from the route: there is no route for anything else
	if resp, _ := e.do(t, "POST", "/api/worktrees/wt_1/compose/down-v", nil); resp.StatusCode == 200 {
		t.Errorf("unknown action answered 200")
	}
	if resp, _ := e.do(t, "POST", "/api/worktrees/wt_1/compose/up", map[string]string{"args": "-v"}); resp.StatusCode != 200 {
		t.Errorf("a body on up is ignored, got %d", resp.StatusCode)
	}
	if resp, _ := e.do(t, "GET", "/api/worktrees/wt_1/compose", nil); resp.StatusCode != 200 {
		t.Errorf("GET state = %d", resp.StatusCode)
	}
	if resp, _ := e.do(t, "POST", "/api/worktrees/wt_1/compose/confirm", map[string]string{"hash": "a", "extra": "b"}); resp.StatusCode != 400 {
		t.Errorf("unknown body field = %d, want 400", resp.StatusCode)
	}
}

func TestComposeEndpointErrorsMapToStatuses(t *testing.T) {
	for name, tc := range map[string]struct {
		err  error
		code int
	}{
		"confirm first": {&worktreeenv.Error{Msg: "Confirm the file first.", Err: worktreeenv.ErrComposeConfirm}, 409},
		"busy":          {&worktreeenv.Error{Msg: "Still running.", Err: worktreeenv.ErrComposeBusy}, 409},
		"stale":         {&worktreeenv.Error{Msg: "File changed.", Err: worktreeenv.ErrComposeStale}, 409},
		"not tracked":   {worktreeenv.ErrNotFound, 404},
	} {
		f := &composeEnv{err: tc.err}
		e := newEnv(t, func(d *Deps) { d.Phase5 = Phase5Services{WorktreeEnv: f} })
		resp, data := e.do(t, "POST", "/api/worktrees/wt_1/compose/up", nil)
		var b ErrorBody
		if resp.StatusCode != tc.code || json.Unmarshal(data, &b) != nil || b.Message == "" {
			t.Errorf("%s = %d %s", name, resp.StatusCode, data)
		}
	}
}
