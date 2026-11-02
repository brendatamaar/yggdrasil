package verify_test

import (
	"context"
	"errors"
	"strings"
	"testing"

	"github.com/brendatamaar/yggdrasil/internal/apperr"
	cv "github.com/brendatamaar/yggdrasil/internal/core/verify"
	"github.com/brendatamaar/yggdrasil/internal/verify"
)

func TestSendFailureUnknownOutcomeIsNeverRepeated(t *testing.T) {
	r := failedRig(t)
	id := r.records()[0].ID
	r.msg.Err = errors.New("timeout waiting for the agent")
	err := r.svc.SendFailure(context.Background(), id)
	if err == nil || !strings.Contains(err.Error(), "couldn't tell") {
		t.Fatalf("err = %v", err)
	}
	if !r.records()[0].Sending {
		t.Fatal("an unknown outcome must stay marked as sending")
	}
	r.msg.Err = nil
	err = r.svc.SendFailure(context.Background(), id)
	if c, ok := apperr.ClassOf(err); !ok || c != apperr.Conflict {
		t.Fatalf("retry = %v, want a conflict", err)
	}
	if n := len(r.msg.Messages()); n != 0 {
		t.Fatalf("%d messages after an unknown outcome and a retry, want 0", n)
	}
}

func TestSendFailureCrashAfterSendNeverDuplicates(t *testing.T) {
	r := failedRig(t)
	id := r.records()[0].ID
	// The process dies right after the message left: the result is not stored.
	r.msg.During = func() { r.store.FailCommit = func() error { return errors.New("disk gone") } }
	if err := r.svc.SendFailure(context.Background(), id); err != nil {
		t.Fatalf("the message was sent: %v", err)
	}
	r.store.FailCommit = nil
	r.msg.During = nil
	rec := r.records()[0]
	if !rec.Sending || rec.Sent {
		t.Fatalf("sending=%v sent=%v, want a mark that blocks a resend", rec.Sending, rec.Sent)
	}
	svc := r.newService() // restart
	if err := svc.SendFailure(context.Background(), id); err == nil {
		t.Fatal("a restart resent the failure")
	}
	if n := len(r.msg.Messages()); n != 1 {
		t.Fatalf("%d messages, want exactly 1", n)
	}
}

func TestSendFailureSentSurvivesRestart(t *testing.T) {
	r := failedRig(t)
	id := r.records()[0].ID
	if err := r.svc.SendFailure(context.Background(), id); err != nil {
		t.Fatal(err)
	}
	if rec := r.records()[0]; !rec.Sent || rec.Sending {
		t.Fatalf("sent=%v sending=%v", rec.Sent, rec.Sending)
	}
	svc := r.newService()
	if err := svc.SendFailure(context.Background(), id); err == nil {
		t.Fatal("sent flag lost across a restart")
	}
	if n := len(r.msg.Messages()); n != 1 {
		t.Fatalf("%d messages", n)
	}
	if !contains(r.auditActions(), "verify.send_failure") {
		t.Fatal("the send is not audited")
	}
}

func TestSendMarkRoundTripAndRejections(t *testing.T) {
	failed := verification(allStates()["failed exit"])
	for _, m := range [][2]bool{{true, false}, {false, true}} {
		row, err := verify.RowWithMarkForTest(failed, m[0], m[1])
		if err != nil {
			t.Fatal(err)
		}
		rec, err := verify.VerificationFromRow(row)
		if err != nil || rec.Sent != m[0] || rec.Sending != m[1] {
			t.Fatalf("round trip lost the mark: %+v %v", rec, err)
		}
	}
	if _, err := verify.RowWithMarkForTest(failed, true, true); err == nil {
		t.Fatal("sent and sending together accepted")
	}
	for name, s := range allStates() {
		if _, isFailed := s.(cv.Failed); isFailed {
			continue
		}
		if _, err := verify.RowWithMarkForTest(verification(s), true, false); err == nil {
			t.Fatalf("a %s row accepted a sent mark", name)
		}
		if _, err := verify.RowWithMarkForTest(verification(s), false, true); err == nil {
			t.Fatalf("a %s row accepted a sending mark", name)
		}
	}
}

func TestSendFailureRedactsTheWholeMessage(t *testing.T) {
	r := failedRig(t)
	id := r.records()[0].ID
	if err := r.svc.SendFailure(context.Background(), id); err != nil {
		t.Fatal(err)
	}
	for _, m := range r.msg.Messages() {
		if strings.Contains(m.Text, secret) {
			t.Fatal("secret in the message")
		}
	}
}
