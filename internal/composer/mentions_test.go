package composer

import (
	"bytes"
	"context"
	"errors"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/brendatamaar/yggdrasil/internal/apperr"
	"github.com/brendatamaar/yggdrasil/internal/core"
	corerepos "github.com/brendatamaar/yggdrasil/internal/core/repos"
	"github.com/brendatamaar/yggdrasil/internal/db"
	"github.com/brendatamaar/yggdrasil/internal/db/dbtest"
	"github.com/brendatamaar/yggdrasil/internal/platform"
	"github.com/brendatamaar/yggdrasil/internal/safety"
)

// isoEnv builds the clean environment with a private git config, so the
// user's settings cannot change a result.
type isoEnv struct{ cfg string }

func (e isoEnv) Build(o safety.EnvOptions) ([]string, error) {
	extra := map[string]string{
		"GIT_CONFIG_GLOBAL": e.cfg, "GIT_CONFIG_NOSYSTEM": "1",
		"GIT_AUTHOR_NAME": "Ada Test", "GIT_AUTHOR_EMAIL": "ada@example.test",
		"GIT_COMMITTER_NAME": "Ada Test", "GIT_COMMITTER_EMAIL": "ada@example.test",
	}
	for k, v := range o.Extra {
		extra[k] = v
	}
	o.Extra = extra
	return safety.NewEnvBuilder().Build(o)
}

func newIso(t *testing.T) isoEnv {
	t.Helper()
	cfg := filepath.Join(t.TempDir(), "gitconfig")
	if err := os.WriteFile(cfg, nil, 0o600); err != nil {
		t.Fatal(err)
	}
	return isoEnv{cfg: cfg}
}

// runGit runs git through platform.Start (exec.Command is forbidden).
func runGit(t *testing.T, env isoEnv, dir string, args ...string) {
	t.Helper()
	built, err := env.Build(safety.EnvOptions{})
	if err != nil {
		t.Fatal(err)
	}
	exe, err := platform.Resolve("git", "", built)
	if err != nil {
		t.Skip("git is not installed")
	}
	var out, errb bytes.Buffer
	p, err := platform.Start(context.Background(), platform.Spec{Argv: append([]string{exe}, args...), Dir: dir, Env: built, Stdout: &out, Stderr: &errb})
	if err != nil {
		t.Fatal(err)
	}
	if code, err := p.Wait(); err != nil || code != 0 {
		t.Fatalf("git %v: %d %v %s", args, code, err, errb.String())
	}
}

func write(t *testing.T, path, content string) {
	t.Helper()
	if err := os.MkdirAll(filepath.Dir(path), 0o750); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(path, []byte(content), 0o600); err != nil {
		t.Fatal(err)
	}
}

type world struct {
	st      *db.Store
	env     isoEnv
	project core.Project
	api     core.Repo
	web     core.Repo
	root    string
}

func newWorld(t *testing.T) *world {
	t.Helper()
	st := newStore(t)
	env := newIso(t)
	root := t.TempDir()
	w := &world{st: st, env: env, root: root}
	w.project = dbtest.Project(t, st, core.Project{Name: "acme-shop", Path: filepath.Join(root, "acme-shop")})
	mk := func(name string, files map[string]string) core.Repo {
		dir := filepath.Join(w.project.Path, name)
		runGit(t, env, root, "init", "-q", dir)
		for p, c := range files {
			write(t, filepath.Join(dir, filepath.FromSlash(p)), c)
		}
		runGit(t, env, dir, "add", "-A")
		runGit(t, env, dir, "commit", "-q", "-m", "init")
		return dbtest.Repo(t, st, core.Repo{ProjectID: w.project.ID, Name: name, Path: dir, DefaultBranch: "main"})
	}
	w.api = mk("shop-api", map[string]string{
		"README.md":                "# api\r\n",
		"src/main.go":              "package main\r\n",
		"docs/user guide/intro.md": "x",
		"src/über/größe.txt":       "x",
		"src/handlers/orders.go":   "x",
		".gitignore":               "ignored.txt\n",
	})
	w.web = mk("shop web", map[string]string{"src/App.tsx": "x", "README.md": "# web"})
	// An untracked file in a tracked repo shows (new work), an ignored one does not.
	write(t, filepath.Join(w.api.Path, "src", "new_file.go"), "x")
	write(t, filepath.Join(w.api.Path, "ignored.txt"), "x")
	// A sibling folder that no tracked repo covers must never appear.
	write(t, filepath.Join(w.project.Path, "notes", "secret.txt"), "x")
	runGit(t, env, filepath.Join(w.project.Path, "notes"), "init", "-q")
	return w
}

func (w *world) svc(t *testing.T, mod func(*Config)) *Service {
	t.Helper()
	return newService(t, w.st, func(c *Config) {
		c.Env = w.env
		if mod != nil {
			mod(c)
		}
	})
}

func TestMentionsFindsFilesAndFoldersOfTrackedRepos(t *testing.T) {
	t.Parallel()
	w := newWorld(t)
	s := w.svc(t, nil)
	res, err := s.Mentions(context.Background(), w.project.ID, "", 500)
	if err != nil {
		t.Fatal(err)
	}
	if len(res.Mentions) > 50 {
		t.Fatalf("limit not capped at 50: %d", len(res.Mentions))
	}
	res, err = s.Mentions(context.Background(), w.project.ID, "", 50)
	if err != nil {
		t.Fatal(err)
	}
	got := map[string]string{}
	for _, m := range res.Mentions {
		got[m.RepoName+":"+m.Path] = m.Kind
		if strings.Contains(m.Path, "secret") || strings.Contains(m.Path, ".git/") || m.Path == ".git" || strings.HasPrefix(m.Path, "..") {
			t.Fatalf("forbidden entry %+v", m)
		}
	}
	for _, want := range []string{"shop-api:src/main.go", "shop-api:src", "shop web:src/App.tsx", "shop-api:docs/user guide", "shop-api:src/über/größe.txt"} {
		if _, ok := got[want]; !ok {
			t.Errorf("missing %q in %v", want, got)
		}
	}
	if got["shop-api:src"] != "dir" || got["shop-api:src/main.go"] != "file" {
		t.Errorf("kinds: %v", got)
	}
	if _, ok := got["shop-api:src/new_file.go"]; !ok {
		t.Errorf("a new file of a tracked repo should show: %v", got)
	}
	if _, ok := got["shop-api:ignored.txt"]; ok {
		t.Error("an ignored file must not show")
	}
}

func TestMentionsQueryNeverReachesUntrackedSiblings(t *testing.T) {
	t.Parallel()
	w := newWorld(t)
	s := w.svc(t, nil)
	for _, q := range []string{"secret", "notes", "secret.txt"} {
		res, err := s.Mentions(context.Background(), w.project.ID, q, 50)
		if err != nil {
			t.Fatal(err)
		}
		if len(res.Mentions) != 0 {
			t.Fatalf("query %q found %+v", q, res.Mentions)
		}
	}
	res, _ := s.Mentions(context.Background(), w.project.ID, "gröss", 50)
	if len(res.Mentions) != 0 {
		t.Fatalf("no subsequence of gröss expected: %+v", res.Mentions)
	}
	res, _ = s.Mentions(context.Background(), w.project.ID, "GRÖßE", 50)
	if len(res.Mentions) == 0 || res.Mentions[0].Path != "src/über/größe.txt" {
		t.Fatalf("unicode, case-insensitive: %+v", res.Mentions)
	}
}

func TestMentionsSkipsUntrackedRepos(t *testing.T) {
	t.Parallel()
	w := newWorld(t)
	ws, err := w.st.GetWorkspace(context.Background())
	if err != nil {
		t.Fatal(err)
	}
	err = w.st.Commit(context.Background(), func(tx *db.Tx) error {
		return tx.Workspace().MarkRepoUntracked(context.Background(), corerepos.MarkRepoUntracked{
			ProjectID: w.project.ID, RepoID: w.web.ID, At: time.Now().UTC(), ExpectVersion: ws.Projects[w.project.ID].Version,
		})
	})
	if err != nil {
		t.Fatal(err)
	}
	res, err := w.svc(t, nil).Mentions(context.Background(), w.project.ID, "App", 50)
	if err != nil {
		t.Fatal(err)
	}
	if len(res.Mentions) != 0 {
		t.Fatalf("untracked repo is visible: %+v", res.Mentions)
	}
}

func TestMentionsCapsTheScanAndSaysSo(t *testing.T) {
	t.Parallel()
	w := newWorld(t)
	res, err := w.svc(t, func(c *Config) { c.MaxScanFiles = 3 }).Mentions(context.Background(), w.project.ID, "", 50)
	if err != nil {
		t.Fatal(err)
	}
	if !res.Truncated {
		t.Fatal("a cut scan must be reported")
	}
	files := 0
	for _, m := range res.Mentions {
		if m.Kind == "file" && m.RepoName == "shop-api" {
			files++
		}
	}
	if files > 3 {
		t.Fatalf("scan cap broken: %d files", files)
	}
}

func TestMentionsHonoursCancelAndUnknownProject(t *testing.T) {
	t.Parallel()
	w := newWorld(t)
	s := w.svc(t, nil)
	ctx, cancel := context.WithCancel(context.Background())
	cancel()
	if _, err := s.Mentions(ctx, w.project.ID, "x", 5); err == nil {
		t.Fatal("a cancelled context must stop the search")
	}
	_, err := s.Mentions(context.Background(), core.ProjectID("prj_0000000000000000"), "x", 5)
	if c, _ := apperr.ClassOf(err); c != apperr.NotFound {
		t.Fatalf("unknown project: %v", err)
	}
	if errors.Is(err, context.Canceled) {
		t.Fatal("wrong error")
	}
}

func TestSafeEntryRejectsEscapes(t *testing.T) {
	for _, p := range []string{"", "/etc/passwd", "../x", "a/../b", ".git/config", "a/.GIT/x", "C:/x", "a//b"} {
		if safeEntry(p) {
			t.Errorf("accepted %q", p)
		}
	}
	if !safeEntry("src/ü ber/a.go") {
		t.Error("rejected a normal path")
	}
}
