package tasks

import (
	"testing"

	coretasks "github.com/brendatamaar/yggdrasil/internal/core/tasks"
)

func TestNeedsYouReason(t *testing.T) {
	idle := func(o coretasks.Outcome) coretasks.State { return coretasks.Idle{Outcome: o} }
	tests := []struct {
		name string
		st   coretasks.State
		pend int
		want string
	}{
		{"integrity violated", idle(coretasks.NeedsYou{Why: coretasks.NeedsIntegrityViolated}), 0, "integrity_violated"},
		{"cap exceeded", idle(coretasks.NeedsYou{Why: coretasks.NeedsCapExceeded}), 0, "cap_exceeded"},
		{"why wins over pending proposals", idle(coretasks.NeedsYou{Why: coretasks.NeedsCapExceeded}), 3, "cap_exceeded"},
		{"only pending proposals", idle(coretasks.ReadyForReview{}), 2, "pending_proposals"},
		{"answered with proposals", idle(coretasks.Answered{}), 1, "pending_proposals"},
		{"no reason: review", idle(coretasks.ReadyForReview{}), 0, ""},
		{"no reason: running", coretasks.Running{}, 0, ""},
		{"no reason: waiting", coretasks.Waiting{}, 0, ""},
		{"no reason: failed", idle(coretasks.Failed{Kind: coretasks.FailRun}), 0, ""},
	}
	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			col := coretasks.Column(tc.st, tc.pend)
			if got := NeedsYouReason(col, tc.st); got != tc.want {
				t.Fatalf("NeedsYouReason(%s) = %q, want %q", col, got, tc.want)
			}
		})
	}
}
