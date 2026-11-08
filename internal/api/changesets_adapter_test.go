package api

import (
	"errors"
	"fmt"
	"testing"

	"github.com/brendatamaar/yggdrasil/internal/apperr"
	"github.com/brendatamaar/yggdrasil/internal/changesets"
	"github.com/brendatamaar/yggdrasil/internal/db"
)

func TestChangeSetErrMapsEachRefusalToAClass(t *testing.T) {
	classed := apperr.New(apperr.Unavailable, "later", errors.New("x"))
	cases := []struct {
		name string
		err  error
		want apperr.Class
	}{
		{"busy", fmt.Errorf("send: %w", changesets.ErrAgentBusy), apperr.Conflict},
		{"queue full", fmt.Errorf("send: %w", changesets.ErrQueueFull), apperr.Conflict},
		{"missing", fmt.Errorf("get: %w", db.ErrNotFound), apperr.NotFound},
		{"version", fmt.Errorf("save: %w", db.ErrConflict), apperr.Conflict},
		{"refused", &changesets.Refused{Message: "stale"}, apperr.Conflict},
		{"user mistake", &changesets.UserError{Message: "fix it"}, apperr.Invalid},
		{"user state conflict", &changesets.UserError{Message: "run in progress", Kind: changesets.KindConflict}, apperr.Conflict},
		{"already classified", classed, apperr.Unavailable},
	}
	for _, c := range cases {
		got, ok := apperr.ClassOf(changeSetErr(c.err))
		if !ok || got != c.want {
			t.Errorf("%s: class = %v (%v), want %v", c.name, got, ok, c.want)
		}
	}
	if _, ok := apperr.ClassOf(changeSetErr(errors.New("boom"))); ok {
		t.Error("an unknown error was classified")
	}
	if changeSetErr(nil) != nil {
		t.Error("nil became an error")
	}
}
