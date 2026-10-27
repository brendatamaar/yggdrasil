// Package gittest holds an in-memory git.Writer and the contract suite that
// both the fake and real git must pass.
package gittest

import (
	"context"
	"crypto/sha256"
	"encoding/hex"
	"fmt"
	"sort"
	"strings"
	"sync"

	"github.com/brendatamaar/yggdrasil/internal/git"
)

type fakeCommit struct {
	parent, msg string
}

type fakeRepo struct {
	branch   string
	refs     map[string]string // branch -> tip
	commits  map[string]fakeCommit
	files    map[string]string // committed content
	work     map[string]string // working content (a path absent here is unchanged)
	staged   map[string]bool
	remotes  map[string]string // name -> url
	def      string
	noAuthor bool
}

type fakeRemote struct {
	refs    map[string]string
	commits map[string]fakeCommit
}

// Fake is an in-memory git.Writer. It keeps refs, commits and a flat file
// tree; it has no hooks, so the "hooks do not run" clause is trivially true.
// Ops records every call as "op args" for tests.
type Fake struct {
	mu      sync.Mutex
	repos   map[string]*fakeRepo
	remotes map[string]*fakeRemote // by url
	seq     int
	ops     []string
	// Fail, when set, is returned by the named operation ("commit", "push",
	// "ls-remote", "add").
	Fail map[string]error
}

var _ git.Writer = (*Fake)(nil)

// NewFake returns an empty fake.
func NewFake() *Fake {
	return &Fake{repos: map[string]*fakeRepo{}, remotes: map[string]*fakeRemote{}, Fail: map[string]error{}}
}

// Ops returns the recorded calls.
func (f *Fake) Ops() []string {
	f.mu.Lock()
	defer f.mu.Unlock()
	return append([]string(nil), f.ops...)
}

func (f *Fake) newID() string {
	f.seq++
	h := sha256.Sum256([]byte(fmt.Sprintf("commit-%d", f.seq)))
	return hex.EncodeToString(h[:])[:40]
}

// AddRepo makes a repo at dir on branch with one root commit, and returns that
// commit's ID. The default branch is "main".
func (f *Fake) AddRepo(dir, branch string) string {
	f.mu.Lock()
	defer f.mu.Unlock()
	id := f.newID()
	f.repos[dir] = &fakeRepo{
		branch: branch, refs: map[string]string{branch: id}, commits: map[string]fakeCommit{id: {msg: "init"}},
		files: map[string]string{}, work: map[string]string{}, staged: map[string]bool{},
		remotes: map[string]string{}, def: "main",
	}
	return id
}

// AddRemote registers a remote name -> url for dir; the remote store is shared
// by every repo that uses the same url.
func (f *Fake) AddRemote(dir, name, url string) {
	f.mu.Lock()
	defer f.mu.Unlock()
	f.repos[dir].remotes[name] = url
	if _, ok := f.remotes[url]; !ok {
		f.remotes[url] = &fakeRemote{refs: map[string]string{}, commits: map[string]fakeCommit{}}
	}
}

// SetBranch moves the checked-out branch, creating it at the current tip.
func (f *Fake) SetBranch(dir, branch string) {
	f.mu.Lock()
	defer f.mu.Unlock()
	r := f.repos[dir]
	if _, ok := r.refs[branch]; !ok {
		r.refs[branch] = r.refs[r.branch]
	}
	r.branch = branch
}

// SetDefaultBranch sets the default branch the fake reports.
func (f *Fake) SetDefaultBranch(dir, b string) {
	f.mu.Lock()
	defer f.mu.Unlock()
	f.repos[dir].def = b
}

// Write changes a working file.
func (f *Fake) Write(dir, rel, content string) {
	f.mu.Lock()
	defer f.mu.Unlock()
	r := f.repos[dir]
	if r.files[rel] == content {
		delete(r.work, rel)
		return
	}
	r.work[rel] = content
}

// SetNoAuthor makes Commit fail as git does when the user is unknown.
func (f *Fake) SetNoAuthor(dir string, v bool) {
	f.mu.Lock()
	defer f.mu.Unlock()
	f.repos[dir].noAuthor = v
}

// RemoteTip returns the remote's tip of branch for the remote url ("" if none).
func (f *Fake) RemoteTip(url, branch string) string {
	f.mu.Lock()
	defer f.mu.Unlock()
	if r, ok := f.remotes[url]; ok {
		return r.refs[branch]
	}
	return ""
}

// AdvanceRemote adds a commit on the remote's branch that the local repo does
// not have, so a later push is not a fast-forward.
func (f *Fake) AdvanceRemote(url, branch string) {
	f.mu.Lock()
	defer f.mu.Unlock()
	r := f.remotes[url]
	id := f.newID()
	r.commits[id] = fakeCommit{parent: r.refs[branch], msg: "elsewhere"}
	r.refs[branch] = id
}

// SetRemoteURL changes the URL of a remote of dir (it may carry credentials).
func (f *Fake) SetRemoteURL(dir, name, url string) {
	f.AddRemote(dir, name, url)
}

func (f *Fake) repo(dir, op string) (*fakeRepo, error) {
	r, ok := f.repos[dir]
	if !ok {
		return nil, fmt.Errorf("fake git %s: no repo at %q", op, dir)
	}
	return r, nil
}

func (f *Fake) begin(op, dir string, args ...string) (*fakeRepo, error) {
	f.ops = append(f.ops, strings.TrimSpace(op+" "+strings.Join(args, " ")))
	if err := f.Fail[op]; err != nil {
		return nil, err
	}
	return f.repo(dir, op)
}

func under(path string, paths []string) bool {
	for _, p := range paths {
		if path == p || strings.HasPrefix(path, strings.TrimSuffix(p, "/")+"/") {
			return true
		}
	}
	return false
}

// Head implements git.Writer.
func (f *Fake) Head(_ context.Context, dir string) (string, error) {
	f.mu.Lock()
	defer f.mu.Unlock()
	r, err := f.begin("head", dir)
	if err != nil {
		return "", err
	}
	return r.refs[r.branch], nil
}

// HeadInfo implements git.Writer.
func (f *Fake) HeadInfo(_ context.Context, dir string) (git.CommitInfo, error) {
	f.mu.Lock()
	defer f.mu.Unlock()
	r, err := f.begin("head-info", dir)
	if err != nil {
		return git.CommitInfo{}, err
	}
	id := r.refs[r.branch]
	c := r.commits[id]
	return git.CommitInfo{ID: id, Parent: c.parent, Message: c.msg}, nil
}

// DiffHash implements git.Writer.
func (f *Fake) DiffHash(_ context.Context, dir string, paths []string) (string, error) {
	f.mu.Lock()
	defer f.mu.Unlock()
	r, err := f.begin("diff-hash", dir, paths...)
	if err != nil {
		return "", err
	}
	var keys []string
	for p := range r.work {
		if under(p, paths) {
			keys = append(keys, p)
		}
	}
	sort.Strings(keys)
	h := sha256.New()
	for _, k := range keys {
		fmt.Fprintf(h, "%d:%s\x00%d:%s\x00", len(k), k, len(r.work[k]), r.work[k])
	}
	return hex.EncodeToString(h.Sum(nil)), nil
}

// Add implements git.Writer.
func (f *Fake) Add(_ context.Context, dir string, paths []string) error {
	f.mu.Lock()
	defer f.mu.Unlock()
	r, err := f.begin("add", dir, paths...)
	if err != nil {
		return err
	}
	for p := range r.work {
		if under(p, paths) {
			r.staged[p] = true
		}
	}
	return nil
}

// Commit implements git.Writer.
func (f *Fake) Commit(_ context.Context, dir string, paths []string, message, expectHead string) (string, error) {
	f.mu.Lock()
	defer f.mu.Unlock()
	r, err := f.begin("commit", dir, paths...)
	if err != nil {
		return "", err
	}
	if expectHead != "" && r.refs[r.branch] != expectHead {
		return "", &git.Error{Op: "commit", Err: git.ErrStaleHead, UserMsg: "The branch has changed."}
	}
	if r.noAuthor {
		return "", &git.Error{Op: "commit", Err: git.ErrNoAuthor, UserMsg: "Git does not know who you are."}
	}
	var picked []string
	for p := range r.staged {
		if under(p, paths) {
			picked = append(picked, p)
		}
	}
	if len(picked) == 0 {
		return "", &git.Error{Op: "commit", Err: git.ErrNothingToCommit, UserMsg: "There is nothing to commit."}
	}
	for _, p := range picked {
		r.files[p] = r.work[p]
		delete(r.work, p)
		delete(r.staged, p)
	}
	id := f.newID()
	r.commits[id] = fakeCommit{parent: r.refs[r.branch], msg: strings.TrimSpace(message)}
	r.refs[r.branch] = id
	return id, nil
}

func (r *fakeRepo) ancestor(anc, of string, remote *fakeRemote) bool {
	for id := of; id != ""; {
		if id == anc {
			return true
		}
		c, ok := r.commits[id]
		if !ok && remote != nil {
			c, ok = remote.commits[id]
		}
		if !ok {
			return false
		}
		id = c.parent
	}
	return anc == ""
}

// Push implements git.Writer.
func (f *Fake) Push(_ context.Context, dir string, req git.PushRequest) (git.PushResult, error) {
	f.mu.Lock()
	defer f.mu.Unlock()
	r, err := f.begin("push", dir, req.Remote, req.Branch)
	if err != nil {
		return git.PushResult{}, err
	}
	url, ok := r.remotes[req.Remote]
	if !ok {
		return git.PushResult{}, &git.Error{Op: "push", Err: git.ErrNoRemote, UserMsg: "No such remote."}
	}
	if err := git.CheckRemoteURL(url); err != nil {
		return git.PushResult{}, &git.Error{Op: "push", Err: git.ErrUnsafeRemote, UserMsg: "Unsafe remote."}
	}
	if req.Branch == r.def {
		return git.PushResult{}, &git.Error{Op: "push", Err: git.ErrDefaultBranch, UserMsg: "Default branch."}
	}
	tip, ok := r.refs[req.Branch]
	if !ok {
		return git.PushResult{}, &git.Error{Op: "push", Err: git.ErrInvalidArg, UserMsg: "No such branch."}
	}
	if req.ExpectHead != "" && tip != req.ExpectHead {
		return git.PushResult{}, &git.Error{Op: "push", Err: git.ErrStaleHead, UserMsg: "The branch has changed."}
	}
	rem := f.remotes[url]
	if cur := rem.refs[req.Branch]; cur != "" && !r.ancestor(cur, tip, rem) {
		return git.PushResult{}, &git.PushError{Kind: git.PushNonFastForward, Reason: "[rejected] (non-fast-forward)", Msg: "The remote has newer commits."}
	}
	for id, c := range r.commits {
		rem.commits[id] = c
	}
	rem.refs[req.Branch] = tip
	out := "To " + "remote\n"
	if req.MR != nil {
		out += "remote: View merge request for " + req.Branch + ":\nremote:   https://fake.example.test/mr/1\n"
	}
	return git.PushResult{Output: out}, nil
}

// LsRemote implements git.Writer.
func (f *Fake) LsRemote(_ context.Context, dir, remote, branch string) (string, error) {
	f.mu.Lock()
	defer f.mu.Unlock()
	r, err := f.begin("ls-remote", dir, remote, branch)
	if err != nil {
		return "", err
	}
	url, ok := r.remotes[remote]
	if !ok {
		return "", &git.Error{Op: "ls-remote", Err: git.ErrNoRemote, UserMsg: "No such remote."}
	}
	if err := git.CheckRemoteURL(url); err != nil {
		return "", &git.Error{Op: "ls-remote", Err: git.ErrUnsafeRemote, UserMsg: "Unsafe remote."}
	}
	return f.remotes[url].refs[branch], nil
}

// RemoteURL implements git.Writer.
func (f *Fake) RemoteURL(_ context.Context, dir, name string) (string, error) {
	f.mu.Lock()
	defer f.mu.Unlock()
	r, err := f.begin("remote-url", dir, name)
	if err != nil {
		return "", err
	}
	u, ok := r.remotes[name]
	if !ok {
		return "", &git.Error{Op: "remote url", Err: git.ErrNoRemote, UserMsg: "No such remote."}
	}
	return u, nil
}
