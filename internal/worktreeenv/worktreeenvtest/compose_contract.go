package worktreeenvtest

import (
	"context"
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/brendatamaar/yggdrasil/internal/core"
	env "github.com/brendatamaar/yggdrasil/internal/core/worktreeenv"
	"github.com/brendatamaar/yggdrasil/internal/worktreeenv"
)

// ComposeRunnerEnv is one ComposeRunner under test. The scripted runner of a
// real implementation answers from Script; a fake answers from the same
// outcomes through its Result function.
type ComposeRunnerEnv struct {
	// Runner is a working runner, or nil when this implementation cannot run
	// here. Cases that need it are skipped.
	Runner worktreeenv.ComposeRunner
	// NoDocker is a runner on a machine where Docker is not installed.
	NoDocker worktreeenv.ComposeRunner
	// Dir is an existing folder to run in.
	Dir string
	// Script, if set, makes the next run of Runner end the way described. A
	// nil Script means the implementation cannot be scripted (the contract
	// then only checks what a real, unscripted Docker answers).
	Script func(Outcome)
}

// Outcome describes how one scripted run ends.
type Outcome struct {
	Exit   int
	Stdout string
	Stderr string
	// Block, if true, makes the run wait until its context is cancelled.
	Block bool
}

// ComposeRunnerContract runs the ComposeRunner contract against a fake and the
// real runner.
func ComposeRunnerContract(t *testing.T, mk func(t *testing.T) ComposeRunnerEnv) {
	t.Helper()
	ctx := context.Background()
	call := func(e ComposeRunnerEnv, op env.ComposeOp) worktreeenv.ComposeCall {
		return worktreeenv.ComposeCall{Op: op, Project: "ygg-contract", File: "compose.yml", Dir: e.Dir,
			Compose: []byte("services: {}\n"),
			Env:     map[string]string{"YGG_PORT_BASE": "20000", "YGG_PORT_0": "20000"}}
	}

	t.Run("an operation outside the closed set is refused", func(t *testing.T) {
		e := mk(t)
		for _, r := range []worktreeenv.ComposeRunner{e.NoDocker, e.Runner} {
			if r == nil {
				continue
			}
			c := call(e, "down -v")
			if res := r.Run(ctx, c); res.Failure != env.FailComposeError {
				t.Fatalf("got %+v", res)
			}
		}
	})

	t.Run("missing docker is DockerMissing with a plain message", func(t *testing.T) {
		e := mk(t)
		for _, op := range []env.ComposeOp{env.OpUp, env.OpStop, env.OpDown, env.OpPs} {
			res := e.NoDocker.Run(ctx, call(e, op))
			if res.Failure != env.FailDockerMissing || res.Summary != worktreeenv.MsgDockerMissing {
				t.Fatalf("%s: got %+v", op, res)
			}
		}
	})

	t.Run("a failed run reports a failure and a short summary", func(t *testing.T) {
		e := mk(t)
		if e.Runner == nil {
			t.Skip("Docker is not available here")
		}
		if e.Script != nil {
			e.Script(Outcome{Exit: 1, Stderr: "yaml: line 3: mapping values are not allowed\n"})
		}
		res := e.Runner.Run(ctx, call(e, env.OpUp))
		if res.Failure == "" || res.Summary == "" || len(res.Summary) > 600 {
			t.Fatalf("got %+v", res)
		}
	})

	t.Run("a run that succeeds has no failure and keeps output only for ps", func(t *testing.T) {
		e := mk(t)
		if e.Runner == nil || e.Script == nil {
			t.Skip("needs a scripted runner")
		}
		for _, op := range []env.ComposeOp{env.OpUp, env.OpStop, env.OpDown} {
			e.Script(Outcome{Stdout: "done\n"})
			res := e.Runner.Run(ctx, call(e, op))
			if res.Failure != "" || res.Summary != "" || len(res.Output) != 0 {
				t.Fatalf("%s: got %+v", op, res)
			}
		}
	})

	t.Run("ps output is returned and parses", func(t *testing.T) {
		e := mk(t)
		if e.Runner == nil || e.Script == nil {
			t.Skip("needs a scripted runner")
		}
		e.Script(Outcome{Stdout: `{"State":"running"}` + "\n"})
		res := e.Runner.Run(ctx, call(e, env.OpPs))
		if res.Failure != "" {
			t.Fatalf("got %+v", res)
		}
		if got, err := env.ParsePs(res.Output); err != nil || got != env.PsRunning {
			t.Fatalf("ps = %v, %v", got, err)
		}
	})

	t.Run("a stopped daemon is DaemonDown", func(t *testing.T) {
		e := mk(t)
		if e.Runner == nil || e.Script == nil {
			t.Skip("needs a scripted runner")
		}
		e.Script(Outcome{Exit: 1, Stderr: "Cannot connect to the Docker daemon at unix:///var/run/docker.sock. Is the docker daemon running?"})
		res := e.Runner.Run(ctx, call(e, env.OpUp))
		if res.Failure != env.FailDaemonDown || res.Summary != worktreeenv.MsgDaemonDown {
			t.Fatalf("got %+v", res)
		}
	})

	t.Run("a token in the output never reaches the summary", func(t *testing.T) {
		e := mk(t)
		if e.Runner == nil || e.Script == nil {
			t.Skip("needs a scripted runner")
		}
		e.Script(Outcome{Exit: 1, Stderr: "pull failed: Authorization: Bearer abcdefghijklmnop1234567890"})
		res := e.Runner.Run(ctx, call(e, env.OpUp))
		if res.Failure == "" || strings.Contains(res.Summary, "abcdefghijklmnop1234567890") {
			t.Fatalf("got %+v", res)
		}
	})

	t.Run("cancelling the context ends the run with a failure", func(t *testing.T) {
		e := mk(t)
		if e.Runner == nil || e.Script == nil {
			t.Skip("needs a scripted runner")
		}
		e.Script(Outcome{Block: true})
		cctx, cancel := context.WithCancel(ctx)
		done := make(chan worktreeenv.ComposeResult, 1)
		go func() { done <- e.Runner.Run(cctx, call(e, env.OpUp)) }()
		time.Sleep(50 * time.Millisecond)
		cancel()
		select {
		case res := <-done:
			if res.Failure == "" {
				t.Fatalf("a cancelled run reported success: %+v", res)
			}
		case <-time.After(5 * time.Second):
			t.Fatal("the run did not stop when its context was cancelled")
		}
	})
}

// ComposeFilesEnv is one ComposeFiles under test. Put places a regular file;
// Link, if set, makes rel a link to a file outside root (skip when the
// implementation or the platform cannot).
type ComposeFilesEnv struct {
	Files worktreeenv.ComposeFiles
	Root  string
	Put   func(rel env.RelPath, data []byte)
	Link  func(rel env.RelPath) bool
	// Dir, if set, makes rel a folder.
	Dir func(rel env.RelPath) bool
}

// ComposeFilesContract runs the ComposeFiles contract against the fake and the
// disk implementation.
func ComposeFilesContract(t *testing.T, mk func(t *testing.T) ComposeFilesEnv) {
	t.Helper()
	t.Run("a regular file is read back", func(t *testing.T) {
		e := mk(t)
		e.Put("a/compose.yml", []byte("services: {}"))
		got, err := e.Files.Read(e.Root, "a/compose.yml")
		if err != nil || string(got) != "services: {}" {
			t.Fatalf("%q %v", got, err)
		}
		got, ok, err := e.Files.ReadOptional(e.Root, "a/compose.yml")
		if err != nil || !ok || string(got) != "services: {}" {
			t.Fatalf("%q %v %v", got, ok, err)
		}
	})
	t.Run("a missing file is ErrComposeFile for Read and absent for ReadOptional", func(t *testing.T) {
		e := mk(t)
		if _, err := e.Files.Read(e.Root, "nope.yml"); !errors.Is(err, worktreeenv.ErrComposeFile) {
			t.Fatalf("Read: %v", err)
		}
		if b, ok, err := e.Files.ReadOptional(e.Root, "nope/.env"); err != nil || ok || b != nil {
			t.Fatalf("ReadOptional: %q %v %v", b, ok, err)
		}
	})
	t.Run("an invalid path is refused", func(t *testing.T) {
		e := mk(t)
		for _, rel := range []env.RelPath{"", "../x.yml", "/abs.yml", "a/../../x.yml"} {
			if _, err := e.Files.Read(e.Root, rel); !errors.Is(err, worktreeenv.ErrComposeFile) {
				t.Errorf("Read %q: %v", rel, err)
			}
			if _, _, err := e.Files.ReadOptional(e.Root, rel); !errors.Is(err, worktreeenv.ErrComposeFile) {
				t.Errorf("ReadOptional %q: %v", rel, err)
			}
		}
	})
	t.Run("a file over the cap is refused", func(t *testing.T) {
		e := mk(t)
		e.Put("big.yml", make([]byte, worktreeenv.MaxComposeFile+1))
		if _, err := e.Files.Read(e.Root, "big.yml"); !errors.Is(err, worktreeenv.ErrComposeFile) {
			t.Fatalf("Read: %v", err)
		}
		if _, _, err := e.Files.ReadOptional(e.Root, "big.yml"); !errors.Is(err, worktreeenv.ErrComposeFile) {
			t.Fatalf("ReadOptional: %v", err)
		}
	})
	t.Run("links and folders are refused, not followed", func(t *testing.T) {
		e := mk(t)
		if e.Link != nil && e.Link("link.yml") {
			if _, err := e.Files.Read(e.Root, "link.yml"); !errors.Is(err, worktreeenv.ErrComposeFile) {
				t.Errorf("link: %v", err)
			}
			if _, _, err := e.Files.ReadOptional(e.Root, "link.yml"); !errors.Is(err, worktreeenv.ErrComposeFile) {
				t.Errorf("link optional: %v", err)
			}
		}
		if e.Dir != nil && e.Dir("folder") {
			if _, _, err := e.Files.ReadOptional(e.Root, "folder"); !errors.Is(err, worktreeenv.ErrComposeFile) {
				t.Errorf("folder: %v", err)
			}
		}
	})
}

// OSComposeFilesEnv builds a ComposeFilesEnv on a temporary folder.
func OSComposeFilesEnv(t *testing.T, files worktreeenv.ComposeFiles) ComposeFilesEnv {
	t.Helper()
	root, outside := t.TempDir(), t.TempDir()
	return ComposeFilesEnv{
		Files: files, Root: root,
		Put: func(rel env.RelPath, data []byte) {
			p := filepath.Join(root, filepath.FromSlash(string(rel)))
			if err := os.MkdirAll(filepath.Dir(p), 0o750); err != nil {
				t.Fatal(err)
			}
			if err := os.WriteFile(p, data, 0o600); err != nil {
				t.Fatal(err)
			}
		},
		Link: func(rel env.RelPath) bool {
			target := filepath.Join(outside, "target.yml")
			if err := os.WriteFile(target, []byte("services: {}"), 0o600); err != nil {
				t.Fatal(err)
			}
			return os.Symlink(target, filepath.Join(root, filepath.FromSlash(string(rel)))) == nil
		},
		Dir: func(rel env.RelPath) bool {
			return os.MkdirAll(filepath.Join(root, filepath.FromSlash(string(rel))), 0o750) == nil
		},
	}
}

// ComposeStoreEnv is one ComposeStore under test.
type ComposeStoreEnv struct {
	Store worktreeenv.ComposeStore
	Repo  core.RepoID
	// PutProfile changes the repo's profile settings through the same store.
	PutProfile func()
	// Audits is the number of audit entries with the given subject.
	Audits func(subject string) int
}

// ComposeStoreContract runs the ComposeStore contract against the fake and the
// SQLite store.
func ComposeStoreContract(t *testing.T, mk func(t *testing.T) ComposeStoreEnv) {
	t.Helper()
	ctx := context.Background()
	entry := func(e ComposeStoreEnv, id string) core.AuditEntry {
		return core.AuditEntry{ID: core.AuditID(id), At: time.Now().UTC(), Actor: "user", Action: "worktreeenv.compose_settings", Subject: "repo:" + string(e.Repo)}
	}
	set := func(c worktreeenv.ComposeSettings) func(worktreeenv.ComposeSettings) (worktreeenv.ComposeSettings, bool, error) {
		return func(worktreeenv.ComposeSettings) (worktreeenv.ComposeSettings, bool, error) { return c, true, nil }
	}

	t.Run("defaults, write and read back with one audit entry", func(t *testing.T) {
		e := mk(t)
		if got, err := e.Store.ComposeSettings(ctx, e.Repo); err != nil || got != (worktreeenv.ComposeSettings{}) {
			t.Fatalf("defaults: %+v %v", got, err)
		}
		want := worktreeenv.ComposeSettings{Enabled: true, File: "deploy/compose.yml", ConfirmedHash: "abc"}
		if err := e.Store.UpdateComposeSettings(ctx, e.Repo, time.Now().UTC(), entry(e, "aud_c1"), set(want)); err != nil {
			t.Fatal(err)
		}
		if got, err := e.Store.ComposeSettings(ctx, e.Repo); err != nil || got != want {
			t.Fatalf("got %+v %v", got, err)
		}
		if e.Audits != nil && e.Audits("repo:"+string(e.Repo)) != 1 {
			t.Fatal("expected one audit entry")
		}
	})
	t.Run("the callback sees the stored value", func(t *testing.T) {
		e := mk(t)
		_ = e.Store.UpdateComposeSettings(ctx, e.Repo, time.Now().UTC(), entry(e, "aud_c1"), set(worktreeenv.ComposeSettings{ConfirmedHash: "x"}))
		var seen worktreeenv.ComposeSettings
		_ = e.Store.UpdateComposeSettings(ctx, e.Repo, time.Now().UTC(), entry(e, "aud_c2"), func(c worktreeenv.ComposeSettings) (worktreeenv.ComposeSettings, bool, error) {
			seen = c
			return c, false, nil
		})
		if seen.ConfirmedHash != "x" {
			t.Fatalf("saw %+v", seen)
		}
	})
	t.Run("write=false and an error write nothing, not even the audit entry", func(t *testing.T) {
		e := mk(t)
		before := 0
		if e.Audits != nil {
			before = e.Audits("repo:" + string(e.Repo))
		}
		_ = e.Store.UpdateComposeSettings(ctx, e.Repo, time.Now().UTC(), entry(e, "aud_n1"), func(c worktreeenv.ComposeSettings) (worktreeenv.ComposeSettings, bool, error) {
			return worktreeenv.ComposeSettings{Enabled: true, File: "x.yml"}, false, nil
		})
		boom := errors.New("refused")
		err := e.Store.UpdateComposeSettings(ctx, e.Repo, time.Now().UTC(), entry(e, "aud_n2"), func(c worktreeenv.ComposeSettings) (worktreeenv.ComposeSettings, bool, error) {
			return worktreeenv.ComposeSettings{Enabled: true, File: "x.yml"}, true, boom
		})
		if !errors.Is(err, boom) {
			t.Fatalf("error not returned: %v", err)
		}
		if got, _ := e.Store.ComposeSettings(ctx, e.Repo); got != (worktreeenv.ComposeSettings{}) {
			t.Fatalf("stored %+v", got)
		}
		if e.Audits != nil && e.Audits("repo:"+string(e.Repo)) != before {
			t.Fatal("an audit entry was written")
		}
	})
	t.Run("concurrent updates do not lose each other", func(t *testing.T) {
		e := mk(t)
		var wg sync.WaitGroup
		const n = 12
		for i := range n {
			wg.Add(1)
			go func() {
				defer wg.Done()
				err := e.Store.UpdateComposeSettings(ctx, e.Repo, time.Now().UTC(), entry(e, fmt.Sprintf("aud_p%d", i)), func(c worktreeenv.ComposeSettings) (worktreeenv.ComposeSettings, bool, error) {
					c.ConfirmedHash += "x"
					return c, true, nil
				})
				if err != nil {
					t.Error(err)
				}
			}()
		}
		wg.Wait()
		if got, _ := e.Store.ComposeSettings(ctx, e.Repo); len(got.ConfirmedHash) != n {
			t.Fatalf("lost updates: %d of %d survived", len(got.ConfirmedHash), n)
		}
	})
	t.Run("a profile change keeps the compose settings", func(t *testing.T) {
		e := mk(t)
		want := worktreeenv.ComposeSettings{Enabled: true, File: "compose.yml", ConfirmedHash: "h"}
		_ = e.Store.UpdateComposeSettings(ctx, e.Repo, time.Now().UTC(), entry(e, "aud_k1"), set(want))
		if e.PutProfile != nil {
			e.PutProfile()
		}
		if got, _ := e.Store.ComposeSettings(ctx, e.Repo); got != want {
			t.Fatalf("lost the compose settings: %+v", got)
		}
	})
	t.Run("an unknown repo is ErrNotFound", func(t *testing.T) {
		e := mk(t)
		if _, err := e.Store.ComposeSettings(ctx, "rep_unknown"); !errors.Is(err, worktreeenv.ErrNotFound) {
			t.Fatalf("read: %v", err)
		}
		err := e.Store.UpdateComposeSettings(ctx, "rep_unknown", time.Now().UTC(), entry(e, "aud_u1"), set(worktreeenv.ComposeSettings{}))
		if !errors.Is(err, worktreeenv.ErrNotFound) {
			t.Fatalf("update: %v", err)
		}
	})
}
