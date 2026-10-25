package repos_test

import (
	"context"
	"sync"
	"testing"

	corerepos "github.com/brendatamaar/yggdrasil/internal/core/repos"
	"github.com/brendatamaar/yggdrasil/internal/git"
	"github.com/brendatamaar/yggdrasil/internal/repos"
)

// scriptedGit answers Status with the current oid and Log with a commit for
// that oid. It counts the Log calls.
type scriptedGit struct {
	mu   sync.Mutex
	oid  string
	logs int
	// head, when set, is returned by HeadCommit (the direct read).
	head   func(dir, oid string) (git.Commit, bool)
	logOID string // what Log returns as hash when not empty
}

func (s *scriptedGit) Status(context.Context, string) (git.Status, error) {
	s.mu.Lock()
	defer s.mu.Unlock()
	return git.Status{Branch: "main", OID: s.oid}, nil
}

func (s *scriptedGit) Log(context.Context, string, int) ([]git.Commit, error) {
	s.mu.Lock()
	defer s.mu.Unlock()
	s.logs++
	h := s.oid
	if s.logOID != "" {
		h = s.logOID
	}
	return []git.Commit{{Hash: h, Subject: "subject " + h, Author: "Ada"}}, nil
}

func (s *scriptedGit) HeadCommit(dir, oid string) (git.Commit, bool) {
	if s.head == nil {
		return git.Commit{}, false
	}
	return s.head(dir, oid)
}

func (s *scriptedGit) setOID(o string) { s.mu.Lock(); s.oid = o; s.mu.Unlock() }
func (s *scriptedGit) logCalls() int   { s.mu.Lock(); defer s.mu.Unlock(); return s.logs }

func cachePath(t *testing.T) corerepos.CleanPath {
	t.Helper()
	p, err := corerepos.NewCleanPath(t.TempDir())
	if err != nil {
		t.Fatal(err)
	}
	return p
}

func readLast(t *testing.T, r *repos.GitReader, p corerepos.CleanPath) string {
	t.Helper()
	snap, _, err := r.Read(context.Background(), p)
	if err != nil {
		t.Fatal(err)
	}
	if snap.LastCommit == nil {
		t.Fatal("no last commit")
	}
	return snap.LastCommit.Subject
}

func TestGitReader_CommitCache_HitAndMiss(t *testing.T) {
	sg := &scriptedGit{oid: "aaa"}
	r := repos.NewGitReader(sg)
	p := cachePath(t)

	if got := readLast(t, r, p); got != "subject aaa" {
		t.Fatalf("first read = %q", got)
	}
	if got := readLast(t, r, p); got != "subject aaa" {
		t.Fatalf("second read = %q", got)
	}
	if n := sg.logCalls(); n != 1 {
		t.Errorf("git log ran %d times for an unchanged HEAD, want 1", n)
	}

	sg.setOID("bbb") // a new commit: the cache must not answer for the old oid
	if got := readLast(t, r, p); got != "subject bbb" {
		t.Errorf("after HEAD moved = %q, want subject bbb", got)
	}
	if n := sg.logCalls(); n != 2 {
		t.Errorf("git log ran %d times after HEAD moved, want 2", n)
	}

	sg.setOID("aaa") // back to an older oid: still correct
	if got := readLast(t, r, p); got != "subject aaa" {
		t.Errorf("after HEAD moved back = %q", got)
	}
}

func TestGitReader_CommitCache_PerFolder(t *testing.T) {
	sg := &scriptedGit{oid: "aaa"}
	r := repos.NewGitReader(sg)
	p1, p2 := cachePath(t), cachePath(t)
	readLast(t, r, p1)
	readLast(t, r, p2)
	if n := sg.logCalls(); n != 2 {
		t.Errorf("two folders with the same oid made %d log calls, want 2", n)
	}
}

func TestGitReader_CommitCache_RetainDropsRemovedRepos(t *testing.T) {
	sg := &scriptedGit{oid: "aaa"}
	r := repos.NewGitReader(sg)
	keep, drop := cachePath(t), cachePath(t)
	readLast(t, r, keep)
	readLast(t, r, drop)

	r.Retain([]corerepos.CleanPath{keep})
	readLast(t, r, keep)
	if n := sg.logCalls(); n != 2 {
		t.Errorf("a kept repo re-ran git log: %d calls, want 2", n)
	}
	readLast(t, r, drop)
	if n := sg.logCalls(); n != 3 {
		t.Errorf("a removed repo was answered from the cache: %d calls, want 3", n)
	}
}

func TestGitReader_CommitCache_NotFilledByAMovedHead(t *testing.T) {
	// Status says aaa, but by the time git log runs HEAD is bbb: the answer
	// is shown but must not be cached under aaa.
	sg := &scriptedGit{oid: "aaa", logOID: "bbb"}
	r := repos.NewGitReader(sg)
	p := cachePath(t)
	readLast(t, r, p)
	readLast(t, r, p)
	if n := sg.logCalls(); n != 2 {
		t.Errorf("log ran %d times, want 2 (a mismatching answer is not cached)", n)
	}
}

func TestGitReader_DirectRead_SkipsLogAndFallsBack(t *testing.T) {
	sg := &scriptedGit{oid: "aaa"}
	sg.head = func(_, oid string) (git.Commit, bool) {
		return git.Commit{Hash: oid, Subject: "direct " + oid, Author: "Ada"}, true
	}
	r := repos.NewGitReader(sg)
	p := cachePath(t)
	if got := readLast(t, r, p); got != "direct aaa" {
		t.Errorf("read = %q, want the direct answer", got)
	}
	if n := sg.logCalls(); n != 0 {
		t.Errorf("git log ran %d times although the direct read answered", n)
	}

	sg2 := &scriptedGit{oid: "ccc"}
	sg2.head = func(string, string) (git.Commit, bool) { return git.Commit{}, false }
	r2 := repos.NewGitReader(sg2)
	if got := readLast(t, r2, cachePath(t)); got != "subject ccc" {
		t.Errorf("fallback read = %q", got)
	}
	if n := sg2.logCalls(); n != 1 {
		t.Errorf("fallback ran git log %d times, want 1", n)
	}
}
