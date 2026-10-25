package mcp

import "time"

// Bucket is the token bucket of one session. The zero value is a full bucket:
// the first Tick sees a huge elapsed time and refills it to Burst.
type Bucket struct {
	Tokens float64
	Last   time.Time
}

// Tick is one call arriving; Now comes from the shell.
type Tick struct{ Now time.Time }

// Allowance is the answer of StepLimiter.
type Allowance struct {
	Allowed bool
	// RetryAfter is how long until one token is back, when not Allowed.
	RetryAfter time.Duration
}

// StepLimiter refills the bucket at RatePerSecond up to Burst, then takes one
// token if there is one. A clock that goes backwards refills nothing. M5: it
// never allows more than Burst + rate x elapsed calls in any window.
func StepLimiter(b Bucket, t Tick) (Bucket, Allowance) {
	tokens := b.Tokens
	if b.Last.IsZero() {
		tokens = Burst
	} else if el := t.Now.Sub(b.Last); el > 0 {
		tokens += RatePerSecond * el.Seconds()
	}
	if tokens > Burst {
		tokens = Burst
	}
	next := Bucket{Tokens: tokens, Last: t.Now}
	if b.Last.After(t.Now) {
		next.Last = b.Last // never move the clock back
	}
	if tokens >= 1 {
		next.Tokens = tokens - 1
		return next, Allowance{Allowed: true}
	}
	wait := time.Duration((1 - tokens) / RatePerSecond * float64(time.Second))
	return next, Allowance{RetryAfter: wait}
}
