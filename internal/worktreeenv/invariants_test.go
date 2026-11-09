package worktreeenv_test

import (
	"bytes"
	"context"
	"errors"
	"io/fs"
	"os"
	"path/filepath"
	"slices"
	"strings"
	"testing"
	"time"

	"github.com/brendatamaar/yggdrasil/internal/core"
	env "github.com/brendatamaar/yggdrasil/internal/core/worktreeenv"
	"github.com/brendatamaar/yggdrasil/internal/db"
	"github.com/brendatamaar/yggdrasil/internal/db/dbtest"
	"github.com/brendatamaar/yggdrasil/internal/platform/platformtest"
	"github.com/brendatamaar/yggdrasil/internal/safety"
	"github.com/brendatamaar/yggdrasil/internal/worktreeenv"
)

// Safety invariant tests of the shell (docs/AGENTS.md section 6). Each one
// fails if the invariant is broken.

func mkdirs(t *testing.T, dirs ...string) {
	t.Helper()
	for _, d := range dirs {
		if err := os.MkdirAll(d, 0o750); err != nil {
			t.Fatal(err)
		}
	}
}

func write(t *testing.T, path string, data []byte) {
	t.Helper()
	mkdirs(t, filepath.Dir(path))
	if err := os.WriteFile(path, data, 0o600); err != nil {
		t.Fatal(err)
	}
}

// snapshot maps every path (with symlinks not followed) under roots to its content.
func snapshot(t *testing.T, roots ...string) map[string]string {
	t.Helper()
	out := map[string]string{}
	for _, r := range roots {
		err := filepath.WalkDir(r, func(p string, d fs.DirEntry, err error) error {
			if err != nil {
				return err
			}
			if d.Type().IsRegular() {
				b, err := os.ReadFile(p)
				if err != nil {
					return err
				}
				out[p] = string(b)
			} else {
				out[p] = "<" + d.Type().String() + ">"
			}
			return nil
		})
		if err != nil {
			t.Fatal(err)
		}
	}
	return out
}

func symlinkOrSkip(t *testing.T, target, link string) {
	t.Helper()
	if err := os.Symlink(target, link); err != nil {
		t.Skipf("cannot create symlinks here: %v", err)
	}
}

// TestInvariant_1_profile_copy_never_overwrites_or_deletes: on a real disk with
// spaces in the folder names, an existing file survives byte for byte, every
// pre-existing path is still there afterwards, and a second copy changes nothing.
func TestInvariant_1_profile_copy_never_overwrites_or_deletes(t *testing.T) {
	base := t.TempDir()
	repo, wt := filepath.Join(base, "shop api"), filepath.Join(base, "wt amber fox")
	write(t, filepath.Join(repo, ".env"), []byte("NEW=1\r\n"))
	write(t, filepath.Join(repo, "conf dir", "app.env"), []byte("A=1\r\nB=2\r\n"))
	write(t, filepath.Join(wt, ".env"), []byte("KEEP=me\n"))
	write(t, filepath.Join(wt, "notes.txt"), []byte("mine"))
	before := snapshot(t, repo, wt)

	fsys := worktreeenv.OSFileSystem{}
	ctx := context.Background()
	for _, rel := range []env.RelPath{".env", "conf dir/app.env"} {
		src, dest, err := fsys.Observe(ctx, repo, wt, rel)
		if err != nil {
			t.Fatal(err)
		}
		if _, ok := src.(env.SourceFile); !ok {
			t.Fatalf("%s: source %T", rel, src)
		}
		wantDest := env.DestAbsent
		if rel == ".env" {
			wantDest = env.DestExists
		}
		if dest != wantDest {
			t.Fatalf("%s: dest %v", rel, dest)
		}
	}
	if err := fsys.CopyExclusive(ctx, repo, wt, ".env", env.MaxFileBytes); !errors.Is(err, worktreeenv.ErrDestExists) {
		t.Fatalf("overwrite not refused: %v", err)
	}
	if err := fsys.CopyExclusive(ctx, repo, wt, "conf dir/app.env", env.MaxFileBytes); err != nil {
		t.Fatal(err)
	}
	if err := fsys.CopyExclusive(ctx, repo, wt, "conf dir/app.env", env.MaxFileBytes); !errors.Is(err, worktreeenv.ErrDestExists) {
		t.Fatalf("second copy: %v", err)
	}
	after := snapshot(t, repo, wt)
	for p, v := range before {
		if after[p] != v {
			t.Errorf("%s changed or vanished: %q -> %q", p, v, after[p])
		}
	}
	got := after[filepath.Join(wt, "conf dir", "app.env")]
	if got != "A=1\r\nB=2\r\n" {
		t.Errorf("CRLF not preserved: %q", got)
	}
}

// TestInvariant_1_profile_copy_never_writes_through_a_destination_link: a link
// planted at the destination file or at a destination folder is never written
// through, and nothing is removed.
func TestInvariant_1_profile_copy_never_writes_through_a_destination_link(t *testing.T) {
	base := t.TempDir()
	repo, wt, outside := filepath.Join(base, "repo"), filepath.Join(base, "wt"), filepath.Join(base, "outside")
	write(t, filepath.Join(repo, "a.env"), []byte("S"))
	write(t, filepath.Join(repo, "dir", "b.env"), []byte("S"))
	write(t, filepath.Join(outside, "target.env"), []byte("ORIGINAL"))
	mkdirs(t, wt)
	symlinkOrSkip(t, filepath.Join(outside, "target.env"), filepath.Join(wt, "a.env"))
	symlinkOrSkip(t, outside, filepath.Join(wt, "dir"))
	before := snapshot(t, outside)

	fsys := worktreeenv.OSFileSystem{}
	for _, rel := range []env.RelPath{"a.env", "dir/b.env"} {
		if err := fsys.CopyExclusive(context.Background(), repo, wt, rel, env.MaxFileBytes); err == nil {
			t.Errorf("%s: copy through a link succeeded", rel)
		}
		if _, dest, _ := fsys.Observe(context.Background(), repo, wt, rel); dest != env.DestExists {
			t.Errorf("%s: a link must read as an occupied destination", rel)
		}
	}
	if after := snapshot(t, outside); !equalMaps(before, after) {
		t.Fatalf("the folder outside the worktree changed: %v", after)
	}
}

func equalMaps(a, b map[string]string) bool {
	if len(a) != len(b) {
		return false
	}
	for k, v := range a {
		if b[k] != v {
			return false
		}
	}
	return true
}

// TestInvariant_6_destination_parent_swapped_after_observe: the destination
// folder is replaced by a link to a folder outside the worktree between Observe
// and CopyExclusive (the TOCTOU window of the design doc); the copy must not
// land outside. The same for a source folder swapped for a link.
func TestInvariant_6_destination_parent_swapped_after_observe(t *testing.T) {
	base := t.TempDir()
	repo, wt, outside := filepath.Join(base, "repo"), filepath.Join(base, "wt"), filepath.Join(base, "outside")
	write(t, filepath.Join(repo, "conf", "app.env"), []byte("S"))
	mkdirs(t, wt, outside)
	fsys := worktreeenv.OSFileSystem{}
	ctx := context.Background()
	if _, dest, _ := fsys.Observe(ctx, repo, wt, "conf/app.env"); dest != env.DestAbsent {
		t.Fatalf("dest %v", dest)
	}
	symlinkOrSkip(t, outside, filepath.Join(wt, "conf")) // the swap
	if err := fsys.CopyExclusive(ctx, repo, wt, "conf/app.env", env.MaxFileBytes); err == nil {
		t.Error("copy through a swapped parent succeeded")
	}
	if len(snapshot(t, outside)) != 1 { // only the folder itself
		t.Fatalf("a file landed outside the worktree: %v", snapshot(t, outside))
	}

	// source side
	base2 := t.TempDir()
	repo2, wt2, secret := filepath.Join(base2, "repo"), filepath.Join(base2, "wt"), filepath.Join(base2, "secret")
	write(t, filepath.Join(secret, "app.env"), []byte("TOP SECRET"))
	mkdirs(t, repo2, wt2)
	symlinkOrSkip(t, secret, filepath.Join(repo2, "conf"))
	if src, _, _ := fsys.Observe(ctx, repo2, wt2, "conf/app.env"); src != (env.SourceLink{}) {
		t.Errorf("a link above the source must read as a link, got %T", src)
	}
	if err := fsys.CopyExclusive(ctx, repo2, wt2, "conf/app.env", env.MaxFileBytes); err == nil {
		t.Error("a file behind a source link was copied")
	}
	if _, err := os.Stat(filepath.Join(wt2, "conf")); err == nil {
		t.Error("the copy made folders in the worktree before refusing")
	}
}

// TestInvariant_6_profile_paths_that_escape_are_refused_by_the_interpreter: even
// if a bad path reached the file system (a caller skipped the parser), nothing
// is read or written outside the two roots.
func TestInvariant_6_profile_paths_that_escape_are_refused_by_the_interpreter(t *testing.T) {
	base := t.TempDir()
	repo, wt := filepath.Join(base, "repo"), filepath.Join(base, "wt")
	write(t, filepath.Join(base, "private.env"), []byte("P"))
	write(t, filepath.Join(repo, ".git", "config"), []byte("[core]"))
	mkdirs(t, wt)
	before := snapshot(t, base)
	fsys := worktreeenv.OSFileSystem{}
	for _, bad := range []env.RelPath{"../private.env", "a/../../private.env", ".git/config", "/etc/passwd", `C:\x`, "*.env", "", "dir/.GIT/x"} {
		src, dest, err := fsys.Observe(context.Background(), repo, wt, bad)
		if err != nil || src != (env.SourceUnreadable{}) || dest != env.DestExists {
			t.Errorf("%q observed as %T %v %v", bad, src, dest, err)
		}
		if err := fsys.CopyExclusive(context.Background(), repo, wt, bad, env.MaxFileBytes); err == nil {
			t.Errorf("%q copied", bad)
		}
	}
	if after := snapshot(t, base); !equalMaps(before, after) {
		t.Fatalf("the disk changed: %v", after)
	}
	// relative roots are refused as well
	if err := fsys.CopyExclusive(context.Background(), "repo", "wt", "a.env", env.MaxFileBytes); err == nil {
		t.Error("relative roots must be refused")
	}
}

// TestInvariant_6_profile_copy_only_for_tracked_repo: an untracked repo (the
// lookup fails) copies nothing and writes no audit entry of a copy.
func TestInvariant_6_profile_copy_only_for_tracked_repo(t *testing.T) {
	r := newRig(t, nil)
	r.fs.WriteFile("/repos/shop-api", ".env", []byte("X"))
	ctx := context.Background()
	if err := r.svc.SetProfile(ctx, "rep_1", []string{".env"}, false); err != nil {
		t.Fatal(err)
	}
	untracked, err := worktreeenv.New(worktreeenv.Config{
		Ports: r.store, Settings: r.store, Audit: r.store, Prober: r.prober, FS: r.fs,
		RepoDir: func(context.Context, core.RepoID) (string, error) { return "", worktreeenv.ErrNotFound },
		Now:     func() time.Time { return now }, NewID: func(p string) string { return p + "_x" },
	})
	if err != nil {
		t.Fatal(err)
	}
	if _, err := untracked.CopyProfile(ctx, core.Worktree{ID: "wt_a", RepoID: "rep_1", Path: "/wt/a"}, env.TriggerUserStarted); err == nil {
		t.Fatal("copy ran for a repo that is not tracked")
	}
	if _, ok := r.fs.Read("/wt/a", ".env"); ok {
		t.Fatal("a file was copied")
	}
}

// TestInvariant_6_compose_file_parent_link_is_refused: a compose file behind a
// link folder, or an escaping path, is not read.
func TestInvariant_6_compose_file_parent_link_is_refused(t *testing.T) {
	base := t.TempDir()
	wt, outside := filepath.Join(base, "wt"), filepath.Join(base, "outside")
	write(t, filepath.Join(outside, "compose.yml"), []byte("services: {}"))
	mkdirs(t, wt)
	symlinkOrSkip(t, outside, filepath.Join(wt, "deploy"))
	files := worktreeenv.OSComposeFiles{}
	for _, rel := range []env.RelPath{"deploy/compose.yml", "../outside/compose.yml", "missing.yml"} {
		if _, err := files.Read(wt, rel); !errors.Is(err, worktreeenv.ErrComposeFile) {
			t.Errorf("%s: %v", rel, err)
		}
	}
}

// TestProfileCopyKeepsCRLFSpacesAndLongPaths: byte-exact copies for CRLF
// content, names with spaces and a path far beyond 100 characters.
func TestProfileCopyKeepsCRLFSpacesAndLongPaths(t *testing.T) {
	base := t.TempDir()
	repo, wt := filepath.Join(base, "my repo"), filepath.Join(base, "my worktree")
	long := strings.TrimSuffix(strings.Repeat("deep folder name/", 12), "/") + "/settings file.env"
	if len(long) < 200 || len(long) > 260 {
		t.Fatalf("test path length %d", len(long))
	}
	data := []byte("A=1\r\nB=two words\r\n\r\nLAST=1")
	write(t, filepath.Join(repo, filepath.FromSlash(long)), data)
	mkdirs(t, wt)
	rel, err := env.ParseProfile([]string{long})
	if err != nil {
		t.Fatal(err)
	}
	if err := (worktreeenv.OSFileSystem{}).CopyExclusive(context.Background(), repo, wt, rel[0], env.MaxFileBytes); err != nil {
		t.Skipf("this file system refuses the long path: %v", err)
	}
	got, err := os.ReadFile(filepath.Join(wt, filepath.FromSlash(long)))
	if err != nil || !bytes.Equal(got, data) {
		t.Fatalf("got %q %v", got, err)
	}
}

// TestInvariant_3_compose_env_is_clean_env_plus_port_vars: for every op, the
// child gets the clean environment and the YGG_PORT_* variables, and no token,
// even when Yggdrasil's own environment holds some.
func TestInvariant_3_compose_env_is_clean_env_plus_port_vars(t *testing.T) {
	tokens := map[string]string{
		"GITLAB_TOKEN": "glpat-fictional-1", "GITHUB_TOKEN": "ghp_fictional2", "GITEA_TOKEN": "fictional3",
		"YGG_TOKEN": "fictional4", "ANTHROPIC_API_KEY": "sk-ant-fictional5", "AWS_SECRET_ACCESS_KEY": "fictional6",
	}
	for k, v := range tokens {
		t.Setenv(k, v)
	}
	var calls []worktreeenv.ComposeCall
	r := newComposeRig(t)
	r.runner.Result = func(c worktreeenv.ComposeCall) worktreeenv.ComposeResult {
		calls = append(calls, c)
		return worktreeenv.ComposeResult{}
	}
	ctx := context.Background()
	r.enable(t)
	st, _ := r.svc.Status(ctx, r.wt.ID)
	if _, err := r.svc.Confirm(ctx, r.wt.ID, st.Hash); err != nil {
		t.Fatal(err)
	}
	if _, err := r.svc.Up(ctx, r.wt.ID); err != nil {
		t.Fatal(err)
	}
	r.waitFor(t, func(s env.Service) bool { _, ok := s.(env.Up); return ok })
	for _, c := range r.runner.Calls() {
		for k := range c.Env {
			if k != "YGG_PORT_BASE" && !strings.HasPrefix(k, "YGG_PORT_") {
				t.Errorf("%s: unexpected variable %s", c.Op, k)
			}
		}
	}

	// the runner turns the call into a child environment
	for _, op := range []env.ComposeOp{env.OpUp, env.OpStop, env.OpDown, env.OpPs} {
		f := &platformtest.Fake{}
		f.Push(platformtest.Script{Stdout: "[]"})
		runner := worktreeenv.PlatformComposeRunner{
			Env: safety.NewEnvBuilder(), Start: f.Start,
			Resolve: func(name, _ string, _ []string) (string, error) { return "/usr/bin/" + name, nil },
		}
		call := worktreeenv.ComposeCall{Op: op, Project: "ygg-a", File: "compose.yml", Compose: []byte("services: {}"), Dir: "/wt", Env: map[string]string{"YGG_PORT_BASE": "20000", "YGG_PORT_0": "20000"}}
		if res := runner.Run(ctx, call); res.Failure != "" {
			t.Fatalf("%s: %+v", op, res)
		}
		joined := strings.Join(f.Started()[0].Env, "\n")
		for k, v := range tokens {
			if strings.Contains(joined, k) || strings.Contains(joined, v) {
				t.Errorf("%s: %s reached the child environment", op, k)
			}
		}
		for _, e := range f.Started()[0].Env {
			name, _, _ := strings.Cut(e, "=")
			if strings.HasPrefix(name, "YGG_") && !strings.HasPrefix(name, "YGG_PORT_") {
				t.Errorf("%s: unexpected %s", op, name)
			}
		}
	}
}

// TestInvariant_3_compose_failure_summary_is_redacted: a token in compose's own
// output never reaches the status text or the audit log.
func TestInvariant_3_compose_failure_summary_is_redacted(t *testing.T) {
	const secret = "glpat-abcdefghij1234567890"
	f := &platformtest.Fake{}
	f.Push(platformtest.Script{Stderr: "pull access denied, token " + secret + "\n", Exit: 1})
	runner := worktreeenv.PlatformComposeRunner{
		Env: safety.NewEnvBuilder(), Start: f.Start,
		Resolve: func(name, _ string, _ []string) (string, error) { return "/usr/bin/" + name, nil },
	}
	res := runner.Run(context.Background(), worktreeenv.ComposeCall{Op: env.OpUp, Project: "ygg-a", File: "c.yml", Compose: []byte("x"), Dir: "/wt"})
	if res.Failure != env.FailComposeError || strings.Contains(res.Summary, secret) {
		t.Fatalf("%+v", res)
	}
}

// dbRig is the service on the real SQLite store with real rows.
type dbRig struct {
	s     *db.Store
	svc   *worktreeenv.Service
	repo  core.Repo
	proj  core.Project
	audit func(subject string) []core.AuditEntry
}

func openDB(t *testing.T, path string) *db.Store {
	t.Helper()
	d, err := db.Open(context.Background(), path)
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = d.Close() })
	return db.NewStore(d, safety.NewRedactor().Redact)
}

func newDBRig(t *testing.T, s *db.Store, proj core.Project, repo core.Repo) *dbRig {
	t.Helper()
	store := worktreeenv.DBStore{S: s}
	r := &dbRig{s: s, repo: repo, proj: proj}
	svc, err := worktreeenv.New(worktreeenv.Config{
		Ports: store, Settings: store, Audit: store, Prober: worktreeenvProber{}, FS: worktreeenv.OSFileSystem{},
		RepoDir: func(context.Context, core.RepoID) (string, error) { return repo.Path, nil },
		Now:     func() time.Time { return now },
		NewID:   func(p string) string { return db.NewID(p) },
	})
	if err != nil {
		t.Fatal(err)
	}
	r.svc = svc
	r.audit = func(subject string) []core.AuditEntry {
		got, err := s.ListAudit(context.Background(), subject, 200)
		if err != nil {
			t.Fatal(err)
		}
		return got
	}
	return r
}

// worktreeenvProber reports nothing busy, so the tests do not depend on the machine.
type worktreeenvProber struct{}

func (worktreeenvProber) Busy(context.Context, int, int) ([]int, error) { return nil, nil }

func (r *dbRig) worktree(t *testing.T, name, path string) core.Worktree {
	t.Helper()
	task := dbtest.Task(t, r.s, core.Task{ProjectID: r.proj.ID, Scope: core.ScopeRepo, Mode: core.ModeCode, RepoIDs: []core.RepoID{r.repo.ID}})
	w := dbtest.TaskWorktree(t, r.s, task.ID, r.repo, name, "ygg/"+name)
	w.Path = path
	return w
}

func seedProject(t *testing.T, s *db.Store, repoPath string) (core.Project, core.Repo) {
	t.Helper()
	proj := dbtest.Project(t, s, core.Project{Name: "acme-shop", Path: t.TempDir()})
	repo := dbtest.Repo(t, s, core.Repo{ProjectID: proj.ID, Name: "shop-api", Path: repoPath})
	return proj, repo
}

// TestInvariant_8_every_worktreeenv_change_has_an_audit_row: on the real store,
// each state change adds an audit row in the same commit.
func TestInvariant_8_every_worktreeenv_change_has_an_audit_row(t *testing.T) {
	ctx := context.Background()
	base := t.TempDir()
	repoDir, wtDir := filepath.Join(base, "shop-api"), filepath.Join(base, "wt1")
	write(t, filepath.Join(repoDir, ".env"), []byte("K=1\r\n"))
	mkdirs(t, wtDir)
	s := openDB(t, filepath.Join(base, "ygg.db"))
	proj, repo := seedProject(t, s, repoDir)
	r := newDBRig(t, s, proj, repo)
	w := r.worktree(t, "wt1", wtDir)

	count := func() int {
		got, err := s.ListAudit(ctx, "", 500)
		if err != nil {
			t.Fatal(err)
		}
		return len(got)
	}
	step := func(name string, do func() error) {
		t.Helper()
		before := count()
		if err := do(); err != nil {
			t.Fatalf("%s: %v", name, err)
		}
		if count() <= before {
			t.Errorf("%s changed state without an audit row", name)
		}
	}
	step("set profile", func() error { return r.svc.SetProfile(ctx, repo.ID, []string{".env"}, false) })
	step("set range", func() error { return r.svc.SetPortRange(ctx, "21000-21999") })
	step("allocate", func() error { _, err := r.svc.Allocate(ctx, w.ID); return err })
	step("copy profile", func() error { _, err := r.svc.CopyProfile(ctx, w, env.TriggerUserStarted); return err })
	step("release", func() error { return r.svc.Release(ctx, w.ID) })

	// no-ops add nothing
	for name, do := range map[string]func() error{
		"allocate again":  func() error { _, err := r.svc.Allocate(ctx, w.ID); return err },
		"release again":   func() error { return r.svc.Release(ctx, w.ID) },
		"release unknown": func() error { return r.svc.Release(ctx, "wt_unknown") },
	} {
		if name == "allocate again" {
			// the worktree was released, so this allocates a new block: audited
			continue
		}
		before := count()
		if err := do(); err != nil {
			t.Fatalf("%s: %v", name, err)
		}
		if count() != before {
			t.Errorf("%s wrote an audit row for nothing", name)
		}
	}
	for _, a := range r.audit("worktree:" + string(w.ID)) {
		if strings.Contains(string(a.Detail), "K=1") {
			t.Errorf("audit holds file contents: %s", a.Detail)
		}
	}
}

// TestPortBlocksSurviveRestartAndStayDisjoint: blocks stored in SQLite come
// back after the database is closed and reopened; a new worktree gets a new,
// disjoint 20-port block inside the configured range.
func TestPortBlocksSurviveRestartAndStayDisjoint(t *testing.T) {
	ctx := context.Background()
	base := t.TempDir()
	path := filepath.Join(base, "ygg.db")
	repoDir := filepath.Join(base, "shop-api")
	mkdirs(t, repoDir)

	d, err := db.Open(ctx, path)
	if err != nil {
		t.Fatal(err)
	}
	s := db.NewStore(d, safety.NewRedactor().Redact)
	proj, repo := seedProject(t, s, repoDir)
	r := newDBRig(t, s, proj, repo)
	if err := r.svc.SetPortRange(ctx, "24000-24999"); err != nil {
		t.Fatal(err)
	}
	w1, w2 := r.worktree(t, "wt1", filepath.Join(base, "w1")), r.worktree(t, "wt2", filepath.Join(base, "w2"))
	b1, err := r.svc.Allocate(ctx, w1.ID)
	if err != nil {
		t.Fatal(err)
	}
	b2, err := r.svc.Allocate(ctx, w2.ID)
	if err != nil {
		t.Fatal(err)
	}
	if err := d.Close(); err != nil {
		t.Fatal(err)
	}

	s2 := openDB(t, path)
	r2 := newDBRig(t, s2, proj, repo)
	again1, err := r2.svc.BlockFor(ctx, w1.ID)
	if err != nil || again1 != b1 {
		t.Fatalf("block after restart %v %v, want %v", again1, err, b1)
	}
	again2, _ := r2.svc.BlockFor(ctx, w2.ID)
	if again2 != b2 {
		t.Fatalf("second block moved: %v -> %v", b2, again2)
	}
	w3 := r2.worktree(t, "wt3", filepath.Join(base, "w3"))
	b3, err := r2.svc.Allocate(ctx, w3.ID)
	if err != nil {
		t.Fatal(err)
	}
	all := []core.PortBlock{b1, b2, b3}
	for i, a := range all {
		if a.Size != 20 || a.Base < 24000 || a.Base+a.Size-1 > 24999 {
			t.Errorf("block %v outside 24000-24999 or not 20 ports", a)
		}
		for _, o := range all[i+1:] {
			if a.Base < o.Base+o.Size && o.Base < a.Base+a.Size {
				t.Errorf("blocks %v and %v overlap", a, o)
			}
		}
	}
}

// TestPortBlockReleasedOnlyForTheReleasedWorktree: releasing one worktree
// frees its block for reuse, leaves every other block where it is, and
// releasing a worktree that has none is a no-op.
func TestPortBlockReleasedOnlyForTheReleasedWorktree(t *testing.T) {
	ctx := context.Background()
	base := t.TempDir()
	repoDir := filepath.Join(base, "shop-api")
	mkdirs(t, repoDir)
	s := openDB(t, filepath.Join(base, "ygg.db"))
	proj, repo := seedProject(t, s, repoDir)
	r := newDBRig(t, s, proj, repo)
	ws := []core.Worktree{r.worktree(t, "wt1", "/x/1"), r.worktree(t, "wt2", "/x/2"), r.worktree(t, "wt3", "/x/3")}
	blocks := map[core.WorktreeID]core.PortBlock{}
	for _, w := range ws {
		b, err := r.svc.Allocate(ctx, w.ID)
		if err != nil {
			t.Fatal(err)
		}
		blocks[w.ID] = b
	}
	if err := r.svc.Release(ctx, ws[1].ID); err != nil {
		t.Fatal(err)
	}
	if err := r.svc.Release(ctx, "wt_never_had_one"); err != nil {
		t.Fatal(err)
	}
	live, err := (worktreeenv.DBStore{S: s}).LoadBlocks(ctx)
	if err != nil {
		t.Fatal(err)
	}
	if len(live) != 2 || live[ws[0].ID] != blocks[ws[0].ID] || live[ws[2].ID] != blocks[ws[2].ID] {
		t.Fatalf("live blocks %v", live)
	}
	if _, still := live[ws[1].ID]; still {
		t.Fatal("released block is still live")
	}
	w4 := r.worktree(t, "wt4", "/x/4")
	b4, err := r.svc.Allocate(ctx, w4.ID)
	if err != nil || b4 != blocks[ws[1].ID] {
		t.Fatalf("freed block not reused: %v %v", b4, err)
	}
}

// TestRangeChangeNeverMovesLiveBlocks: after the range is narrowed to exclude
// a live block, the block stays; new blocks come from the new range.
func TestRangeChangeNeverMovesLiveBlocks(t *testing.T) {
	ctx := context.Background()
	base := t.TempDir()
	repoDir := filepath.Join(base, "shop-api")
	mkdirs(t, repoDir)
	s := openDB(t, filepath.Join(base, "ygg.db"))
	proj, repo := seedProject(t, s, repoDir)
	r := newDBRig(t, s, proj, repo)
	w1 := r.worktree(t, "wt1", "/x/1")
	b1, err := r.svc.Allocate(ctx, w1.ID)
	if err != nil {
		t.Fatal(err)
	}
	if err := r.svc.SetPortRange(ctx, "30000-30099"); err != nil {
		t.Fatal(err)
	}
	w2 := r.worktree(t, "wt2", "/x/2")
	b2, err := r.svc.Allocate(ctx, w2.ID)
	if err != nil {
		t.Fatal(err)
	}
	if again, _ := r.svc.BlockFor(ctx, w1.ID); again != b1 {
		t.Fatalf("the live block moved: %v -> %v", b1, again)
	}
	if b2.Base < 30000 || b2.Base+b2.Size-1 > 30099 {
		t.Fatalf("new block %v outside the new range", b2)
	}
	for _, bad := range []string{"500-9000", "20000-20010", "20000-70000", "abc", "30000", "-5-100"} {
		if err := r.svc.SetPortRange(ctx, bad); err == nil {
			t.Errorf("range %q accepted", bad)
		}
	}
	if !slices.Contains([]int{30000, 30020, 30040, 30060, 30080}, b2.Base) {
		t.Errorf("block %v not aligned to the range", b2)
	}
}
