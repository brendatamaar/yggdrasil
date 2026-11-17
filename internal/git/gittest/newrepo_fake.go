package gittest

import (
	"context"
	"crypto/sha1" //nolint:gosec // fake object IDs mimic git's 40 hex digits; not a security use
	"encoding/hex"
	"errors"
	"fmt"
	"path/filepath"
	"sort"
	"strings"
	"sync"

	"github.com/brendatamaar/yggdrasil/internal/git"
)

const fakeAuthor = "Ada Test <ada@example.test>"

type fakeDir struct {
	initialized bool
	branch      string
	files       map[string]string
	commit      *git.FirstCommit
}

// FakeRepoInitializer is an in-memory git.RepoInitializer. Directories are
// registered with AddDir; "disk" content is set with WriteFile. Faults scripts
// failures by operation: "init", "commit".
type FakeRepoInitializer struct {
	Faults Faults

	mu     sync.Mutex
	dirs   map[string]*fakeDir
	author bool
	seq    int
}

var _ git.RepoInitializer = (*FakeRepoInitializer)(nil)

// NewFakeRepoInitializer returns a fake whose user has a git author.
func NewFakeRepoInitializer() *FakeRepoInitializer {
	return &FakeRepoInitializer{dirs: map[string]*fakeDir{}, author: true}
}

// AddDir registers an existing empty directory.
func (f *FakeRepoInitializer) AddDir(dir string) {
	f.mu.Lock()
	defer f.mu.Unlock()
	f.dirs[dir] = &fakeDir{files: map[string]string{}}
}

// WriteFile puts a file in the fake directory.
func (f *FakeRepoInitializer) WriteFile(dir, rel, content string) {
	f.mu.Lock()
	defer f.mu.Unlock()
	f.dirs[dir].files[rel] = content
}

// SetAuthor sets whether git knows the user.
func (f *FakeRepoInitializer) SetAuthor(known bool) {
	f.mu.Lock()
	defer f.mu.Unlock()
	f.author = known
}

func (f *FakeRepoInitializer) InitRepo(_ context.Context, dir, branch string) error {
	if err := fakeName(branch); err != nil {
		return err
	}
	f.mu.Lock()
	defer f.mu.Unlock()
	mode, ferr, hit := f.Faults.next("init")
	if hit && mode == FailBefore {
		return ferr
	}
	d, ok := f.dirs[dir]
	if !ok {
		return fmt.Errorf("init: %w", git.ErrNotEmptyDir)
	}
	if d.initialized || len(d.files) != 0 {
		return fmt.Errorf("init: %w", git.ErrNotEmptyDir)
	}
	d.initialized, d.branch = true, branch
	if hit {
		return ferr
	}
	return nil
}

func (f *FakeRepoInitializer) HasAuthor(_ context.Context, _ string) (bool, error) {
	f.mu.Lock()
	defer f.mu.Unlock()
	return f.author, nil
}

func (f *FakeRepoInitializer) CommitFirst(_ context.Context, dir string, files []string, message string) (git.FirstCommit, error) {
	if len(files) == 0 || strings.TrimSpace(message) == "" {
		return git.FirstCommit{}, fmt.Errorf("first commit: %w", git.ErrInvalidArg)
	}
	for _, p := range files {
		if p == "" || p == "." || filepath.IsAbs(p) || strings.HasPrefix(p, "/") || strings.HasPrefix(p, "-") || strings.HasPrefix(p, ":") {
			return git.FirstCommit{}, fmt.Errorf("first commit: %w: path", git.ErrInvalidArg)
		}
		for _, seg := range strings.Split(strings.ReplaceAll(p, "\\", "/"), "/") {
			if seg == ".." || strings.EqualFold(seg, ".git") {
				return git.FirstCommit{}, fmt.Errorf("first commit: %w: path", git.ErrInvalidArg)
			}
		}
	}
	f.mu.Lock()
	defer f.mu.Unlock()
	d, ok := f.dirs[dir]
	if !ok || !d.initialized {
		return git.FirstCommit{}, errors.New("first commit: not a repository")
	}
	if !f.author {
		return git.FirstCommit{}, fmt.Errorf("first commit: %w", git.ErrNoAuthor)
	}
	mode, ferr, hit := f.Faults.next("commit")
	if hit && mode == FailBefore {
		return git.FirstCommit{}, ferr
	}
	if d.commit != nil {
		return git.FirstCommit{}, fmt.Errorf("first commit: %w", git.ErrNotFirstCommit)
	}
	listed := append([]string(nil), files...)
	sort.Strings(listed)
	h := sha1.New() //nolint:gosec // see import
	for _, p := range listed {
		c, ok := d.files[p]
		if !ok {
			return git.FirstCommit{}, fmt.Errorf("first commit: file %q does not exist", p)
		}
		fmt.Fprintf(h, "%d:%s\x00%d:%s\x00", len(p), p, len(c), c)
	}
	f.seq++
	cid := sha1.Sum([]byte(fmt.Sprintf("commit-%d", f.seq))) //nolint:gosec // see import
	fc := git.FirstCommit{
		Commit:      hex.EncodeToString(cid[:]),
		Tree:        hex.EncodeToString(h.Sum(nil)),
		MessageHash: git.MessageHash(message),
		Author:      fakeAuthor,
	}
	d.commit = &fc
	if hit {
		return git.FirstCommit{}, ferr
	}
	return fc, nil
}

func (f *FakeRepoInitializer) ReadFirstCommit(_ context.Context, dir, branch string) (git.FirstCommit, bool, error) {
	f.mu.Lock()
	defer f.mu.Unlock()
	d, ok := f.dirs[dir]
	if !ok || !d.initialized || d.commit == nil || d.branch != branch {
		return git.FirstCommit{}, false, nil
	}
	return *d.commit, true, nil
}

func fakeName(branch string) error {
	if branch == "" || strings.HasPrefix(branch, "-") || strings.Contains(branch, "..") || strings.ContainsAny(branch, " ~^:?*[\\") {
		return fmt.Errorf("init: %w: branch", git.ErrInvalidArg)
	}
	return nil
}

// FakeOriginManager is an in-memory git.OriginManager. Faults: "add-origin".
type FakeOriginManager struct {
	Faults Faults

	mu      sync.Mutex
	origins map[string]string
}

var _ git.OriginManager = (*FakeOriginManager)(nil)

// NewFakeOriginManager returns an empty fake.
func NewFakeOriginManager() *FakeOriginManager {
	return &FakeOriginManager{origins: map[string]string{}}
}

func (f *FakeOriginManager) Origin(_ context.Context, dir string) (string, bool, error) {
	f.mu.Lock()
	defer f.mu.Unlock()
	u, ok := f.origins[dir]
	return u, ok, nil
}

func (f *FakeOriginManager) AddOrigin(_ context.Context, dir, rawURL string) error {
	canon, err := git.ValidateRemoteURL(rawURL)
	if err != nil {
		return err
	}
	f.mu.Lock()
	defer f.mu.Unlock()
	mode, ferr, hit := f.Faults.next("add-origin")
	if hit && mode == FailBefore {
		return ferr
	}
	if cur, ok := f.origins[dir]; ok {
		if cur == canon {
			return nil
		}
		return fmt.Errorf("add origin: %w", git.ErrOriginExists)
	}
	f.origins[dir] = canon
	if hit {
		return ferr
	}
	return nil
}

// FakePublishTransport is an in-memory git.PublishTransport. Faults:
// "remote-branch", "publish". With LostReply on "publish" the push lands and the
// caller gets the error it scripted (use git.ErrPublishUnknown for a lost reply).
type FakePublishTransport struct {
	Faults Faults

	mu      sync.Mutex
	remotes map[string]map[string]string // url -> branch -> head
	local   map[string]map[string]string // dir -> branch -> head
	pushes  int
}

var _ git.PublishTransport = (*FakePublishTransport)(nil)

// NewFakePublishTransport returns an empty fake.
func NewFakePublishTransport() *FakePublishTransport {
	return &FakePublishTransport{remotes: map[string]map[string]string{}, local: map[string]map[string]string{}}
}

// SetLocalHead sets the head of a local branch.
func (f *FakePublishTransport) SetLocalHead(dir, branch, head string) {
	f.mu.Lock()
	defer f.mu.Unlock()
	if f.local[dir] == nil {
		f.local[dir] = map[string]string{}
	}
	f.local[dir][branch] = head
}

// SetRemoteHead sets the head of a remote branch (use it to script a diverged remote).
func (f *FakePublishTransport) SetRemoteHead(url, branch, head string) {
	f.mu.Lock()
	defer f.mu.Unlock()
	if f.remotes[url] == nil {
		f.remotes[url] = map[string]string{}
	}
	f.remotes[url][branch] = head
}

// RemoteHead returns the remote head of branch, "" when absent.
func (f *FakePublishTransport) RemoteHead(url, branch string) string {
	f.mu.Lock()
	defer f.mu.Unlock()
	return f.remotes[url][branch]
}

// Pushes is the number of pushes that reached the remote and sent objects.
func (f *FakePublishTransport) Pushes() int {
	f.mu.Lock()
	defer f.mu.Unlock()
	return f.pushes
}

func (f *FakePublishTransport) RemoteBranch(_ context.Context, req git.PublishRequest) (git.RemoteRef, error) {
	canon, err := git.ValidateRemoteURL(req.URL)
	if err != nil {
		return git.RemoteRef{}, err
	}
	f.mu.Lock()
	defer f.mu.Unlock()
	if _, ferr, hit := f.Faults.next("remote-branch"); hit {
		return git.RemoteRef{}, ferr
	}
	h, ok := f.remotes[canon][req.Branch]
	return git.RemoteRef{Exists: ok, Head: h}, nil
}

func (f *FakePublishTransport) PublishBranch(_ context.Context, req git.PublishRequest) (git.PublishResult, error) {
	canon, err := git.ValidateRemoteURL(req.URL)
	if err != nil {
		return git.PublishResult{}, err
	}
	f.mu.Lock()
	defer f.mu.Unlock()
	mode, ferr, hit := f.Faults.next("publish")
	if hit && mode == FailBefore {
		return git.PublishResult{}, ferr
	}
	if f.local[req.Dir][req.Branch] != req.ExpectedHead || req.ExpectedHead == "" {
		return git.PublishResult{}, fmt.Errorf("publish: %w", git.ErrStaleHead)
	}
	if cur, ok := f.remotes[canon][req.Branch]; ok {
		if cur == req.ExpectedHead {
			return git.PublishResult{Head: cur}, nil
		}
		return git.PublishResult{}, fmt.Errorf("publish: %w", git.ErrRemoteDiverged)
	}
	if f.remotes[canon] == nil {
		f.remotes[canon] = map[string]string{}
	}
	f.remotes[canon][req.Branch] = req.ExpectedHead
	f.pushes++
	if hit {
		return git.PublishResult{}, ferr
	}
	return git.PublishResult{Head: req.ExpectedHead}, nil
}
