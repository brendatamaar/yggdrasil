package verify_test

import (
	"context"
	"errors"
	"strings"
	"testing"

	"github.com/brendatamaar/yggdrasil/internal/apperr"
	"github.com/brendatamaar/yggdrasil/internal/builds"
	cv "github.com/brendatamaar/yggdrasil/internal/core/verify"
	"github.com/brendatamaar/yggdrasil/internal/verify"
)

func TestSendFailureUnknownOutcomeIsNeverRepeated(t *testing.T) {
	r := failedRig(t)
	id := r.records()[0].ID
	r.msg.Err = errors.New("timeout waiting for the agent")
	err := r.svc.SendFailure(context.Background(), id, false)
	if err == nil || !strings.Contains(err.Error(), "couldn't tell") {
		t.Fatalf("err = %v", err)
	}
	if !r.records()[0].Sending {
		t.Fatal("an unknown outcome must stay marked as sending")
	}
	r.msg.Err = nil
	err = r.svc.SendFailure(context.Background(), id, false)
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
	if err := r.svc.SendFailure(context.Background(), id, false); err != nil {
		t.Fatalf("the message was sent: %v", err)
	}
	r.store.FailCommit = nil
	r.msg.During = nil
	rec := r.records()[0]
	if !rec.Sending || rec.Sent {
		t.Fatalf("sending=%v sent=%v, want a mark that blocks a resend", rec.Sending, rec.Sent)
	}
	svc := r.newService() // restart
	if err := svc.SendFailure(context.Background(), id, false); err == nil {
		t.Fatal("a restart resent the failure")
	}
	if n := len(r.msg.Messages()); n != 1 {
		t.Fatalf("%d messages, want exactly 1", n)
	}
}

func TestSendFailureSentSurvivesRestart(t *testing.T) {
	r := failedRig(t)
	id := r.records()[0].ID
	if err := r.svc.SendFailure(context.Background(), id, false); err != nil {
		t.Fatal(err)
	}
	if rec := r.records()[0]; !rec.Sent || rec.Sending {
		t.Fatalf("sent=%v sending=%v", rec.Sent, rec.Sending)
	}
	svc := r.newService()
	if err := svc.SendFailure(context.Background(), id, false); err == nil {
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

// The composed message is redacted as a whole: the secret is in the step name
// (not only in the log tail, which has its own redaction), in the log and in
// the failure reason's place. The control checks the message really contains
// the step and the log, so an empty message cannot pass.
func TestSendFailureRedactsTheWholeMessage(t *testing.T) {
	r := newRig(t)
	r.tc.verify = []builds.Step{step("lint", 0), step("test "+secret, 3)}
	r.settle()
	rec := r.records()[0]
	f, ok := rec.State.(cv.Failed)
	if !ok {
		t.Fatalf("state = %#v", rec.State)
	}
	r.runner.SetLog(f.Build, 1, "FAIL widget_test.go\nusing "+secret+" as key\nexit status 3\n")
	if err := r.svc.SendFailure(context.Background(), rec.ID, false); err != nil {
		t.Fatal(err)
	}
	msgs := r.msg.Messages()
	if len(msgs) != 1 {
		t.Fatalf("%d messages, want 1", len(msgs))
	}
	text := msgs[0].Text
	for _, frag := range []string{secret, "4711", "sentinel"} {
		if strings.Contains(text, frag) {
			t.Fatalf("the message leaks %q: %.400s", frag, text)
		}
	}
	for _, want := range []string{"test ", "widget_test.go", "exit status 3"} {
		if !strings.Contains(text, want) {
			t.Errorf("the message lost %q (the control that it is a real message): %.400s", want, text)
		}
	}
}
