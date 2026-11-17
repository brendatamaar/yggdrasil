package proposalstest

import (
	"context"
	"errors"
	"os"
	"path/filepath"
	"testing"

	"github.com/brendatamaar/yggdrasil/internal/core"
	coreproposals "github.com/brendatamaar/yggdrasil/internal/core/proposals"
	"github.com/brendatamaar/yggdrasil/internal/platform"
)

func TestIgnoreContractFake(t *testing.T) {
	IgnoreContract(t, func(t *testing.T) IgnoreRig {
		f := NewIgnoreFake()
		root := filepath.Join(t.TempDir(), "shop-api")
		return IgnoreRig{
			W: f, Root: root,
			Seed:    func(c []byte, present bool) { f.Seed(t, root, c, present) },
			Content: func() ([]byte, bool) { return f.Content(t, root) },
			Script:  f.Script,
		}
	})
}

// The same suite against the real platform writer on a temporary folder.
func TestIgnoreContractRealWriter(t *testing.T) {
	IgnoreContract(t, func(t *testing.T) IgnoreRig {
		root := t.TempDir()
		path := filepath.Join(root, ".gitignore")
		return IgnoreRig{
			W: platform.NewIgnoreWriter(), Root: root,
			Seed: func(c []byte, present bool) {
				if present {
					if err := os.WriteFile(path, c, 0o600); err != nil {
						t.Fatal(err)
					}
				}
			},
			Content: func() ([]byte, bool) {
				b, err := os.ReadFile(path) //nolint:gosec // a temp folder of this test
				return b, err == nil
			},
		}
	})
}

func TestCommentHostContractFake(t *testing.T) {
	CommentHostContract(t, func(t *testing.T) CommentRig {
		head := "a1b2c3d4e5f6a1b2c3d4e5f6a1b2c3d4e5f6a1b2"
		f := NewCommentFake(head, coreproposals.CommentBodyHash)
		return CommentRig{Host: f, Head: head, Count: func() int { return len(f.Posted()) }, SetHead: f.SetHead, Script: f.ScriptPost}
	})
}

// The fake pull runner records its calls and applies faults as documented.
func TestPullFakeFaults(t *testing.T) {
	type res struct{ Head string }
	f := &PullFake[res]{Result: func(PullCall) res { return res{Head: "new"} }}
	ctx := context.Background()
	boom := errors.New("boom")
	f.Script(FailBefore, boom)
	if _, err := f.PullFF(ctx, core.RepoID("rep_1"), "main", "origin/main", "abc", true); !errors.Is(err, boom) || f.Applied() != 0 {
		t.Fatalf("fail-before: err %v, applied %d", err, f.Applied())
	}
	f.Script(FailAfter, boom)
	if _, err := f.PullFF(ctx, "rep_1", "main", "origin/main", "abc", true); !errors.Is(err, boom) || f.Applied() != 1 {
		t.Fatalf("fail-after: err %v, applied %d", err, f.Applied())
	}
	r, err := f.PullFF(ctx, "rep_1", "main", "origin/main", "abc", false)
	if err != nil || r.Head != "new" || f.Applied() != 2 || len(f.Calls()) != 3 {
		t.Fatalf("ok call: %+v %v applied %d calls %d", r, err, f.Applied(), len(f.Calls()))
	}
	if c := f.Calls()[0]; c.Branch != "main" || c.Upstream != "origin/main" || c.ExpectHead != "abc" || !c.Verify {
		t.Fatalf("call = %+v", c)
	}
}
