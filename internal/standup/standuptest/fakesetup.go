package standuptest

import (
	"context"
	"fmt"
	"testing"
	"time"

	"github.com/brendatamaar/yggdrasil/internal/core"
	corestandup "github.com/brendatamaar/yggdrasil/internal/core/standup"
	"github.com/brendatamaar/yggdrasil/internal/repos"
)

type fakeSetup struct {
	seed func(repo core.RepoID, fs []Fact)
}

func (s fakeSetup) Seed(_ *testing.T, repo core.RepoID, fs []Fact) { s.seed(repo, fs) }
func (fakeSetup) Missing() core.RepoID                             { return "rep_gone" }

// FakeCommitSource returns the fake history reader as a contract subject.
func FakeCommitSource(*testing.T) (Reader, SourceSetup) {
	f := &Commits{}
	return CommitReader(f), fakeSetup{func(repo core.RepoID, fs []Fact) {
		cs := make([]repos.HistoryCommit, len(fs))
		for i, x := range fs {
			cs[i] = repos.HistoryCommit{OID: fmt.Sprintf("%040x", i+1), Subject: x.Subject, Author: "Ada Test", At: x.At}
		}
		f.Seed(repo, cs)
	}}
}

// FakeMRSource returns the fake MR source as a contract subject.
func FakeMRSource(*testing.T) (Reader, SourceSetup) {
	f := &MRs{}
	read := func(ctx context.Context, repo core.RepoID, from, to time.Time, max int) ([]Fact, bool, error) {
		ms, trunc, err := f.MRs(ctx, repo, from, to, max)
		out := make([]Fact, 0, len(ms))
		for _, m := range ms {
			if m.Repo != repo {
				return nil, false, fmt.Errorf("record of repo %s returned for %s", m.Repo, repo)
			}
			out = append(out, Fact{At: m.At, Subject: m.Title})
		}
		return out, trunc, err
	}
	return read, fakeSetup{func(repo core.RepoID, fs []Fact) {
		ms := make([]corestandup.MR, len(fs))
		for i, x := range fs {
			ms[i] = corestandup.MR{Repo: repo, RepoName: string(repo), Number: i + 1, Title: x.Subject, State: "open", At: x.At}
		}
		f.Seed(repo, ms)
	}}
}

// FakeBuildSource returns the fake build source as a contract subject.
func FakeBuildSource(*testing.T) (Reader, SourceSetup) {
	f := &Builds{}
	read := func(ctx context.Context, repo core.RepoID, from, to time.Time, max int) ([]Fact, bool, error) {
		bs, trunc, err := f.Builds(ctx, repo, from, to, max)
		out := make([]Fact, 0, len(bs))
		for _, b := range bs {
			if b.Repo != repo {
				return nil, false, fmt.Errorf("record of repo %s returned for %s", b.Repo, repo)
			}
			out = append(out, Fact{At: b.At, Subject: string(b.ID)})
		}
		return out, trunc, err
	}
	return read, fakeSetup{func(repo core.RepoID, fs []Fact) {
		bs := make([]corestandup.Build, len(fs))
		for i, x := range fs {
			bs[i] = corestandup.Build{Repo: repo, RepoName: string(repo), ID: core.BuildID(x.Subject), Status: core.BuildSucceeded, At: x.At}
		}
		f.Seed(repo, bs)
	}}
}
