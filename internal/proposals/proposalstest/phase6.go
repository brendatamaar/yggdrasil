package proposalstest

// Fakes of the three ports the Phase 6 executors call (IgnoreWriter,
// CommentHost, PullRunner), with scripted failure injection. A fault is either
// FailBefore (the effect did not happen) or FailAfter (the effect happened and
// the reply was lost). Faults are one-shot: they fire on the next call and are
// then cleared. The fakes count every call, so a test can prove that a port was
// never reached (no write before a confirmation, no second POST).
//
// The contract suites in phase6_contract.go run against these fakes. The live
// ports (the platform ignore writer, hosts.Service, the git-action service) are
// wired later; the ignore writer's own contract already runs against the real
// platform implementation in internal/platform/platformtest.

import (
	"context"
	"errors"
	"path/filepath"
	"sync"
	"testing"

	"github.com/brendatamaar/yggdrasil/internal/core"
	coreproposals "github.com/brendatamaar/yggdrasil/internal/core/proposals"
	"github.com/brendatamaar/yggdrasil/internal/platform"
	"github.com/brendatamaar/yggdrasil/internal/platform/platformtest"
)

// Fault says how the next call of a fake fails.
type Fault int

const (
	// NoFault: the call behaves normally.
	NoFault Fault = iota
	// FailBefore: the call returns the scripted error and has no effect.
	FailBefore
	// FailAfter: the effect happens, then the call returns the scripted error
	// (a lost reply).
	FailAfter
)

// IgnoreFake is an in-memory IgnoreWriter (platformtest's fake disk) that counts
// calls and injects one-shot faults.
type IgnoreFake struct {
	mu      sync.Mutex
	inner   *platformtest.FakeIgnoreWriter
	disk    *platformtest.FakeDisk
	reads   int
	appends int
	fault   Fault
	err     error
}

// NewIgnoreFake returns a fake with an empty disk.
func NewIgnoreFake() *IgnoreFake {
	d := platformtest.NewFakeDisk()
	return &IgnoreFake{inner: platformtest.NewFakeIgnoreWriter(d), disk: d}
}

// Fixture lets a contract suite arrange the disk.
func (f *IgnoreFake) Fixture() platformtest.FSFixture { return f.inner.Fixture() }

// Seed makes root a folder, with a .gitignore of content when present.
func (f *IgnoreFake) Seed(t *testing.T, root string, content []byte, present bool) {
	t.Helper()
	f.disk.Mkdir(root)
	if present {
		fx := f.inner.Fixture()
		fx.WriteFile(t, filepath.Join(root, ".gitignore"), content)
	}
}

// Script makes the next AppendIgnore fail as fault says with err.
func (f *IgnoreFake) Script(fault Fault, err error) {
	f.mu.Lock()
	f.fault, f.err = fault, err
	f.mu.Unlock()
}

// Reads and Appends count calls.
func (f *IgnoreFake) Reads() int   { f.mu.Lock(); defer f.mu.Unlock(); return f.reads }
func (f *IgnoreFake) Appends() int { f.mu.Lock(); defer f.mu.Unlock(); return f.appends }

// Calls is every call of either method: zero means the port was never reached.
func (f *IgnoreFake) Calls() int { return f.Reads() + f.Appends() }

// Content returns the file's bytes.
func (f *IgnoreFake) Content(t *testing.T, root string) (data []byte, exists bool) {
	t.Helper()
	return f.inner.Fixture().ReadFile(t, filepath.Join(root, ".gitignore"))
}

func (f *IgnoreFake) ReadIgnore(ctx context.Context, root string) (platform.IgnoreSnapshot, error) {
	f.mu.Lock()
	f.reads++
	f.mu.Unlock()
	return f.inner.ReadIgnore(ctx, root)
}

func (f *IgnoreFake) AppendIgnore(ctx context.Context, root string, entries []string, expect platform.IgnoreExpectation) (platform.IgnoreOutcome, platform.IgnoreSnapshot, error) {
	f.mu.Lock()
	f.appends++
	fault, ferr := f.fault, f.err
	f.fault, f.err = NoFault, nil
	f.mu.Unlock()
	if fault == FailBefore {
		return "", platform.IgnoreSnapshot{}, ferr
	}
	out, snap, err := f.inner.AppendIgnore(ctx, root, entries, expect)
	if fault == FailAfter && err == nil {
		return "", platform.IgnoreSnapshot{}, ferr
	}
	return out, snap, err
}

var _ platform.IgnoreWriter = (*IgnoreFake)(nil)

// PostedComment is a comment the fake host holds.
type PostedComment struct {
	ID     string
	Marker string
	Body   string
	Head   string
}

// RefusedError is an error that says nothing was written (a definite refusal).
type RefusedError struct{ Msg string }

func (e RefusedError) Error() string        { return e.Msg }
func (e RefusedError) UserMessage() string  { return e.Msg }
func (e RefusedError) NothingWritten() bool { return true }

// CommentFake is an in-memory review-comment host for one merge request. It
// follows the documented behaviour of the real host: a POST needs the MR head to
// equal the reviewed head, a lookup is conclusive only when it matches the
// marker, body hash and head, and a lookup against a moved head is a conflict.
type CommentFake struct {
	mu       sync.Mutex
	head     string
	comments []PostedComment
	next     int
	posts    int
	heads    int
	finds    int
	postF    Fault
	postErr  error
	findOne  *coreproposals.CommentLookup
	findErr  error
	headErr  error
	hash     func(body string) string
}

// NewCommentFake returns a host whose MR is at head. hash is the body hash the
// lookup compares (coreproposals.CommentBodyHash).
func NewCommentFake(head string, hash func(string) string) *CommentFake {
	return &CommentFake{head: head, hash: hash}
}

// SetHead moves the MR head.
func (f *CommentFake) SetHead(h string) { f.mu.Lock(); f.head = h; f.mu.Unlock() }

// ScriptPost makes the next PostReviewComment fail.
func (f *CommentFake) ScriptPost(fault Fault, err error) {
	f.mu.Lock()
	f.postF, f.postErr = fault, err
	f.mu.Unlock()
}

// ScriptFind makes the next FindReviewComment answer l, err.
func (f *CommentFake) ScriptFind(l coreproposals.CommentLookup, err error) {
	f.mu.Lock()
	f.findOne, f.findErr = &l, err
	f.mu.Unlock()
}

// ScriptHead makes MRHead fail until cleared with nil.
func (f *CommentFake) ScriptHead(err error) { f.mu.Lock(); f.headErr = err; f.mu.Unlock() }

// Posted returns the comments on the host.
func (f *CommentFake) Posted() []PostedComment {
	f.mu.Lock()
	defer f.mu.Unlock()
	return append([]PostedComment(nil), f.comments...)
}

// Posts, Finds and Heads count calls; Calls is their sum.
func (f *CommentFake) Posts() int { f.mu.Lock(); defer f.mu.Unlock(); return f.posts }
func (f *CommentFake) Finds() int { f.mu.Lock(); defer f.mu.Unlock(); return f.finds }
func (f *CommentFake) Heads() int { f.mu.Lock(); defer f.mu.Unlock(); return f.heads }
func (f *CommentFake) Calls() int { return f.Posts() + f.Finds() + f.Heads() }

func (f *CommentFake) MRHead(_ context.Context, _ core.RepoID, _ int) (string, error) {
	f.mu.Lock()
	defer f.mu.Unlock()
	f.heads++
	if f.headErr != nil {
		return "", f.headErr
	}
	return f.head, nil
}

func (f *CommentFake) PostReviewComment(_ context.Context, _ core.RepoID, _ int, expectedHead, body, marker string) (string, error) {
	f.mu.Lock()
	defer f.mu.Unlock()
	f.posts++
	fault, ferr := f.postF, f.postErr
	f.postF, f.postErr = NoFault, nil
	if fault == FailBefore {
		return "", ferr
	}
	if expectedHead != f.head {
		return "", RefusedError{Msg: "The merge request has new commits."}
	}
	f.next++
	id := "c" + string(rune('0'+f.next%10)) + "-" + marker[:4]
	f.comments = append(f.comments, PostedComment{ID: id, Marker: marker, Body: body, Head: expectedHead})
	if fault == FailAfter {
		return "", ferr
	}
	return id, nil
}

func (f *CommentFake) FindReviewComment(_ context.Context, _ core.RepoID, _ int, marker, bodyHash, expectedHead string) (coreproposals.CommentLookup, error) {
	f.mu.Lock()
	defer f.mu.Unlock()
	f.finds++
	if f.findOne != nil {
		l, err := *f.findOne, f.findErr
		f.findOne, f.findErr = nil, nil
		return l, err
	}
	if f.head != expectedHead {
		return coreproposals.CommentLookup{Outcome: coreproposals.CommentConflict}, nil
	}
	var match []PostedComment
	for _, c := range f.comments {
		if c.Marker == marker {
			match = append(match, c)
		}
	}
	switch {
	case len(match) == 0:
		return coreproposals.CommentLookup{Outcome: coreproposals.CommentAbsent}, nil
	case len(match) > 1 || f.hash(match[0].Body) != bodyHash || match[0].Head != expectedHead:
		return coreproposals.CommentLookup{Outcome: coreproposals.CommentConflict}, nil
	}
	return coreproposals.CommentLookup{Outcome: coreproposals.CommentFound, CommentID: match[0].ID}, nil
}

// PullCall is one call of the fake pull runner.
type PullCall struct {
	Repo       core.RepoID
	Branch     string
	Upstream   string
	ExpectHead string
	Verify     bool
}

// PullFake is a pull runner. R is the result type of the port
// (proposals.PullFFResult); it is a type parameter because this package cannot
// import internal/proposals, whose tests import it. Result builds the result of
// a successful call.
type PullFake[R any] struct {
	mu      sync.Mutex
	Result  func(PullCall) R
	calls   []PullCall
	applied int
	fault   Fault
	err     error
}

// Script makes the next PullFF fail as fault says with err.
func (f *PullFake[R]) Script(fault Fault, err error) {
	f.mu.Lock()
	f.fault, f.err = fault, err
	f.mu.Unlock()
}

// Calls returns every call; Applied counts the pulls that took effect.
func (f *PullFake[R]) Calls() []PullCall {
	f.mu.Lock()
	defer f.mu.Unlock()
	return append([]PullCall(nil), f.calls...)
}

// Applied counts the calls that took effect (including a lost reply).
func (f *PullFake[R]) Applied() int { f.mu.Lock(); defer f.mu.Unlock(); return f.applied }

// PullFF records the call and applies the scripted fault.
func (f *PullFake[R]) PullFF(_ context.Context, repo core.RepoID, branch, upstream, expectHead string, verify bool) (R, error) {
	f.mu.Lock()
	defer f.mu.Unlock()
	c := PullCall{Repo: repo, Branch: branch, Upstream: upstream, ExpectHead: expectHead, Verify: verify}
	f.calls = append(f.calls, c)
	fault, ferr := f.fault, f.err
	f.fault, f.err = NoFault, nil
	var zero R
	if fault == FailBefore {
		if ferr == nil {
			ferr = errors.New("the pull failed")
		}
		return zero, ferr
	}
	f.applied++
	if fault == FailAfter {
		if ferr == nil {
			ferr = errors.New("the reply was lost")
		}
		return zero, ferr
	}
	if f.Result == nil {
		return zero, nil
	}
	return f.Result(c), nil
}
