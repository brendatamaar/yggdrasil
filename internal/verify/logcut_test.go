package verify_test

import (
	"context"
	"strings"
	"testing"

	"github.com/brendatamaar/yggdrasil/internal/builds"
	cv "github.com/brendatamaar/yggdrasil/internal/core/verify"
)

// A secret cut in two at the start of the kept log must not survive: the cut
// leaves its end, which the redactor does not recognize.
func TestSendFailureLogCutThroughASecretLeaksNothing(t *testing.T) {
	r := newRig(t)
	r.tc.verify = []builds.Step{step("lint", 0), step("test", 3)}
	r.settle()
	rec := r.records()[0]
	f := rec.State.(cv.Failed)
	// Many secrets so redaction shrinks the text below the limit; the cut of
	// the kept bytes falls inside one of them.
	r.runner.SetLog(f.Build, 1, strings.Repeat("x", 10)+strings.Repeat(secret, 3000))
	if err := r.svc.SendFailure(context.Background(), rec.ID); err != nil {
		t.Fatal(err)
	}
	text := r.msg.Messages()[0].Text
	for _, frag := range []string{"4711", "value-", "sentinel"} {
		if strings.Contains(text, frag) {
			t.Fatalf("message leaks a piece of the secret %q: %.300s", frag, text)
		}
	}
}
