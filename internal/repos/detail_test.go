package repos_test

import (
	"context"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/brendatamaar/yggdrasil/internal/apperr"
	"github.com/brendatamaar/yggdrasil/internal/core"
	"github.com/brendatamaar/yggdrasil/internal/db"
	"github.com/brendatamaar/yggdrasil/internal/repos"
)

type fakeDetailRepos map[core.RepoID]string

func (f fakeDetailRepos) GetRepo(_ context.Context, id core.RepoID) (core.Repo, error) {
	p, ok := f[id]
	if !ok {
		return core.Repo{}, db.ErrNotFound
	}
	return core.Repo{ID: id, Path: p}, nil
}

func newDetail(t *testing.T) (*repos.DetailService, *gitFx, string) {
	t.Helper()
	f := newGitFx(t)
	dir := f.repo("shop-api")
	return repos.NewDetailService(fakeDetailRepos{"r1": dir}, f.g), f, dir
}

func wantClass(t *testing.T, err error, want apperr.Class) {
	t.Helper()
	if c, ok := apperr.ClassOf(err); !ok || c != want {
		t.Fatalf("class = %v (%v), want %v", c, err, want)
	}
}

func TestDetailUntrackedRepoIsNotFound(t *testing.T) {
	s, _, _ := newDetail(t)
	ctx := context.Background()
	_, err := s.Changes(ctx, "nope")
	wantClass(t, err, apperr.NotFound)
	_, err = s.Branches(ctx, "nope")
	wantClass(t, err, apperr.NotFound)
	_, err = s.Commit(ctx, "nope", "abcd")
	wantClass(t, err, apperr.NotFound)
}

func TestDetailChangesAndDiff(t *testing.T) {
	s, f, dir := newDetail(t)
	f.commit(dir, "a.txt", "one\n", "init")
	f.write(dir, "a.txt", "one\ntwo\n")
	f.write(dir, "new.txt", "n\n")
	f.write(dir, "staged.txt", "s\n")
	f.run(dir, "add", "staged.txt")
	ctx := context.Background()

	st, err := s.Changes(ctx, "r1")
	if err != nil || len(st.Files) != 3 {
		t.Fatalf("Changes = %+v, %v", st.Files, err)
	}
	d, err := s.Diff(ctx, "r1", "a.txt", repos.Unstaged)
	if err != nil || !strings.Contains(d.Text, "+two") || d.Truncated || d.Binary {
		t.Fatalf("unstaged diff = %+v, %v", d, err)
	}
	d, err = s.Diff(ctx, "r1", "", repos.Staged)
	if err != nil || !strings.Contains(d.Text, "staged.txt") || strings.Contains(d.Text, "a.txt") {
		t.Fatalf("staged diff = %+v, %v", d, err)
	}
}

func TestDetailDiffBinaryAndCap(t *testing.T) {
	s, f, dir := newDetail(t)
	f.commit(dir, "b.bin", "a\x00b", "init")
	f.commit(dir, "big.txt", "x\n", "big")
	if err := os.WriteFile(filepath.Join(dir, "b.bin"), []byte("c\x00d"), 0o600); err != nil {
		t.Fatal(err)
	}
	d, err := s.Diff(context.Background(), "r1", "b.bin", repos.Unstaged)
	if err != nil || !d.Binary {
		t.Fatalf("binary diff = %+v, %v", d, err)
	}
	f.write(dir, "big.txt", strings.Repeat("0123456789abcdef\n", 200_000))
	d, err = s.Diff(context.Background(), "r1", "big.txt", repos.Unstaged)
	if err != nil || !d.Truncated || len(d.Text) > repos.MaxDiffBytes || !strings.HasSuffix(d.Text, "\n") {
		t.Fatalf("big diff: truncated=%v len=%d err=%v", d.Truncated, len(d.Text), err)
	}
}

func TestDetailLogCommitBranches(t *testing.T) {
	s, f, dir := newDetail(t)
	ctx := context.Background()

	cs, err := s.Log(ctx, "r1", 10, "")
	if err != nil || cs == nil || len(cs) != 0 {
		t.Fatalf("empty repo log = %v, %v (want empty non-nil list)", cs, err)
	}
	f.commit(dir, "a.txt", "one\n", "first")
	f.commit(dir, "a.txt", "one\ntwo\n", "second")
	f.run(dir, "branch", "topic")

	cs, err = s.Log(ctx, "r1", 1, "HEAD")
	if err != nil || len(cs) != 1 || cs[0].Subject != "second" {
		t.Fatalf("log = %+v, %v", cs, err)
	}
	cs, err = s.Log(ctx, "r1", 1, "topic")
	if err != nil || len(cs) != 1 || cs[0].Subject != "second" {
		t.Fatalf("log of topic = %+v, %v", cs, err)
	}
	_, err = s.Log(ctx, "r1", 1, "no-such-branch")
	wantClass(t, err, apperr.NotFound)

	root, err := s.Commit(ctx, "r1", repos.OID(cs[0].Parents[0]))
	if err != nil || root.Detail.Subject != "first" || len(root.Detail.Files) != 1 || !strings.Contains(root.Diff.Text, "+one") {
		t.Fatalf("root commit = %+v, %v", root, err)
	}
	tip, err := s.Commit(ctx, "r1", repos.OID(cs[0].Short))
	if err != nil || !strings.Contains(tip.Diff.Text, "+two") || strings.Contains(tip.Diff.Text, "+one\n") {
		t.Fatalf("tip commit = %+v, %v", tip, err)
	}
	_, err = s.Commit(ctx, "r1", "deadbeef")
	wantClass(t, err, apperr.NotFound)

	bs, err := s.Branches(ctx, "r1")
	if err != nil || len(bs) != 2 {
		t.Fatalf("branches = %+v, %v", bs, err)
	}
	cur := 0
	for _, b := range bs {
		if b.Current {
			cur++
		}
	}
	if cur != 1 {
		t.Fatalf("current branches = %d", cur)
	}
}
