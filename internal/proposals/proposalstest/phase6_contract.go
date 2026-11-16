package proposalstest

import (
	"bytes"
	"context"
	"errors"
	"testing"

	"github.com/brendatamaar/yggdrasil/internal/core"
	coreproposals "github.com/brendatamaar/yggdrasil/internal/core/proposals"
	"github.com/brendatamaar/yggdrasil/internal/platform"
)

// IgnoreRig is an IgnoreWriter under test with the means to arrange and inspect
// its folder. Script may be nil (the real writer has no fault injection); the
// fault clauses then skip.
type IgnoreRig struct {
	W       platform.IgnoreWriter
	Root    string
	Seed    func(content []byte, present bool)
	Content func() (data []byte, exists bool)
	Script  func(f Fault, err error)
}

// IgnoreContract is what the ignore executor relies on: the reviewed state
// (absent or checksum) is enforced through the write, existing bytes and line
// endings are kept, a changed file is ErrChanged and left untouched, entries
// already there are not written twice, and a lost reply never causes a second
// append. The live platform writer runs this too (phase6_contract_test.go).
func IgnoreContract(t *testing.T, mk func(t *testing.T) IgnoreRig) {
	t.Helper()
	ctx := context.Background()
	entries := []string{"/dist/", "/node_modules/"}
	t.Run("absent file is created with exactly the entries", func(t *testing.T) {
		r := mk(t)
		r.Seed(nil, false)
		out, _, err := r.W.AppendIgnore(ctx, r.Root, entries, platform.IgnoreExpectation{Absent: true})
		if err != nil || out != platform.IgnoreCreated {
			t.Fatalf("append = %q, %v", out, err)
		}
		got, ok := r.Content()
		if !ok || string(got) != "/dist/\n/node_modules/\n" {
			t.Fatalf("content = %q, %v", got, ok)
		}
	})
	t.Run("existing bytes and CRLF are preserved", func(t *testing.T) {
		r := mk(t)
		orig := []byte("*.log\r\n# keep\r\n")
		r.Seed(orig, true)
		out, _, err := r.W.AppendIgnore(ctx, r.Root, entries, platform.IgnoreExpectation{Hash: platform.HashHex(orig)})
		if err != nil || out != platform.IgnoreAppended {
			t.Fatalf("append = %q, %v", out, err)
		}
		got, _ := r.Content()
		if !bytes.HasPrefix(got, orig) || bytes.Contains(bytes.TrimPrefix(got, orig), []byte("\n\n")) || !bytes.Contains(got, []byte("/dist/\r\n")) {
			t.Fatalf("content = %q", got)
		}
	})
	t.Run("a changed file is refused and not touched", func(t *testing.T) {
		r := mk(t)
		orig := []byte("a\n")
		r.Seed(orig, true)
		_, _, err := r.W.AppendIgnore(ctx, r.Root, entries, platform.IgnoreExpectation{Hash: platform.HashHex([]byte("other\n"))})
		if !errors.Is(err, platform.ErrChanged) {
			t.Fatalf("err = %v, want ErrChanged", err)
		}
		if got, _ := r.Content(); !bytes.Equal(got, orig) {
			t.Fatalf("content changed to %q", got)
		}
	})
	t.Run("a file that appeared is refused", func(t *testing.T) {
		r := mk(t)
		r.Seed([]byte("a\n"), true)
		_, _, err := r.W.AppendIgnore(ctx, r.Root, entries, platform.IgnoreExpectation{Absent: true})
		if !errors.Is(err, platform.ErrChanged) {
			t.Fatalf("err = %v, want ErrChanged", err)
		}
	})
	t.Run("entries already there are not written twice", func(t *testing.T) {
		r := mk(t)
		orig := []byte("/dist/\n/node_modules/\n")
		r.Seed(orig, true)
		out, _, err := r.W.AppendIgnore(ctx, r.Root, entries, platform.IgnoreExpectation{Hash: platform.HashHex(orig)})
		if err != nil || out != platform.IgnoreAlreadyPresent {
			t.Fatalf("append = %q, %v", out, err)
		}
		if got, _ := r.Content(); !bytes.Equal(got, orig) {
			t.Fatalf("content = %q", got)
		}
	})
	t.Run("a lost reply never causes a second append", func(t *testing.T) {
		r := mk(t)
		if r.Script == nil {
			t.Skip("this writer cannot inject faults")
		}
		orig := []byte("a\n")
		r.Seed(orig, true)
		boom := errors.New("lost")
		r.Script(FailBefore, boom)
		if _, _, err := r.W.AppendIgnore(ctx, r.Root, entries, platform.IgnoreExpectation{Hash: platform.HashHex(orig)}); !errors.Is(err, boom) {
			t.Fatalf("err = %v", err)
		}
		if got, _ := r.Content(); !bytes.Equal(got, orig) {
			t.Fatalf("a failed-before write changed the file: %q", got)
		}
		r.Script(FailAfter, boom)
		if _, _, err := r.W.AppendIgnore(ctx, r.Root, entries, platform.IgnoreExpectation{Hash: platform.HashHex(orig)}); !errors.Is(err, boom) {
			t.Fatalf("err = %v", err)
		}
		landed, _ := r.Content()
		if bytes.Equal(landed, orig) {
			t.Fatal("a lost reply must have landed")
		}
		if _, _, err := r.W.AppendIgnore(ctx, r.Root, entries, platform.IgnoreExpectation{Hash: platform.HashHex(orig)}); !errors.Is(err, platform.ErrChanged) {
			t.Fatalf("retry err = %v, want ErrChanged", err)
		}
		if got, _ := r.Content(); !bytes.Equal(got, landed) {
			t.Fatalf("the retry changed the file again: %q", got)
		}
	})
}

// commentHost is the method set of proposals.CommentHost.
type commentHost interface {
	MRHead(ctx context.Context, repo core.RepoID, mr int) (string, error)
	PostReviewComment(ctx context.Context, repo core.RepoID, mr int, expectedHead, body, marker string) (string, error)
	FindReviewComment(ctx context.Context, repo core.RepoID, mr int, marker, bodyHash, expectedHead string) (coreproposals.CommentLookup, error)
}

// CommentRig is a CommentHost under test. Head is the MR head it starts at;
// Count is the number of comments the host holds; SetHead and Script may be nil.
type CommentRig struct {
	Host    commentHost
	Head    string
	Count   func() int
	SetHead func(string)
	Script  func(f Fault, err error)
}

// CommentHostContract is what the comment executor relies on: a POST needs the
// reviewed head, a lookup finds exactly the posted comment (marker, body hash,
// head), is absent for none, conflicts for a duplicate or a moved head, and a
// lost reply is found by its marker.
func CommentHostContract(t *testing.T, mk func(t *testing.T) CommentRig) {
	t.Helper()
	ctx := context.Background()
	const repo, mr = core.RepoID("rep_1"), 7
	marker, body := "0123456789abcdef", "Looks fine.\nOne nit."
	hash := coreproposals.CommentBodyHash(body)
	t.Run("nothing posted is absent", func(t *testing.T) {
		r := mk(t)
		l, err := r.Host.FindReviewComment(ctx, repo, mr, marker, hash, r.Head)
		if err != nil || l.Outcome != coreproposals.CommentAbsent {
			t.Fatalf("find = %+v, %v", l, err)
		}
	})
	t.Run("a posted comment is found with its id", func(t *testing.T) {
		r := mk(t)
		id, err := r.Host.PostReviewComment(ctx, repo, mr, r.Head, body, marker)
		if err != nil || id == "" {
			t.Fatalf("post = %q, %v", id, err)
		}
		l, err := r.Host.FindReviewComment(ctx, repo, mr, marker, hash, r.Head)
		if err != nil || l.Outcome != coreproposals.CommentFound || l.CommentID != id {
			t.Fatalf("find = %+v, %v; want found %s", l, err, id)
		}
	})
	t.Run("a different body under the marker is a conflict", func(t *testing.T) {
		r := mk(t)
		if _, err := r.Host.PostReviewComment(ctx, repo, mr, r.Head, body, marker); err != nil {
			t.Fatal(err)
		}
		l, err := r.Host.FindReviewComment(ctx, repo, mr, marker, coreproposals.CommentBodyHash("other"), r.Head)
		if err != nil || l.Outcome != coreproposals.CommentConflict {
			t.Fatalf("find = %+v, %v", l, err)
		}
	})
	t.Run("a post on a moved head posts nothing", func(t *testing.T) {
		r := mk(t)
		if r.SetHead == nil {
			t.Skip("this host cannot move its head")
		}
		r.SetHead("moved")
		if _, err := r.Host.PostReviewComment(ctx, repo, mr, r.Head, body, marker); err == nil {
			t.Fatal("a comment was posted for a head that is not current")
		}
		if n := r.Count(); n != 0 {
			t.Fatalf("%d comments on the host", n)
		}
	})
	t.Run("a lost reply is found by its marker", func(t *testing.T) {
		r := mk(t)
		if r.Script == nil {
			t.Skip("this host cannot inject faults")
		}
		r.Script(FailAfter, errors.New("lost"))
		if _, err := r.Host.PostReviewComment(ctx, repo, mr, r.Head, body, marker); err == nil {
			t.Fatal("the scripted fault did not fire")
		}
		l, err := r.Host.FindReviewComment(ctx, repo, mr, marker, hash, r.Head)
		if err != nil || l.Outcome != coreproposals.CommentFound {
			t.Fatalf("find after a lost reply = %+v, %v", l, err)
		}
		if n := r.Count(); n != 1 {
			t.Fatalf("%d comments, want exactly 1", n)
		}
	})
	t.Run("a failed-before post leaves nothing", func(t *testing.T) {
		r := mk(t)
		if r.Script == nil {
			t.Skip("this host cannot inject faults")
		}
		r.Script(FailBefore, errors.New("down"))
		if _, err := r.Host.PostReviewComment(ctx, repo, mr, r.Head, body, marker); err == nil {
			t.Fatal("the scripted fault did not fire")
		}
		if n := r.Count(); n != 0 {
			t.Fatalf("%d comments on the host", n)
		}
	})
}
