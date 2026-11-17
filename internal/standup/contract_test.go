package standup_test

import (
	"bytes"
	"context"
	"errors"
	"os"
	"path/filepath"
	"sync"
	"testing"
	"time"

	"github.com/brendatamaar/yggdrasil/internal/core"
	"github.com/brendatamaar/yggdrasil/internal/git"
	"github.com/brendatamaar/yggdrasil/internal/platform"
	"github.com/brendatamaar/yggdrasil/internal/repos"
	"github.com/brendatamaar/yggdrasil/internal/safety"
	"github.com/brendatamaar/yggdrasil/internal/standup/standuptest"
)

// The source contract runs against the fakes and, for commits, against the real
// repos.GitHistoryReader on temporary git repos. There is no real MR source (it
// is the cached host data of another task) or build reader in this repo yet:
// when one lands it adds one line here.
func TestSourceContract(t *testing.T) {
	t.Run("fake commits", func(t *testing.T) { standuptest.SourceContract(t, standuptest.FakeCommitSource) })
	t.Run("fake merge requests", func(t *testing.T) { standuptest.SourceContract(t, standuptest.FakeMRSource) })
	t.Run("fake builds", func(t *testing.T) { standuptest.SourceContract(t, standuptest.FakeBuildSource) })
	t.Run("real git history", func(t *testing.T) { standuptest.SourceContract(t, newGitSource) })
}

// gitSource is a real GitHistoryReader over temp repos, as a contract subject.
type gitSource struct {
	t   *testing.T
	env []string
	exe string
	g   *git.Git

	mu    sync.Mutex
	repos []core.Repo
}

func newGitSource(t *testing.T) (standuptest.Reader, standuptest.SourceSetup) {
	t.Helper()
	cfg := filepath.Join(t.TempDir(), "gitconfig")
	if err := os.WriteFile(cfg, nil, 0o600); err != nil {
		t.Fatal(err)
	}
	env, err := safety.NewEnvBuilder().Build(safety.EnvOptions{Extra: map[string]string{
		"GIT_CONFIG_GLOBAL": cfg, "GIT_CONFIG_NOSYSTEM": "1",
		"GIT_AUTHOR_NAME": "Ada Test", "GIT_AUTHOR_EMAIL": "ada@example.test",
		"GIT_COMMITTER_NAME": "Ada Test", "GIT_COMMITTER_EMAIL": "ada@example.test",
		"GIT_COMMITTER_DATE": "2030-03-01T12:00:00Z",
	}})
	if err != nil {
		t.Fatal(err)
	}
	g, err := git.New(env)
	if errors.Is(err, git.ErrNotInstalled) {
		t.Skip("git is not installed")
	}
	if err != nil {
		t.Fatal(err)
	}
	exe, err := platform.Resolve("git", "", env)
	if err != nil {
		t.Fatal(err)
	}
	s := &gitSource{t: t, env: env, exe: exe, g: g}
	return standuptest.CommitReader(repos.NewHistoryReader(s, g)), s
}

func (s *gitSource) run(dir string, args ...string) {
	s.t.Helper()
	var out, errb bytes.Buffer
	argv := append([]string{s.exe, "-c", "core.longpaths=true"}, args...)
	p, err := platform.Start(context.Background(), platform.Spec{Argv: argv, Dir: dir, Env: s.env, Stdout: &out, Stderr: &errb})
	if err != nil {
		s.t.Fatal(err)
	}
	if code, err := p.Wait(); err != nil || code != 0 {
		s.t.Fatalf("git %v: exit %d, err %v: %s", args, code, err, errb.String())
	}
}

func (s *gitSource) Seed(t *testing.T, repo core.RepoID, fs []standuptest.Fact) {
	t.Helper()
	dir := filepath.Join(t.TempDir(), string(repo))
	if err := os.MkdirAll(dir, 0o750); err != nil {
		t.Fatal(err)
	}
	s.run(dir, "init", "-q", "-b", "main")
	for _, f := range fs {
		s.run(dir, "commit", "-q", "--allow-empty", "-m", f.Subject, "--date="+f.At.Format(time.RFC3339))
	}
	s.mu.Lock()
	defer s.mu.Unlock()
	s.repos = append(s.repos, core.Repo{ID: repo, ProjectID: "prj_1", Name: string(repo), Path: dir})
}

func (s *gitSource) Missing() core.RepoID { return "rep_gone" }

// ActiveSource for the reader.
func (s *gitSource) ListProjects(context.Context) ([]core.Project, error) {
	return []core.Project{{ID: "prj_1", Name: "acme-shop"}}, nil
}

func (s *gitSource) ListRepos(context.Context, core.ProjectID) ([]core.Repo, error) {
	s.mu.Lock()
	defer s.mu.Unlock()
	return append([]core.Repo(nil), s.repos...), nil
}
