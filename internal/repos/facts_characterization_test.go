package repos_test

import (
	"context"
	"errors"
	"fmt"
	"reflect"
	"testing"

	corerepos "github.com/brendatamaar/yggdrasil/internal/core/repos"
	"github.com/brendatamaar/yggdrasil/internal/git"
	"github.com/brendatamaar/yggdrasil/internal/repos"
)

type pullFactsGit struct {
	repos.GitRunner
	before, after    git.Status
	readErr, pullErr error
	calls            []string
}

func (g *pullFactsGit) Status(context.Context, string) (git.Status, error) {
	g.calls = append(g.calls, "status")
	if len(g.calls) == 1 {
		return g.before, nil
	}
	return g.after, g.readErr
}
func (g *pullFactsGit) PullFFOnly(context.Context, string) error {
	g.calls = append(g.calls, "pull-ff-only")
	return g.pullErr
}

func TestCharacterizePullFacts(t *testing.T) {
	before := "123456789012abcdef"
	for _, tc := range []struct {
		name, after string
		readErr     error
		want        corerepos.ActOutcome
	}{
		{"unchanged", before, nil, corerepos.UpToDate{}},
		{"changed", "abcdef1234567890", nil, corerepos.Pulled{From: "123456789012", To: "abcdef123456"}},
		{"post-read-fails", "", errors.New("read failed"), corerepos.Pulled{From: "123456789012"}},
		{"empty-heads", "", nil, corerepos.Pulled{From: "123456789012"}},
	} {
		t.Run(tc.name, func(t *testing.T) {
			g := &pullFactsGit{before: git.Status{OID: before}, after: git.Status{OID: tc.after}, readErr: tc.readErr}
			got := repos.RunGitOp(context.Background(), g, corerepos.OpPull, "shop-api", "")
			if !reflect.DeepEqual(got, tc.want) {
				t.Fatalf("got %#v, want %#v", got, tc.want)
			}
			if !reflect.DeepEqual(g.calls, []string{"status", "pull-ff-only", "status"}) {
				t.Fatal(g.calls)
			}
		})
	}
	for _, tc := range []struct {
		reason git.FFReason
		want   corerepos.PullReason
	}{
		{git.FFDetached, corerepos.PullDetached}, {git.FFNoUpstream, corerepos.PullNoUpstream},
		{git.FFDiverged, corerepos.PullDiverged}, {git.FFDirty, corerepos.PullDirty},
		{git.FFOther, corerepos.PullOther}, {"future", corerepos.PullOther},
	} {
		t.Run(string(tc.reason), func(t *testing.T) {
			ff := &git.FFError{Reason: tc.reason, Branch: "main", Upstream: "origin/main", Ahead: 2, Behind: 3}
			g := &pullFactsGit{pullErr: fmt.Errorf("wrapped: %w", ff)}
			got := repos.RunGitOp(context.Background(), g, corerepos.OpPull, "shop-api", "")
			want := corerepos.PullRefused{Reason: tc.want, Message: ff.UserMessage()}
			if !reflect.DeepEqual(got, want) {
				t.Fatalf("got %#v, want %#v", got, want)
			}
			if !reflect.DeepEqual(g.calls, []string{"status", "pull-ff-only"}) {
				t.Fatal(g.calls)
			}
		})
	}
}

type statusFactsGit struct {
	repos.DetailGit
	status git.Status
	reads  int
}

func (g *statusFactsGit) Status(context.Context, string) (git.Status, error) {
	g.reads++
	return g.status, nil
}

func TestCharacterizeSampledStatusFacts(t *testing.T) {
	for _, truncated := range []bool{false, true} {
		t.Run(fmt.Sprint(truncated), func(t *testing.T) {
			st := git.Status{Branch: "main", Untracked: 10000, Staged: 2, Unstaged: 3, Conflicted: 1, Files: []git.FileChange{{Path: "dist/a.txt", Index: "?", Worktree: "?"}}, Truncated: truncated}
			g := &statusFactsGit{status: st}
			s := repos.NewDetailService(fakeDetailRepos{"r1": "shop-api"}, g)
			got, err := s.Changes(context.Background(), "r1")
			if err != nil || !reflect.DeepEqual(got, st) || g.reads != 1 {
				t.Fatalf("got %+v, err %v, reads %d", got, err, g.reads)
			}
			snap, err := repos.SnapshotFrom(got, nil)
			if err != nil || snap.Untracked != 10000 || snap.Staged != 2 || snap.Unstaged != 3 || snap.Conflicted != 1 {
				t.Fatalf("snapshot %+v, %v", snap, err)
			}
			if _, err := s.Changes(context.Background(), "untracked"); err == nil || g.reads != 1 {
				t.Fatalf("scope check: %v, reads %d", err, g.reads)
			}
		})
	}
}
