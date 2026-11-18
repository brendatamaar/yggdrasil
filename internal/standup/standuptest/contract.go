package standuptest

import (
	"context"
	"fmt"
	"testing"
	"time"

	"github.com/brendatamaar/yggdrasil/internal/core"
	"github.com/brendatamaar/yggdrasil/internal/repos"
)

// Base is the first instant the contract suites use.
var Base = time.Date(2026, 6, 1, 9, 0, 0, 0, time.UTC)

// Fact is one record of the suites: a distinct instant per index, so ordering
// and filtering are unambiguous.
type Fact struct {
	At      time.Time
	Subject string
}

// SourceSetup lets a suite seed an implementation and break it.
type SourceSetup interface {
	// Seed makes repo known with exactly these facts.
	Seed(t *testing.T, repo core.RepoID, facts []Fact)
	// Missing returns a repo ID the source does not have (untracked, deleted).
	Missing() core.RepoID
}

// Reader is one source read, reduced to the instants and subjects the suites
// compare. Implementations adapt their own record types.
type Reader func(ctx context.Context, repo core.RepoID, from, to time.Time, max int) (got []Fact, truncated bool, err error)

// CommitReader adapts a repos.HistoryReader to a Reader.
func CommitReader(r repos.HistoryReader) Reader {
	return func(ctx context.Context, repo core.RepoID, from, to time.Time, max int) ([]Fact, bool, error) {
		cs, trunc, err := r.Commits(ctx, repo, from, to, max)
		out := make([]Fact, 0, len(cs))
		for _, c := range cs {
			out = append(out, Fact{At: c.At, Subject: c.Subject})
		}
		return out, trunc, err
	}
}

func facts(n int) []Fact {
	out := make([]Fact, n)
	for i := range out {
		out[i] = Fact{At: Base.Add(time.Duration(i) * time.Minute), Subject: fmt.Sprintf("change %02d", i)}
	}
	return out
}

// SourceContract runs the clauses every source must satisfy. newSource returns
// a fresh implementation (its reader and its setup).
//
//   - time filtering: [from, to), the start included and the end excluded, and
//     only the repo asked for;
//   - bounds: at most max, newest first, truncated exactly when more matched;
//   - missing: an unknown repo is an error, never an empty success;
//   - cancellation: a cancelled context returns an error and no data.
func SourceContract(t *testing.T, newSource func(t *testing.T) (Reader, SourceSetup)) {
	t.Helper()
	const repo, other = core.RepoID("rep_api"), core.RepoID("rep_web")
	ctx := context.Background()

	t.Run("half-open window", func(t *testing.T) {
		read, setup := newSource(t)
		fs := facts(10)
		setup.Seed(t, repo, fs)
		setup.Seed(t, other, []Fact{{At: Base.Add(3 * time.Minute), Subject: "other repo"}})
		got, trunc, err := read(ctx, repo, Base.Add(2*time.Minute), Base.Add(5*time.Minute), 200)
		if err != nil || trunc {
			t.Fatalf("err %v truncated %v", err, trunc)
		}
		// Instants 2, 3 and 4: the start is in, the end (5) is out.
		want := []Fact{fs[4], fs[3], fs[2]}
		if len(got) != len(want) {
			t.Fatalf("got %v, want %v", got, want)
		}
		for i := range want {
			if !got[i].At.Equal(want[i].At) || got[i].Subject != want[i].Subject {
				t.Fatalf("record %d: got %+v, want %+v", i, got[i], want[i])
			}
		}
		if got, _, err := read(ctx, repo, Base.Add(time.Hour), Base.Add(2*time.Hour), 200); err != nil || len(got) != 0 {
			t.Fatalf("empty window: %v, %v", got, err)
		}
	})

	t.Run("bounds", func(t *testing.T) {
		read, setup := newSource(t)
		fs := facts(12)
		setup.Seed(t, repo, fs)
		got, trunc, err := read(ctx, repo, Base, Base.Add(time.Hour), 5)
		if err != nil || !trunc || len(got) != 5 {
			t.Fatalf("max 5: %d records, truncated %v, err %v", len(got), trunc, err)
		}
		// The newest five, newest first.
		for i := range got {
			if want := fs[11-i]; !got[i].At.Equal(want.At) {
				t.Fatalf("record %d: %v, want %v", i, got[i].At, want.At)
			}
		}
		// Exactly max matches is not truncated.
		got, trunc, err = read(ctx, repo, Base, Base.Add(12*time.Minute), 12)
		if err != nil || trunc || len(got) != 12 {
			t.Fatalf("max 12: %d records, truncated %v, err %v", len(got), trunc, err)
		}
	})

	t.Run("missing source", func(t *testing.T) {
		read, setup := newSource(t)
		setup.Seed(t, repo, facts(3))
		got, _, err := read(ctx, setup.Missing(), Base, Base.Add(time.Hour), 200)
		if err == nil || len(got) != 0 {
			t.Fatalf("missing repo: %d records, err %v; want an error", len(got), err)
		}
	})

	t.Run("cancellation", func(t *testing.T) {
		read, setup := newSource(t)
		setup.Seed(t, repo, facts(3))
		cctx, cancel := context.WithCancel(ctx)
		cancel()
		got, _, err := read(cctx, repo, Base, Base.Add(time.Hour), 200)
		if err == nil || len(got) != 0 {
			t.Fatalf("cancelled: %d records, err %v; want an error", len(got), err)
		}
	})
}
