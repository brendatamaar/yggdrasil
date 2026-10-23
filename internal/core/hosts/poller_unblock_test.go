package hosts

import (
	"testing"
	"time"
)

// A rejected token blocks the other keys of its instance; a changed token (and
// only a changed one) unblocks them and makes their data stale at once.
func TestChangedTokenUnblocksAndRefetchesAtOnce(t *testing.T) {
	cfg := func(token bool, now time.Time) ConfigChanged {
		return ConfigChanged{Instances: []Instance{{ID: "github.com", Kind: KindGitHub, Base: "https://github.com", Token: token}}, Now: now}
	}
	p, _ := Step(Poller{}, cfg(true, t0))
	p, cmds := Step(p, TargetsChanged{Repos: []RepoTarget{
		{Repo: "r1", Instance: "github.com", Project: "a/b", Branch: "main"},
		{Repo: "r2", Instance: "github.com", Project: "a/c", Branch: "main"},
	}, Now: t0})
	k1, k2 := RepoKey{Repo: "r1", Branch: "main"}, RepoKey{Repo: "r2", Branch: "main"}
	attempt := map[Key]int{}
	for _, c := range cmds {
		if x, ok := c.(Fetch); ok {
			attempt[x.Key] = x.Attempt
		}
	}
	// r2 has fresh data; r1's rejected token blocks r2 too, with the data kept.
	p, _ = Step(p, Fetched{Key: k2, Attempt: attempt[k2], Payload: RepoState{Pipeline: &Pipeline{Status: PipelineSuccess}}, Now: t0})
	p, _ = Step(p, FetchFailed{Key: k1, Attempt: attempt[k1], Kind: FailUnauthorized, Now: t0})
	b, ok := p.Entries[k2].Phase.(Blocked)
	if !ok || b.Prev.FetchedAt != t0 || b.Prev.Data == nil {
		t.Fatalf("setup: %#v", p.Entries[k2].Phase)
	}
	later := t0.Add(RepoTTL / 2) // the data would still be fresh

	// The same config again: still blocked, no fetch.
	p, cmds = Step(p, cfg(true, later))
	if !isBlocked(p.Entries[k2].Phase, BlockUnauthorized) {
		t.Fatalf("an unchanged config unblocked the key: %#v", p.Entries[k2].Phase)
	}
	for _, c := range cmds {
		if x, ok := c.(Fetch); ok && x.Key == k2 {
			t.Fatal("an unchanged config fetched a blocked key")
		}
	}

	// The token goes away: a change.
	p, off := Step(p, cfg(false, later))
	// The first change already makes the key fetch with the new (empty) token.
	fetched := false
	for _, c := range off {
		if x, ok := c.(Fetch); ok && x.Key == k2 {
			fetched = true
		}
	}
	e := p.Entries[k2]
	if _, still := e.Phase.(Blocked); still || !fetched {
		t.Fatalf("a changed token left the key %#v (fetched %v)", e.Phase, fetched)
	}
	if got := prevOf(e.Phase); !got.FetchedAt.IsZero() || got.Data == nil {
		t.Fatalf("FetchedAt = %v data %v: want a cleared time and kept data", got.FetchedAt, got.Data)
	}
}
