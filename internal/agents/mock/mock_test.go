package mock

import (
	"context"
	"testing"
	"time"

	"github.com/brendatamaar/yggdrasil/internal/agents"
)

func TestFixturesParse(t *testing.T) {
	for _, name := range []string{"plain", "edits", "error", "subagents"} {
		ev, err := Load(name)
		if err != nil || len(ev) == 0 {
			t.Errorf("%s: %d events, err %v", name, len(ev), err)
			continue
		}
		last := ev[len(ev)-1]
		if last.Kind != agents.EventStatus || (last.Status != agents.RunCompleted && last.Status != agents.RunFailed) {
			t.Errorf("%s: last event is %+v", name, last)
		}
	}
	if _, err := Load("nope"); err == nil {
		t.Error("unknown fixture accepted")
	}
}

func TestCancelEmitsCancelled(t *testing.T) {
	ctx, cancel := context.WithCancel(context.Background())
	r, err := New(Options{Fixture: "plain", Delay: time.Hour}).Start(ctx, agents.StartOptions{})
	if err != nil {
		t.Fatal(err)
	}
	cancel()
	var got []agents.Event
	for e := range r.Events() {
		got = append(got, e)
	}
	if len(got) != 1 || got[0].Status != agents.RunCancelled {
		t.Fatalf("events = %+v", got)
	}
	if err := r.Wait(); err != nil {
		t.Error(err)
	}
}
