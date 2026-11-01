package proposals

import (
	"errors"
	"reflect"
	"strings"
	"testing"

	"github.com/brendatamaar/yggdrasil/internal/core"
)

const (
	mergeArgs  = `{"repo":"r1","worktree":"w1","branch":"ygg/swift-fern","target":"main","expect_branch_head":"` + h40 + `","title":"Add thing"}`
	removeArgs = `{"worktree":"w1"}`
)

func TestLandingKindsValidate(t *testing.T) {
	for _, k := range []core.ProposalKind{core.ProposalMergeLocal, core.ProposalRemoveWorktree} {
		if err := k.Validate(); err != nil {
			t.Errorf("%s: %v", k, err)
		}
	}
	if err := core.WorktreeRemoving.Validate(); err != nil {
		t.Error(err)
	}
}

func TestLandingActionsRoundTrip(t *testing.T) {
	for kind, args := range map[string]string{"merge_local": mergeArgs, "remove_worktree": removeArgs} {
		a, err := ParseAction(kind, []byte(args))
		if err != nil {
			t.Fatalf("%s: %v", kind, err)
		}
		if err := a.Validate(); err != nil {
			t.Errorf("%s: %v", kind, err)
		}
		k, row := RowOf(a)
		if string(k) != kind {
			t.Errorf("%s: kind %s", kind, k)
		}
		back, err := ActionFromRow(k, row)
		if err != nil || !reflect.DeepEqual(back, a) {
			t.Errorf("%s: round trip gave %#v, %v", kind, back, err)
		}
		if Hash(a) == "" || len(Describe(a)) == 0 {
			t.Errorf("%s: no hash or description", kind)
		}
	}
}

func TestMergeLocalFieldsChangeHash(t *testing.T) {
	a, _ := ParseAction("merge_local", []byte(mergeArgs))
	m := a.(MergeLocal)
	for name, f := range map[string]func(MergeLocal) MergeLocal{
		"worktree": func(x MergeLocal) MergeLocal { x.Worktree = "w2"; return x },
		"branch":   func(x MergeLocal) MergeLocal { x.Branch = "other"; return x },
		"target":   func(x MergeLocal) MergeLocal { x.TargetBranch = "dev"; return x },
		"head":     func(x MergeLocal) MergeLocal { x.ExpectBranchHead = h64; return x },
		"title":    func(x MergeLocal) MergeLocal { x.Title = "other"; return x },
		"repo":     func(x MergeLocal) MergeLocal { x.Where = MainCheckout{Repo: "r2"}; return x },
	} {
		if Hash(f(m)) == Hash(m) {
			t.Errorf("changing %s did not change the hash", name)
		}
	}
}

func TestLandingRejectionRules(t *testing.T) {
	long := strings.Repeat("a", MaxTitleLen+1)
	cases := []struct{ name, kind, args string }{
		{"unknown field", "merge_local", strings.Replace(mergeArgs, `"title"`, `"force":true,"title"`, 1)},
		{"trailing data", "merge_local", mergeArgs + `{}`},
		{"no repo", "merge_local", strings.Replace(mergeArgs, `"repo":"r1",`, ``, 1)},
		{"repo with slash", "merge_local", strings.Replace(mergeArgs, `"r1"`, `"a/b"`, 1)},
		{"no worktree", "merge_local", strings.Replace(mergeArgs, `"worktree":"w1",`, ``, 1)},
		{"bad branch", "merge_local", strings.Replace(mergeArgs, `ygg/swift-fern`, `-x`, 1)},
		{"bad target", "merge_local", strings.Replace(mergeArgs, `"main"`, `"a..b"`, 1)},
		{"short head", "merge_local", strings.Replace(mergeArgs, h40, h40[:12], 1)},
		{"upper-case head", "merge_local", strings.Replace(mergeArgs, h40, strings.ToUpper(h40), 1)},
		{"no head", "merge_local", strings.Replace(mergeArgs, `"expect_branch_head":"`+h40+`",`, ``, 1)},
		{"empty title", "merge_local", strings.Replace(mergeArgs, `Add thing`, ``, 1)},
		{"long title", "merge_local", strings.Replace(mergeArgs, `Add thing`, long, 1)},
		{"unknown field remove", "remove_worktree", `{"worktree":"w1","force":true}`},
		{"path in remove", "remove_worktree", `{"worktree":"w1","path":"C:/x"}`},
		{"no worktree remove", "remove_worktree", `{}`},
		{"worktree with slash", "remove_worktree", `{"worktree":"../x"}`},
		{"trailing data remove", "remove_worktree", removeArgs + `x`},
	}
	for _, c := range cases {
		_, err := ParseAction(c.kind, []byte(c.args))
		if err == nil || !errors.Is(err, ErrInvalidAction) {
			t.Errorf("%s: accepted or wrong error: %v", c.name, err)
		}
	}
}

func TestMergeLocalMustActInMainCheckout(t *testing.T) {
	a, _ := ParseAction("merge_local", []byte(mergeArgs))
	m := a.(MergeLocal)
	m.Where = InWorktree{ID: "w1"}
	if m.Validate() == nil {
		t.Error("a merge in a worktree was accepted")
	}
	m.Where = nil
	if m.Validate() == nil {
		t.Error("a merge with no target was accepted")
	}
}
