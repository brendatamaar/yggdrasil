// Package actionstest holds the contract suites of the consumer-local ports of
// internal/actions that have a fake (ScopeReader, ContextReader), and a fake of
// each. A suite runs against the fake here and against the real adapter in
// cmd/ygg (docs/AGENTS.md 5.11), so the fake cannot drift from the adapter.
package actionstest

import (
	"context"
	"errors"
	"strings"
	"testing"

	"github.com/brendatamaar/yggdrasil/internal/actions"
	"github.com/brendatamaar/yggdrasil/internal/core"
	coreactions "github.com/brendatamaar/yggdrasil/internal/core/actions"
	"github.com/brendatamaar/yggdrasil/internal/db"
)

// Tracked is a tracked, READY repo and its project.
type Tracked struct {
	ID      core.RepoID
	Project core.ProjectID
}

// ScopeRig is the subject of ScopeReaderContract: a reader and the means to put
// the repos it reads into known states. Every mutator leaves the reader seeing
// the change (the real rig refreshes the repo status itself).
type ScopeRig struct {
	Reader actions.ScopeReader
	// Repo makes a tracked, READY, readable repo; withUpstream gives it a
	// checked-out branch with an upstream. A new call makes a new project.
	Repo func(t *testing.T, withUpstream bool) Tracked
	// Sibling makes a second tracked READY repo in the project.
	Sibling func(t *testing.T, project core.ProjectID) core.RepoID
	// Reserved makes a reserved (not yet ready) repo in the project.
	Reserved func(t *testing.T, project core.ProjectID) core.RepoID
	// Untrack stops tracking the repo; Dirty adds an untracked file; Break makes
	// the repo's folder unreadable.
	Untrack func(t *testing.T, repo core.RepoID)
	Dirty   func(t *testing.T, repo core.RepoID)
	Break   func(t *testing.T, repo core.RepoID)
}

const unknownRepo = core.RepoID("rep_0000000000000000")

func has(rs []coreactions.Reason, r coreactions.Reason) bool {
	for _, x := range rs {
		if x == r {
			return true
		}
	}
	return false
}

func isHexOID(s string) bool {
	if len(s) != 40 && len(s) != 64 {
		return false
	}
	return strings.Trim(s, "0123456789abcdef") == ""
}

// ScopeReaderContract states what every actions.ScopeReader promises:
//
//   - a tracked healthy repo has facts with a full HEAD and no reasons;
//   - a dirty repo gains the dirty reason and a count; a broken folder is
//     unreadable with no HEAD pin, never a stale one;
//   - an unknown, untracked or reserved repo is db.ErrNotFound on every method
//     (invariant 6: only tracked repos are visible);
//   - StillTracked turns to db.ErrNotFound after an untrack;
//   - ReposOf lists exactly the tracked READY repos of a known project;
//   - PullTarget pins the branch, its upstream and the same HEAD as Facts, and
//     refuses a repo without an upstream;
//   - a cancelled context returns an error and no data.
func ScopeReaderContract(t *testing.T, mk func(t *testing.T) ScopeRig) {
	ctx := context.Background()

	t.Run("healthy repo facts", func(t *testing.T) {
		r := mk(t)
		repo := r.Repo(t, true)
		f, err := r.Reader.Facts(ctx, repo.ID)
		if err != nil {
			t.Fatal(err)
		}
		if f.Repo != repo.ID || !f.Readable || !isHexOID(f.Head) || len(f.Reasons) != 0 {
			t.Fatalf("facts = %+v, want readable, a full head and no reasons", f)
		}
		if err := r.Reader.StillTracked(ctx, repo.ID); err != nil {
			t.Fatalf("StillTracked: %v", err)
		}
		if _, err := r.Reader.NoiseFacts(ctx, repo.ID); err != nil {
			t.Fatalf("NoiseFacts: %v", err)
		}
		tg, err := r.Reader.Target(ctx, repo.ID)
		if err != nil || tg.Project != repo.Project || tg.HasHost {
			t.Fatalf("Target = %+v, %v; want project %s and no host", tg, err, repo.Project)
		}
	})

	t.Run("dirty repo", func(t *testing.T) {
		r := mk(t)
		repo := r.Repo(t, true)
		before, _ := r.Reader.Facts(ctx, repo.ID)
		r.Dirty(t, repo.ID)
		f, err := r.Reader.Facts(ctx, repo.ID)
		if err != nil || !has(f.Reasons, coreactions.ReasonDirty) || f.UntrackedCount < 1 || !f.Readable {
			t.Fatalf("facts = %+v, %v; want the dirty reason and an untracked count", f, err)
		}
		if f.Head != before.Head {
			t.Fatalf("an untracked file moved the head pin: %q -> %q", before.Head, f.Head)
		}
	})

	t.Run("unreadable folder has no pin", func(t *testing.T) {
		r := mk(t)
		repo := r.Repo(t, true)
		r.Break(t, repo.ID)
		f, err := r.Reader.Facts(ctx, repo.ID)
		if err != nil {
			t.Fatal(err)
		}
		if f.Readable || f.Head != "" || !has(f.Reasons, coreactions.ReasonUnreadable) {
			t.Fatalf("facts = %+v, want unreadable with no head pin", f)
		}
		if err := r.Reader.StillTracked(ctx, repo.ID); err != nil {
			t.Fatalf("an unreadable repo is still tracked: %v", err)
		}
	})

	t.Run("not visible: unknown, untracked, reserved", func(t *testing.T) {
		r := mk(t)
		repo := r.Repo(t, true)
		reserved := r.Reserved(t, repo.Project)
		gone := r.Sibling(t, repo.Project)
		r.Untrack(t, gone)
		for name, id := range map[string]core.RepoID{"unknown": unknownRepo, "reserved": reserved, "untracked": gone} {
			if _, err := r.Reader.Facts(ctx, id); !errors.Is(err, db.ErrNotFound) {
				t.Errorf("%s: Facts err = %v, want ErrNotFound", name, err)
			}
			if err := r.Reader.StillTracked(ctx, id); !errors.Is(err, db.ErrNotFound) {
				t.Errorf("%s: StillTracked err = %v, want ErrNotFound", name, err)
			}
			if _, err := r.Reader.NoiseFacts(ctx, id); !errors.Is(err, db.ErrNotFound) {
				t.Errorf("%s: NoiseFacts err = %v, want ErrNotFound", name, err)
			}
			if _, err := r.Reader.Target(ctx, id); !errors.Is(err, db.ErrNotFound) {
				t.Errorf("%s: Target err = %v, want ErrNotFound", name, err)
			}
			if _, err := r.Reader.PullTarget(ctx, id); !errors.Is(err, db.ErrNotFound) {
				t.Errorf("%s: PullTarget err = %v, want ErrNotFound", name, err)
			}
		}
		if _, err := r.Reader.ReposOf(ctx, "prj_0000000000000000"); !errors.Is(err, db.ErrNotFound) {
			t.Errorf("ReposOf of an unknown project: %v, want ErrNotFound", err)
		}
	})

	t.Run("untrack is seen at execution", func(t *testing.T) {
		r := mk(t)
		repo := r.Repo(t, true)
		if err := r.Reader.StillTracked(ctx, repo.ID); err != nil {
			t.Fatal(err)
		}
		r.Untrack(t, repo.ID)
		if err := r.Reader.StillTracked(ctx, repo.ID); !errors.Is(err, db.ErrNotFound) {
			t.Fatalf("StillTracked after untrack = %v, want ErrNotFound", err)
		}
	})

	t.Run("ReposOf lists tracked ready repos only", func(t *testing.T) {
		r := mk(t)
		a := r.Repo(t, true)
		b := r.Sibling(t, a.Project)
		_ = r.Reserved(t, a.Project)
		gone := r.Sibling(t, a.Project)
		r.Untrack(t, gone)
		got, err := r.Reader.ReposOf(ctx, a.Project)
		if err != nil {
			t.Fatal(err)
		}
		want := map[core.RepoID]bool{a.ID: true, b: true}
		if len(got) != len(want) {
			t.Fatalf("ReposOf = %v, want %v", got, want)
		}
		for _, id := range got {
			if !want[id] {
				t.Fatalf("ReposOf lists %s: %v", id, got)
			}
		}
	})

	t.Run("pull target", func(t *testing.T) {
		r := mk(t)
		repo := r.Repo(t, true)
		pt, err := r.Reader.PullTarget(ctx, repo.ID)
		if err != nil {
			t.Fatal(err)
		}
		f, _ := r.Reader.Facts(ctx, repo.ID)
		if pt.Branch == "" || !strings.Contains(pt.Upstream, "/") || pt.Head == "" || pt.Head != f.Head {
			t.Fatalf("PullTarget = %+v, facts head %q; want branch, remote/branch and the same head", pt, f.Head)
		}
		bare := r.Repo(t, false)
		if _, err := r.Reader.PullTarget(ctx, bare.ID); err == nil {
			t.Fatal("a repo with no upstream gave a pull target")
		}
	})

	t.Run("cancelled context", func(t *testing.T) {
		r := mk(t)
		repo := r.Repo(t, true)
		cctx, cancel := context.WithCancel(ctx)
		cancel()
		if f, err := r.Reader.Facts(cctx, repo.ID); err == nil {
			t.Fatalf("Facts with a cancelled context returned %+v and no error", f)
		}
		if err := r.Reader.StillTracked(cctx, repo.ID); err == nil {
			t.Fatal("StillTracked with a cancelled context returned no error")
		}
	})
}
