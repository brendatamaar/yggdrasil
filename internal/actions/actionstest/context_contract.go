package actionstest

import (
	"context"
	"errors"
	"strings"
	"testing"

	"github.com/brendatamaar/yggdrasil/internal/actions"
	"github.com/brendatamaar/yggdrasil/internal/core"
	coreactions "github.com/brendatamaar/yggdrasil/internal/core/actions"
	corehosts "github.com/brendatamaar/yggdrasil/internal/core/hosts"
	"github.com/brendatamaar/yggdrasil/internal/db"
)

// SecretToken is a fictional access token the rigs plant in build logs and in
// changed files; no output of a ContextReader may contain it.
const SecretToken = "ghp_abcdefghijklmnopqrstuvwxyz0123456789"

// ContextRig is the subject of ContextReaderContract.
type ContextRig struct {
	Reader actions.ContextReader
	// Repo is a tracked READY git repo with a clean tree and one commit.
	Repo core.RepoID
	// Head is the repo's current commit.
	Head func(t *testing.T) string
	// Edit changes the tracked file README.md and adds the untracked file
	// notes.txt, both with content that includes SecretToken and name; it returns
	// the changed paths.
	Edit func(t *testing.T, name string) []string
	// Build stores a finished failed build of repo with a log of this size, ending
	// in the line "LAST-LINE", and containing SecretToken near its start.
	Build func(t *testing.T, repo core.RepoID, logBytes int) core.BuildID
	// OtherRepo is another tracked repo (to hold a build that is not Repo's).
	OtherRepo core.RepoID
	// Untrack stops tracking Repo.
	Untrack func(t *testing.T)
	// NoTokenKey is a host context key whose host has no token configured.
	NoTokenKey corehosts.ContextKey
	// Known maps a clause name to a production defect it exposes in this
	// implementation; the clause is skipped with a KNOWN DEFECT message.
	Known map[string]string
}

// ContextReaderContract states what every actions.ContextReader promises:
//
//   - BuildLog is bounded by MaxContextBytes (the tail is kept and Truncated
//     says so), redacted, and only for a build of that tracked repo;
//   - Diff names the exact changed paths, pins the current HEAD and a hash that
//     changes when the changes change, is bounded and redacted;
//   - an untracked repo is db.ErrNotFound (invariant 6);
//   - a host without a token is a labelled blocked outcome, not an error;
//   - a cancelled context returns an error.
func ContextReaderContract(t *testing.T, mk func(t *testing.T) ContextRig) {
	ctx := context.Background()

	t.Run("build log is redacted and complete when small", func(t *testing.T) {
		r := mk(t)
		id := r.Build(t, r.Repo, 2000)
		text, trunc, err := r.Reader.BuildLog(ctx, r.Repo, id)
		if err != nil || trunc {
			t.Fatalf("err %v truncated %v", err, trunc)
		}
		if !strings.Contains(text, "LAST-LINE") || strings.Contains(text, SecretToken) {
			t.Fatalf("log = %.200q: want the content with the token redacted", text)
		}
	})

	t.Run("build log is bounded and keeps the tail", func(t *testing.T) {
		r := mk(t)
		id := r.Build(t, r.Repo, 3*coreactions.MaxContextBytes)
		text, trunc, err := r.Reader.BuildLog(ctx, r.Repo, id)
		if err != nil || !trunc {
			t.Fatalf("err %v truncated %v, want truncated", err, trunc)
		}
		if len(text) > coreactions.MaxContextBytes || !strings.Contains(text, "LAST-LINE") || strings.Contains(text, SecretToken) {
			t.Fatalf("log of %d bytes: bound, tail or redaction broken", len(text))
		}
	})

	t.Run("build log of another repo or an unknown build", func(t *testing.T) {
		r := mk(t)
		other := r.Build(t, r.OtherRepo, 100)
		if _, _, err := r.Reader.BuildLog(ctx, r.Repo, other); !errors.Is(err, db.ErrNotFound) {
			t.Fatalf("a build of another repo: %v, want ErrNotFound", err)
		}
		if _, _, err := r.Reader.BuildLog(ctx, r.Repo, "bld_0000000000000000"); !errors.Is(err, db.ErrNotFound) {
			t.Fatalf("an unknown build: %v, want ErrNotFound", err)
		}
	})

	t.Run("diff of a clean repo", func(t *testing.T) {
		r := mk(t)
		skipKnown(t, r.Known, "diff of a clean repo")
		d, err := r.Reader.Diff(ctx, r.Repo)
		if err != nil || len(d.Paths) != 0 || d.Text != "" || d.Truncated || d.Head != r.Head(t) {
			t.Fatalf("diff = %+v, %v; want no paths, no text and the current head", d, err)
		}
	})

	t.Run("diff pins the exact changes", func(t *testing.T) {
		r := mk(t)
		paths := r.Edit(t, "one")
		d, err := r.Reader.Diff(ctx, r.Repo)
		if err != nil {
			t.Fatal(err)
		}
		if d.Head != r.Head(t) || d.DiffHash == "" || len(d.Text) > coreactions.MaxContextBytes {
			t.Fatalf("diff = %+v", d)
		}
		got := map[string]bool{}
		for _, p := range d.Paths {
			got[p] = true
		}
		for _, p := range paths {
			if !got[p] {
				t.Errorf("path %q is missing from %v", p, d.Paths)
			}
		}
		if len(got) != len(paths) {
			t.Errorf("paths = %v, want exactly %v", d.Paths, paths)
		}
		if strings.Contains(d.Text, SecretToken) {
			t.Fatalf("the diff text carries the token")
		}
		again, err := r.Reader.Diff(ctx, r.Repo)
		if err != nil || again.DiffHash != d.DiffHash {
			t.Fatalf("the hash is not stable: %q then %q (%v)", d.DiffHash, again.DiffHash, err)
		}
		r.Edit(t, "two")
		changed, err := r.Reader.Diff(ctx, r.Repo)
		if err != nil || changed.DiffHash == d.DiffHash {
			t.Fatalf("the hash did not change after another edit (%v): a stale diff would be confirmed", err)
		}
	})

	t.Run("untracked repo is not found", func(t *testing.T) {
		r := mk(t)
		id := r.Build(t, r.Repo, 100)
		r.Untrack(t)
		if _, err := r.Reader.Diff(ctx, r.Repo); !errors.Is(err, db.ErrNotFound) {
			t.Errorf("Diff = %v, want ErrNotFound", err)
		}
		if _, _, err := r.Reader.BuildLog(ctx, r.Repo, id); !errors.Is(err, db.ErrNotFound) {
			t.Errorf("BuildLog = %v, want ErrNotFound", err)
		}
	})

	t.Run("host without a token is a blocked outcome", func(t *testing.T) {
		r := mk(t)
		out, err := r.Reader.Host(ctx, r.NoTokenKey)
		if err != nil || out.Blocked == nil {
			t.Fatalf("outcome = %+v, %v; want a blocked outcome and no error", out, err)
		}
	})

	t.Run("cancelled context", func(t *testing.T) {
		r := mk(t)
		cctx, cancel := context.WithCancel(ctx)
		cancel()
		if d, err := r.Reader.Diff(cctx, r.Repo); err == nil {
			t.Fatalf("Diff with a cancelled context returned %+v and no error", d)
		}
	})
}
