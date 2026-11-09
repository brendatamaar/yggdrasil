package worktreeenv_test

import (
	"context"
	"errors"
	"slices"
	"strings"
	"sync/atomic"
	"testing"
	"time"

	"github.com/brendatamaar/yggdrasil/internal/core"
	env "github.com/brendatamaar/yggdrasil/internal/core/worktreeenv"
	"github.com/brendatamaar/yggdrasil/internal/worktreeenv"
)

// confirmedDown enables compose, confirms the file as it is and waits for Down.
func (r *composeRig) confirmedDown(t *testing.T) string {
	t.Helper()
	ctx := context.Background()
	r.enable(t)
	st, err := r.svc.Status(ctx, r.wt.ID)
	if err != nil || st.Hash == "" {
		t.Fatalf("%+v %v", st, err)
	}
	if _, err := r.svc.Confirm(ctx, r.wt.ID, st.Hash); err != nil {
		t.Fatal(err)
	}
	r.waitFor(t, func(s env.Service) bool { _, ok := s.(env.Down); return ok })
	return st.Hash
}

func (r *composeRig) up(t *testing.T) {
	t.Helper()
	if _, err := r.svc.Up(context.Background(), r.wt.ID); err != nil {
		t.Fatal(err)
	}
}

func TestComposeConfirmationCoversTheSiblingDotEnv(t *testing.T) {
	ctx := context.Background()
	r := newComposeRig(t)
	r.files.Put(r.wt.Path, ".env", "DB_PASSWORD=one")
	h1 := r.confirmedDown(t)

	// the same compose file without the .env hashes differently
	r2 := newComposeRig(t)
	r2.enable(t)
	st2, _ := r2.svc.Status(ctx, r2.wt.ID)
	if st2.Hash == h1 {
		t.Fatal("the hash does not cover the .env")
	}

	// a run passes the .env and the exact bytes that were hashed
	r.up(t)
	r.waitFor(t, func(s env.Service) bool { _, ok := s.(env.Up); return ok })
	c := r.ops()[0]
	if !c.HasEnvFile || string(c.Compose) != "services: {db: {ports: ['${YGG_PORT_0}:5432']}}" {
		t.Fatalf("call %+v", c)
	}
	if _, err := r.svc.Down(ctx, r.wt.ID); err != nil {
		t.Fatal(err)
	}
	r.waitFor(t, func(s env.Service) bool { _, ok := s.(env.Down); return ok && len(r.ops()) == 2 })

	// an edit of the .env needs a new confirmation
	r.files.Put(r.wt.Path, ".env", "DB_PASSWORD=two")
	before := len(r.ops())
	if _, err := r.svc.Up(ctx, r.wt.ID); !errors.Is(err, worktreeenv.ErrComposeConfirm) {
		t.Fatalf("got %v", err)
	}
	if len(r.ops()) != before {
		t.Fatal("a changed .env was run without confirmation")
	}
	// removing the .env is a change too, and then docker is not told about it
	r.files.Put(r.wt.Path, ".env", "DB_PASSWORD=one")
}

func TestComposeRefusesIncludeExtendsEnvFile(t *testing.T) {
	ctx := context.Background()
	for name, body := range map[string]string{
		"include":  "include:\n  - other.yml\nservices: {}\n",
		"extends":  "services:\n  web:\n    extends:\n      file: base.yml\n",
		"env_file": "services:\n  web:\n    image: x\n    env_file: .env.prod\n",
	} {
		r := newComposeRig(t)
		r.files.Put(r.wt.Path, "compose.yml", body)
		r.enable(t)
		st, err := r.svc.Status(ctx, r.wt.ID)
		if err != nil || st.Hash != "" || !strings.Contains(st.Note, "include:") {
			t.Fatalf("%s: %+v %v", name, st, err)
		}
		for _, f := range []func() error{
			func() error { _, err := r.svc.Confirm(ctx, r.wt.ID, "anything"); return err },
			func() error { _, err := r.svc.Up(ctx, r.wt.ID); return err },
		} {
			if err := f(); !errors.Is(err, env.ErrComposeIndirect) {
				t.Errorf("%s: got %v", name, err)
			}
		}
		if len(r.ops()) != 0 {
			t.Errorf("%s: ran", name)
		}
	}
}

func TestComposeRehashesRightBeforeTheRun(t *testing.T) {
	ctx := context.Background()
	r := newComposeRig(t)
	r.confirmedDown(t)
	base := r.files.Reads(r.wt.Path, "compose.yml")
	// Up reads the file in the status call, in act, and then in the goroutine
	// that runs docker (base+3): the file changes before that last read.
	r.files.Hook = func(rel env.RelPath, n int) {
		if rel == "compose.yml" && n == base+3 {
			r.files.Put(r.wt.Path, "compose.yml", "services: {evil: {volumes: ['/:/host']}}")
		}
	}
	r.up(t)
	st := r.waitFor(t, func(s env.Service) bool { _, ok := s.(env.NeedsConfirm); return ok })
	if len(r.ops()) != 0 {
		t.Fatalf("docker ran a file that was not confirmed: %+v", r.ops())
	}
	if !slices.Contains(r.actions(), env.ActionComposeFail) || !slices.Contains(r.actions(), env.ActionComposeFileChanged) {
		t.Fatalf("audit %v", r.actions())
	}
	if _, err := r.svc.Up(ctx, r.wt.ID); !errors.Is(err, worktreeenv.ErrComposeConfirm) {
		t.Fatalf("got %v (state %T)", err, st.State)
	}
}

func TestComposeNeverStuckStarting(t *testing.T) {
	ctx := context.Background()
	t.Run("the port block lookup fails", func(t *testing.T) {
		var fail atomic.Bool
		r := newComposeRigWith(t, func(_ *composeRig, c *worktreeenv.ComposeConfig) {
			c.Block = func(context.Context, core.WorktreeID) (core.PortBlock, bool, error) {
				if fail.Load() {
					return core.PortBlock{}, false, errors.New("database is locked")
				}
				return core.PortBlock{Base: 20040, Size: 20}, true, nil
			}
		})
		r.confirmedDown(t)
		fail.Store(true)
		r.up(t)
		st := r.waitFor(t, func(s env.Service) bool { _, ok := s.(env.ComposeFailed); return ok })
		if strings.Contains(st.Note, "locked") || st.Note == "" {
			t.Errorf("note %q", st.Note)
		}
		if len(r.ops()) != 0 {
			t.Fatal("ran without a port block")
		}
		fail.Store(false)
		if _, err := r.svc.Up(ctx, r.wt.ID); err != nil { // can be retried
			t.Fatal(err)
		}
		r.waitFor(t, func(s env.Service) bool { _, ok := s.(env.Up); return ok })
	})
	t.Run("the audit write of the end fails once", func(t *testing.T) {
		r := newComposeRig(t)
		r.confirmedDown(t)
		var failed atomic.Bool
		r.store.AuditErr = func(e core.AuditEntry) error {
			if e.Action == env.ActionComposeStarted && failed.CompareAndSwap(false, true) {
				return errors.New("disk full")
			}
			return nil
		}
		r.up(t)
		r.waitFor(t, func(s env.Service) bool { _, ok := s.(env.Up); return ok })
		if !slices.Contains(r.actions(), env.ActionComposeStarted) {
			t.Fatalf("the retry did not write the audit entry: %v", r.actions())
		}
	})
	t.Run("the audit write of the end keeps failing", func(t *testing.T) {
		r := newComposeRig(t)
		r.confirmedDown(t)
		r.store.AuditErr = func(e core.AuditEntry) error {
			if e.Action == env.ActionComposeStarted || e.Action == env.ActionComposeFail {
				return errors.New("disk full")
			}
			return nil
		}
		r.up(t)
		r.waitFor(t, func(s env.Service) bool { _, ok := s.(env.Up); return ok })
		// and the project can be stopped again
		if _, err := r.svc.Stop(ctx, r.wt.ID); err != nil {
			t.Fatal(err)
		}
	})
}

func TestComposeRecoveryDoesNotHoldTheLock(t *testing.T) {
	ctx := context.Background()
	release := make(chan struct{})
	r := newComposeRig(t)
	r.runner.Result = func(c worktreeenv.ComposeCall) worktreeenv.ComposeResult {
		if c.Op == env.OpPs {
			<-release
			return worktreeenv.ComposeResult{Output: env.FormatPs(env.PsRunning)}
		}
		return worktreeenv.ComposeResult{}
	}
	r.enable(t)
	first := make(chan error, 1)
	go func() { _, err := r.svc.Status(ctx, r.wt.ID); first <- err }()
	for range 400 { // wait until docker is being asked
		if len(r.runner.Calls()) > 0 {
			break
		}
		time.Sleep(5 * time.Millisecond)
	}
	if len(r.runner.Calls()) == 0 {
		t.Fatal("recovery never asked docker")
	}
	other := make(chan error, 1)
	go func() { _, err := r.svc.Status(ctx, r.wt.ID); other <- err }()
	select {
	case err := <-other:
		if err != nil {
			t.Fatal(err)
		}
	case <-time.After(2 * time.Second):
		close(release)
		t.Fatal("a status read waited for `docker compose ps`: the service lock is held during it")
	}
	close(release)
	if err := <-first; err != nil {
		t.Fatal(err)
	}
	st, _ := r.svc.Status(ctx, r.wt.ID)
	if _, ok := st.State.(env.Up); !ok {
		t.Fatalf("a running project must show as up: %+v", st.State)
	}
}

func TestComposeDisableThenEnableLooksAtDockerAgain(t *testing.T) {
	ctx := context.Background()
	r := newComposeRig(t)
	r.runner.Result = func(worktreeenv.ComposeCall) worktreeenv.ComposeResult {
		return worktreeenv.ComposeResult{Output: env.FormatPs(env.PsRunning)}
	}
	r.enable(t)
	if st, _ := r.svc.Status(ctx, r.wt.ID); !isUp(st.State) {
		t.Fatalf("first: %+v", st.State)
	}
	if err := r.svc.Configure(ctx, "rep_1", false, "compose.yml"); err != nil {
		t.Fatal(err)
	}
	if st, _ := r.svc.Status(ctx, r.wt.ID); st.State != (env.Off{}) {
		t.Fatalf("disabled: %+v", st.State)
	}
	r.enable(t)
	if st, _ := r.svc.Status(ctx, r.wt.ID); !isUp(st.State) {
		t.Fatalf("containers still run, but the state is %+v", st.State)
	}
}

func isUp(s env.Service) bool { _, ok := s.(env.Up); return ok }

func TestComposeConfirmNeedsAHash(t *testing.T) {
	r := newComposeRig(t)
	r.enable(t)
	if _, err := r.svc.Confirm(context.Background(), r.wt.ID, ""); !errors.Is(err, worktreeenv.ErrComposeHashRequired) {
		t.Fatalf("got %v", err)
	}
	st, _ := r.svc.Status(context.Background(), r.wt.ID)
	if _, ok := st.State.(env.NeedsConfirm); !ok {
		t.Fatalf("an empty hash confirmed the file: %+v", st.State)
	}
}

func TestComposeSilentNoopsAreReportedNotSwallowed(t *testing.T) {
	ctx := context.Background()
	r := newComposeRig(t)
	h := r.confirmedDown(t)
	// stop of a project that is not running
	if _, err := r.svc.Stop(ctx, r.wt.ID); !errors.Is(err, worktreeenv.ErrComposeNoop) {
		t.Fatalf("stop in Down: %v", err)
	}
	// confirming what is already confirmed is fine
	if _, err := r.svc.Confirm(ctx, r.wt.ID, h); err != nil {
		t.Fatalf("idempotent confirm: %v", err)
	}
	// a new file cannot be confirmed while the project is up
	r.up(t)
	r.waitFor(t, func(s env.Service) bool { return isUp(s) })
	r.files.Put(r.wt.Path, "compose.yml", "services: {db: {image: other}}")
	st, _ := r.svc.Status(ctx, r.wt.ID)
	if _, err := r.svc.Confirm(ctx, r.wt.ID, st.Hash); !errors.Is(err, worktreeenv.ErrComposeNoop) {
		t.Fatalf("confirm while up: %v", err)
	}
}

// interceptStore runs before once, just before the next settings update.
type interceptStore struct {
	worktreeenv.ComposeStore
	before func()
}

func (s *interceptStore) UpdateComposeSettings(ctx context.Context, repo core.RepoID, at time.Time, a core.AuditEntry,
	mut func(worktreeenv.ComposeSettings) (worktreeenv.ComposeSettings, bool, error)) error {
	if b := s.before; b != nil {
		s.before = nil
		b()
	}
	return s.ComposeStore.UpdateComposeSettings(ctx, repo, at, a, mut)
}

func TestComposeConfirmDoesNotOverwriteNewerSettings(t *testing.T) {
	ctx := context.Background()
	ic := &interceptStore{}
	r := newComposeRigWith(t, func(r *composeRig, c *worktreeenv.ComposeConfig) {
		ic.ComposeStore = r.store
		c.Store = ic
	})
	r.enable(t)
	st, _ := r.svc.Status(ctx, r.wt.ID)
	// the user points the repo at another file while the confirmation is written
	ic.before = func() {
		if err := r.svc.Configure(ctx, "rep_1", true, "other.yml"); err != nil {
			t.Error(err)
		}
	}
	if _, err := r.svc.Confirm(ctx, r.wt.ID, st.Hash); !errors.Is(err, worktreeenv.ErrComposeStale) {
		t.Fatalf("got %v", err)
	}
	got, _ := r.svc.Settings(ctx, "rep_1")
	if got.File != "other.yml" || !got.Enabled || got.ConfirmedHash != "" {
		t.Fatalf("settings %+v: the confirmation of the old file survived", got)
	}
}

func TestComposeChangingTheFileDropsTheConfirmation(t *testing.T) {
	ctx := context.Background()
	r := newComposeRig(t)
	h := r.confirmedDown(t)
	if got, _ := r.svc.Settings(ctx, "rep_1"); got.ConfirmedHash != h {
		t.Fatalf("%+v", got)
	}
	if err := r.svc.Configure(ctx, "rep_1", true, "other.yml"); err != nil {
		t.Fatal(err)
	}
	if got, _ := r.svc.Settings(ctx, "rep_1"); got.ConfirmedHash != "" {
		t.Fatalf("kept the confirmation of another file: %+v", got)
	}
	// the same file again keeps it
	_ = r.svc.Configure(ctx, "rep_1", true, "compose.yml")
	r.confirmedDown2(t)
}

// confirmedDown2 confirms again after a settings change (the entry is NeedsConfirm).
func (r *composeRig) confirmedDown2(t *testing.T) {
	t.Helper()
	st, _ := r.svc.Status(context.Background(), r.wt.ID)
	if _, err := r.svc.Confirm(context.Background(), r.wt.ID, st.Hash); err != nil {
		t.Fatal(err)
	}
	if got, _ := r.svc.Settings(context.Background(), "rep_1"); got.ConfirmedHash != st.Hash {
		t.Fatalf("%+v", got)
	}
}

func TestComposeAutoConfirmRecordsTheSourceWorktree(t *testing.T) {
	ctx := context.Background()
	r := newComposeRig(t)
	b := core.Worktree{ID: "wt_b", RepoID: "rep_1", Path: "/wt/b"}
	r.wts[b.ID] = b
	r.files.Put(b.Path, "compose.yml", "services: {db: {ports: ['${YGG_PORT_0}:5432']}}")
	r.enable(t)
	stB, _ := r.svc.Status(ctx, b.ID)
	if _, ok := stB.State.(env.NeedsConfirm); !ok {
		t.Fatalf("%+v", stB.State)
	}
	stA, _ := r.svc.Status(ctx, r.wt.ID)
	if stA.Hash != stB.Hash {
		t.Fatal("same content must hash the same")
	}
	if _, err := r.svc.Confirm(ctx, r.wt.ID, stA.Hash); err != nil {
		t.Fatal(err)
	}
	stB, _ = r.svc.Status(ctx, b.ID)
	if _, ok := stB.State.(env.Down); !ok {
		t.Fatalf("the same content in another worktree is confirmed too: %+v", stB.State)
	}
	var found bool
	for _, a := range r.store.Audits() {
		if a.Action == env.ActionComposeConfirmed && a.Subject == "worktree:wt_b" {
			found = true
			d := string(a.Detail)
			if !strings.Contains(d, `"autoConfirmed":true`) || !strings.Contains(d, `"sourceWorktree":"`+string(r.wt.ID)+`"`) {
				t.Errorf("audit detail %s", d)
			}
		}
	}
	if !found {
		t.Fatal("no audit entry for the automatic confirmation")
	}
}
