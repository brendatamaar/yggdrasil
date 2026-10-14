package main

import (
	"errors"
	"path/filepath"
	"testing"
	"time"

	"github.com/go-git/go-git/v5"
	"github.com/go-git/go-git/v5/plumbing"
)

// The builder keeps objects and refs in memory and writes them in one go;
// these tests pin that, since per-file writes made generation take minutes
// on Windows.

func TestBuilder_WritesOnFlushOnly(t *testing.T) {
	root, err := prepareOut(filepath.Join(t.TempDir(), outName))
	if err != nil {
		t.Fatal(err)
	}
	dir := filepath.Join(root, "repo")
	b, err := newBuilder(dir, filepath.Join(root, "repo.git"), time.Date(2026, 1, 1, 0, 0, 0, 0, time.UTC))
	if err != nil {
		t.Fatal(err)
	}
	defer b.close()
	b.commit("main", ada, "chore: first", f("a.txt", "a\n"))
	b.push("main")
	b.commit("main", ada, "chore: second", f("b.txt", "b\n"))
	if b.err != nil {
		t.Fatal(b.err)
	}
	if _, err := b.local.Reference(plumbing.NewBranchReferenceName("main"), true); !errors.Is(err, plumbing.ErrReferenceNotFound) {
		t.Fatalf("main written before flush: %v", err)
	}

	b.checkout("main")
	if b.err != nil {
		t.Fatal(b.err)
	}
	head, err := b.local.Head()
	if err != nil {
		t.Fatal(err)
	}
	if head.Hash() != b.localRefs[plumbing.NewBranchReferenceName("main")] {
		t.Errorf("HEAD %s, want %s", head.Hash(), b.localRefs[plumbing.NewBranchReferenceName("main")])
	}
	cfg, err := b.local.Config()
	if err != nil {
		t.Fatal(err)
	}
	if br := cfg.Branches["main"]; br == nil || br.Remote != "origin" {
		t.Errorf("main has no upstream: %+v", br)
	}
	wt, err := b.local.Worktree()
	if err != nil {
		t.Fatal(err)
	}
	if st, err := wt.Status(); err != nil || !st.IsClean() {
		t.Errorf("worktree not clean after checkout (%v):\n%s", err, st)
	}
	b.close()

	// The packfile and refs are readable by a fresh, ordinary open.
	r, err := git.PlainOpen(dir)
	if err != nil {
		t.Fatal(err)
	}
	s, err := summarize("repo", r)
	if err != nil {
		t.Fatal(err)
	}
	if s.commits != 2 || len(s.branches) != 1 || s.branches[0].state != "ahead of origin/main by 1" {
		t.Errorf("summary %+v", s)
	}
}

func TestBuilder_FirstErrorSticks(t *testing.T) {
	root, err := prepareOut(filepath.Join(t.TempDir(), outName))
	if err != nil {
		t.Fatal(err)
	}
	b, err := newBuilder(filepath.Join(root, "repo"), filepath.Join(root, "repo.git"), time.Date(2026, 1, 1, 0, 0, 0, 0, time.UTC))
	if err != nil {
		t.Fatal(err)
	}
	defer b.close()
	b.commit("main", ada, "chore: first", f("a.txt", "a\n"))
	b.branch("feature/x", "no-such-branch")
	first := b.err
	b.push("also-missing")
	if first == nil || b.err != first {
		t.Fatalf("err = %v, want the first error %v", b.err, first)
	}
}
