package api

import (
	"encoding/json"
	"errors"
	"slices"
	"testing"

	"github.com/brendatamaar/yggdrasil/internal/apperr"
)

// The opt-in travels with the click: absent means false, true reaches the
// service, and an unknown field is still refused.
func TestSendOptInBodyParsing(t *testing.T) {
	f := &fakeP4{}
	e := p4Env(t, f, &fakeDiffer{fakeP4: f})
	steps := []struct {
		path string
		body any
		want bool
	}{
		{"/api/verifications/v1/send-failure", nil, false},
		{"/api/verifications/v1/send-failure", map[string]any{}, false},
		{"/api/verifications/v1/send-failure", SendFailureRequest{OptIn: true}, true},
		{"/api/tasks/tsk_1/notes/send", SendNotesRequest{IDs: []string{"n1"}}, false},
		{"/api/tasks/tsk_1/notes/send", SendNotesRequest{IDs: []string{"n1"}, OptIn: true}, true},
	}
	var want []bool
	for _, s := range steps {
		if resp, data := e.do(t, "POST", s.path, s.body); resp.StatusCode != 200 {
			t.Fatalf("POST %s = %d %s", s.path, resp.StatusCode, data)
		}
		want = append(want, s.want)
	}
	if !slices.Equal(f.optIns, want) {
		t.Errorf("optIn seen by the services = %v, want %v", f.optIns, want)
	}
	if resp, _ := e.do(t, "POST", "/api/verifications/v1/send-failure", map[string]any{"bogus": 1}); resp.StatusCode != 400 {
		t.Errorf("unknown field = %d, want 400", resp.StatusCode)
	}
}

// A refusal for the missing opt-in is a 409 with its own code, so the UI asks
// for the confirmation and sends again.
func TestSendFailureNeedsOptInIs409(t *testing.T) {
	f := &fakeP4{err: apperr.NeedsOptIn("This mode edits your main checkout directly. Turn on the opt-in to use it.", errors.New("x"))}
	e := p4Env(t, f, &fakeDiffer{fakeP4: f})
	for _, path := range []string{"/api/verifications/v1/send-failure", "/api/tasks/tsk_1/notes/send"} {
		var body any
		if path[len(path)-4:] == "send" {
			body = SendNotesRequest{IDs: []string{"n1"}}
		}
		resp, data := e.do(t, "POST", path, body)
		var eb ErrorBody
		_ = json.Unmarshal(data, &eb)
		if resp.StatusCode != 409 || eb.Code != ErrNeedsOptIn || eb.Message == "" {
			t.Errorf("%s = %d %s", path, resp.StatusCode, data)
		}
	}
}
