package api

import (
	"testing"

	"github.com/brendatamaar/yggdrasil/internal/core"
	"github.com/brendatamaar/yggdrasil/internal/tasks"
)

func TestBoardResponseCarriesNeedsYouReason(t *testing.T) {
	for _, reason := range []string{"integrity_violated", "cap_exceeded", "pending_proposals", ""} {
		v := tasks.BoardView{Cards: []tasks.BoardCard{{
			Task:           core.Task{ID: "tsk_1", Status: core.TaskNeedsYou},
			Column:         "needs_you",
			NeedsYouReason: reason,
		}}}
		if got := boardResponse(v).Cards[0].NeedsYouReason; got != reason {
			t.Errorf("reason %q mapped to %q", reason, got)
		}
	}
}
