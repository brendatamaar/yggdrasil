package api

import (
	"encoding/json"
	"net/http"
	"testing"
	"time"

	"github.com/brendatamaar/yggdrasil/internal/core"
)

// Batch F contracts: Deps carries Clock and IDs with working defaults, the
// per-area services may all be nil, and the new topics work end to end.

func TestClockAndIDsDefaults(t *testing.T) {
	e := newEnv(t, "plain", 0)
	now := e.srv.clock()
	if now.Location() != time.UTC || time.Since(now) > time.Minute || time.Since(now) < -time.Minute {
		t.Errorf("default clock = %v, want about now in UTC", now)
	}
	a, b := e.srv.ids("bld"), e.srv.ids("bld")
	if a == b || len(a) < 5 || a[:4] != "bld_" {
		t.Errorf("default ids = %q, %q, want distinct bld_ IDs", a, b)
	}
}

func TestClockAndIDsFromDeps(t *testing.T) {
	fixed := time.Date(2026, 1, 2, 3, 4, 5, 0, time.UTC)
	e := newEnv(t, "plain", 0, func(d *Deps) {
		d.Clock = func() time.Time { return fixed }
		d.IDs = func(prefix string) string { return prefix + "_fixed" }
	})
	if got := e.srv.clock(); !got.Equal(fixed) {
		t.Errorf("clock = %v", got)
	}
	if got := e.srv.ids("rep"); got != "rep_fixed" {
		t.Errorf("ids = %q", got)
	}
}

// With no service wired, the area routes register nothing: the catch-all
// answers with the usual JSON 404.
func TestUnwiredAreasRegisterNoRoutes(t *testing.T) {
	e := newEnv(t, "plain", 0)
	for _, path := range []string{"/api/repos", "/api/builds", "/api/hosts", "/api/mrs", "/api/search", "/api/fs/dirs"} {
		resp, data := e.do(t, http.MethodGet, path, nil)
		var body ErrorBody
		if resp.StatusCode != http.StatusNotFound || json.Unmarshal(data, &body) != nil || body.Code != ErrNotFound {
			t.Errorf("GET %s = %d %s, want the JSON 404", path, resp.StatusCode, data)
		}
	}
}

func TestNewTopicsDeliverMessages(t *testing.T) {
	e := newEnv(t, "plain", 0)
	ws := e.dial(t)
	cases := []struct {
		topic wsTopic
		data  any
	}{
		{reposTopic{}, ReposChangedMessage{}},
		{repoTopic{ID: "rep_1"}, RepoStatusDTO{ID: "rep_1"}},
		{buildsTopic{}, BuildChangedMessage{BuildID: "bld_1", Status: core.BuildQueued}},
		{buildTopic{ID: "bld_1"}, BuildLogMessage{BuildID: "bld_1", Line: "ok"}},
		{hostsTopic{}, HostsChangedMessage{}},
	}
	for _, c := range cases {
		subscribe(t, ws, c.topic.String())
	}
	time.Sleep(150 * time.Millisecond) // let the subscriptions register
	for _, c := range cases {
		e.srv.hub.publishNext(c.topic, c.data)
		m, err := readMsg(t, ws, 5*time.Second)
		if err != nil {
			t.Fatalf("topic %s: %v", c.topic, err)
		}
		if m.Type != MsgEvent || m.Topic != c.topic.String() || m.Seq != 1 {
			t.Errorf("topic %s: got %+v", c.topic, m)
		}
	}
	// An unknown topic is still refused with an error message.
	subscribe(t, ws, "builds:all")
	m, err := readMsg(t, ws, 5*time.Second)
	if err != nil || m.Type != MsgError {
		t.Fatalf("unknown topic: %+v, %v", m, err)
	}
}
