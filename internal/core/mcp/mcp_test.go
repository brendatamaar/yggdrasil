package mcp

import (
	"testing"
	"time"
)

func TestToolSetIsClosedAndHasNoConfirm(t *testing.T) {
	seen := map[Tool]bool{}
	for _, tool := range AllTools() {
		if seen[tool] {
			t.Errorf("duplicate tool %q", tool)
		}
		seen[tool] = true
		if err := tool.Validate(); err != nil {
			t.Error(err)
		}
		for _, bad := range []string{"confirm", "reject", "execute", "publish", "subtasks"} {
			if contains(string(tool), bad) {
				t.Errorf("tool %q looks like it can %s (M4)", tool, bad)
			}
		}
	}
	for _, name := range []string{"propose_publish", "propose_subtasks", "", "confirm_proposal"} {
		if Tool(name).Validate() == nil {
			t.Errorf("%q is in the tool set", name)
		}
	}
}

func contains(s, sub string) bool {
	for i := 0; i+len(sub) <= len(s); i++ {
		if s[i:i+len(sub)] == sub {
			return true
		}
	}
	return false
}

func TestStubsFailClosed(t *testing.T) {
	c := Context{Task: "t", Live: true}
	if got := Offered(c); len(got) != 0 {
		t.Errorf("Offered = %v", got)
	}
	for _, tool := range AllTools() {
		if d := Authorize(c, tool); d.Allowed {
			t.Errorf("Authorize(%q) allowed", tool)
		}
	}
	if _, err := ParseCall("list_repos", nil); err == nil {
		t.Error("ParseCall stub accepted a call")
	}
}

func TestLimiterBoundsCalls(t *testing.T) {
	start := time.Date(2026, 1, 1, 0, 0, 0, 0, time.UTC)
	var b Bucket
	allowed := 0
	// 1000 calls in the same instant: at most Burst get through.
	for range 1000 {
		var a Allowance
		b, a = StepLimiter(b, Tick{Now: start})
		if a.Allowed {
			allowed++
		}
	}
	if allowed != Burst {
		t.Fatalf("allowed %d calls in one instant, want %d", allowed, Burst)
	}
	_, a := StepLimiter(b, Tick{Now: start})
	if a.Allowed || a.RetryAfter <= 0 {
		t.Fatalf("empty bucket: %+v", a)
	}
	// One second later about RatePerSecond calls are back.
	later := start.Add(time.Second)
	allowed = 0
	for range 100 {
		var a Allowance
		b, a = StepLimiter(b, Tick{Now: later})
		if a.Allowed {
			allowed++
		}
	}
	if allowed != int(RatePerSecond) {
		t.Fatalf("allowed %d calls after 1s, want %d", allowed, int(RatePerSecond))
	}
	// A clock that goes backwards refills nothing and does not move Last back.
	b2, a := StepLimiter(b, Tick{Now: start})
	if a.Allowed || !b2.Last.Equal(later) {
		t.Fatalf("backwards clock: %+v %v", a, b2.Last)
	}
}
