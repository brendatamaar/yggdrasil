package verify_test

import (
	"context"
	"encoding/json"
	"errors"
	"testing"

	"github.com/brendatamaar/yggdrasil/internal/apperr"
)

// The opt-in given at the click reaches the messenger, false stays false, and
// the audit entries record the boolean only.
func TestSendFailureCarriesTheOptIn(t *testing.T) {
	for _, optIn := range []bool{false, true} {
		r := failedRig(t)
		if err := r.svc.SendFailure(context.Background(), r.records()[0].ID, optIn); err != nil {
			t.Fatal(err)
		}
		msgs := r.msg.Messages()
		if len(msgs) != 1 || msgs[0].OptIn != optIn {
			t.Fatalf("optIn=%v: messages %+v", optIn, msgs)
		}
		seen := 0
		for _, e := range r.store.Audit() {
			if e.Action != "verify.send_failure" {
				continue
			}
			var d map[string]any
			if err := json.Unmarshal(e.Detail, &d); err != nil {
				t.Fatal(err)
			}
			if got, ok := d["optIn"].(bool); !ok || got != optIn {
				t.Errorf("audit detail %s, want optIn=%v", e.Detail, optIn)
			}
			seen++
		}
		if seen != 1 {
			t.Errorf("%d send audit entries", seen)
		}
	}
}

// A refusal for the missing opt-in is a known refusal: the failure is
// unmarked and can be sent again with the opt-in.
func TestSendFailureNeedsOptInIsAKnownRefusal(t *testing.T) {
	r := failedRig(t)
	id := r.records()[0].ID
	r.msg.Err = apperr.NeedsOptIn("This mode edits your main checkout directly. Turn on the opt-in to use it.", errors.New("rejected"))
	err := r.svc.SendFailure(context.Background(), id, false)
	if !errors.Is(err, apperr.ErrNeedsOptIn) {
		t.Fatalf("err = %v", err)
	}
	if rec := r.records()[0]; rec.Sent || rec.Sending {
		t.Fatalf("the failure stays marked after a refusal: %+v", rec)
	}
	r.msg.Err = nil
	if err := r.svc.SendFailure(context.Background(), id, true); err != nil {
		t.Fatal(err)
	}
	if m := r.msg.Messages(); len(m) != 1 || !m[0].OptIn {
		t.Fatalf("messages %+v", m)
	}
}
