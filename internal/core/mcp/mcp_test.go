package mcp

import (
	"math/rand"
	"testing"
	"time"
)

func TestToolsHaveNoConfirm(t *testing.T) {
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

// C5: a zero clock never refills or allows, and leaves the bucket alone.
func TestLimiterZeroClockDenies(t *testing.T) {
	b, a := StepLimiter(Bucket{}, Tick{})
	if a.Allowed || a.RetryAfter <= 0 {
		t.Fatalf("zero clock on a fresh bucket allowed: %+v", a)
	}
	if b != (Bucket{}) {
		t.Fatalf("zero clock changed the bucket: %+v", b)
	}
	start := time.Date(2026, 1, 1, 0, 0, 0, 0, time.UTC)
	empty := Bucket{Tokens: 0, Last: start}
	got, a := StepLimiter(empty, Tick{})
	if a.Allowed || got != empty {
		t.Fatalf("zero clock on a used bucket: %+v %+v", a, got)
	}
}

// M5: in any run of ticks (forwards or backwards), the calls allowed never
// exceed Burst + RatePerSecond x elapsed, where elapsed is the furthest the
// clock has advanced from the first tick.
func TestPropLimiterBound(t *testing.T) {
	start := time.Date(2026, 1, 1, 0, 0, 0, 0, time.UTC)
	for seed := int64(1); seed <= 300; seed++ {
		r := rand.New(rand.NewSource(seed))
		var b Bucket
		now := start
		maxNow := start
		allowed := 0
		for i := 0; i < 400; i++ {
			switch k := r.Intn(4); {
			case i == 0:
				// the first tick fixes the origin of the window
			case k == 0:
				now = now.Add(-time.Duration(r.Intn(3000)) * time.Millisecond)
			case k == 1:
				// same instant
			default:
				now = now.Add(time.Duration(r.Intn(400)) * time.Millisecond)
			}
			if now.After(maxNow) {
				maxNow = now
			}
			var a Allowance
			b, a = StepLimiter(b, Tick{Now: now})
			if a.Allowed {
				allowed++
			}
			bound := float64(Burst) + RatePerSecond*maxNow.Sub(start).Seconds()
			if float64(allowed) > bound+1e-6 {
				t.Fatalf("seed %d step %d: allowed %d > bound %.2f", seed, i, allowed, bound)
			}
		}
	}
}

func TestEnforcementValidate(t *testing.T) {
	for _, e := range []Enforcement{EnforcementHook, EnforcementLimited} {
		if err := e.Validate(); err != nil {
			t.Errorf("%q: %v", e, err)
		}
	}
	for _, e := range []Enforcement{"", "none", "HOOK"} {
		if e.Validate() == nil {
			t.Errorf("%q accepted", e)
		}
	}
}
