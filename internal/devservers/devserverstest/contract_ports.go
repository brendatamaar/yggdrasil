package devserverstest

import (
	"context"
	"slices"
	"sync"
	"testing"
	"time"

	"github.com/brendatamaar/yggdrasil/internal/apperr"
	"github.com/brendatamaar/yggdrasil/internal/devservers"
)

// This file holds the contract suites of the dev server capabilities that
// have a fake: Repos, Templates, BlockSource and the port lister. Each is run
// against the fake (in this module) and the live implementation (in
// cmd/ygg for the adapters that need the database).

func classOf(err error) apperr.Class {
	c, _ := apperr.ClassOf(err)
	return c
}

// ReposFixture is a devservers.Repos under test. The tracked repo has a main
// checkout and one active worktree in different folders; Untrack removes the
// repo (and with it its worktree).
type ReposFixture struct {
	Repos     devservers.Repos
	Main      devservers.Target // the tracked repo's main checkout
	Worktree  devservers.Target // an active worktree of that repo
	Unknown   devservers.Target // a repo that was never tracked
	UnknownWT devservers.Target // a worktree that does not exist (in the tracked repo)
	WrongRepo devservers.Target // the tracked worktree's ID under another repo
	Untrack   func()
}

// ReposContract: only tracked folders resolve (invariant 6, DV-05).
func ReposContract(t *testing.T, mk func(t *testing.T) ReposFixture) {
	t.Helper()
	ctx := context.Background()
	t.Run("a tracked repo and its worktree resolve to different folders", func(t *testing.T) {
		fx := mk(t)
		a, err1 := fx.Repos.Dir(ctx, fx.Main)
		b, err2 := fx.Repos.Dir(ctx, fx.Worktree)
		if err1 != nil || err2 != nil || a == "" || b == "" || a == b {
			t.Fatalf("main %q %v, worktree %q %v", a, err1, b, err2)
		}
	})
	t.Run("resolution is stable", func(t *testing.T) {
		fx := mk(t)
		a, _ := fx.Repos.Dir(ctx, fx.Worktree)
		b, _ := fx.Repos.Dir(ctx, fx.Worktree)
		if a != b {
			t.Fatalf("%q then %q", a, b)
		}
	})
	for name, pick := range map[string]func(ReposFixture) devservers.Target{
		"an untracked repo":               func(f ReposFixture) devservers.Target { return f.Unknown },
		"an unknown worktree":             func(f ReposFixture) devservers.Target { return f.UnknownWT },
		"a worktree under the wrong repo": func(f ReposFixture) devservers.Target { return f.WrongRepo },
		"an empty target":                 func(ReposFixture) devservers.Target { return devservers.Target{} },
	} {
		t.Run(name+" is refused as not found", func(t *testing.T) {
			fx := mk(t)
			dir, err := fx.Repos.Dir(ctx, pick(fx))
			if err == nil || dir != "" {
				t.Fatalf("resolved to %q (err %v)", dir, err)
			}
			if c := classOf(err); c != apperr.NotFound {
				t.Fatalf("class = %v, want NotFound (err %v)", c, err)
			}
		})
	}
	t.Run("a removed repo is refused, with its worktree", func(t *testing.T) {
		fx := mk(t)
		fx.Untrack()
		for _, tg := range []devservers.Target{fx.Main, fx.Worktree} {
			if dir, err := fx.Repos.Dir(ctx, tg); err == nil || classOf(err) != apperr.NotFound {
				t.Fatalf("%+v still resolves: %q %v", tg, dir, err)
			}
		}
	})
}

// TemplatesFixture is a devservers.Templates under test.
type TemplatesFixture struct {
	Templates devservers.Templates
	Target    devservers.Target
	Command   string   // a command the target offers
	WantArgv  []string // its argv
	Unknown   string   // a command it does not offer
	// Untracked, when set, is a target the implementation must refuse.
	Untracked *devservers.Target
}

// TemplatesContract: offered commands come back as the argv the toolchain
// gave, callers cannot change the next answer, unknown commands are Invalid.
func TemplatesContract(t *testing.T, mk func(t *testing.T) TemplatesFixture) {
	t.Helper()
	ctx := context.Background()
	t.Run("an offered command returns its argv", func(t *testing.T) {
		fx := mk(t)
		argv, _, err := fx.Templates.DevServer(ctx, fx.Target, fx.Command)
		if err != nil || !slices.Equal(argv, fx.WantArgv) {
			t.Fatalf("argv %v, err %v; want %v", argv, err, fx.WantArgv)
		}
	})
	t.Run("an unknown command is invalid", func(t *testing.T) {
		fx := mk(t)
		argv, _, err := fx.Templates.DevServer(ctx, fx.Target, fx.Unknown)
		if err == nil || argv != nil || classOf(err) != apperr.Invalid {
			t.Fatalf("argv %v, err %v (class %v)", argv, err, classOf(err))
		}
		if msg, ok := apperr.MessageOf(err); !ok || msg == "" {
			t.Fatalf("no plain message: %v", err)
		}
	})
	t.Run("the caller cannot change the next answer", func(t *testing.T) {
		fx := mk(t)
		argv, env, err := fx.Templates.DevServer(ctx, fx.Target, fx.Command)
		if err != nil {
			t.Fatal(err)
		}
		for i := range argv {
			argv[i] = "tampered"
		}
		if env != nil {
			env["TAMPERED"] = "1"
		}
		again, env2, err := fx.Templates.DevServer(ctx, fx.Target, fx.Command)
		if err != nil || !slices.Equal(again, fx.WantArgv) {
			t.Fatalf("second answer %v, %v", again, err)
		}
		if _, bad := env2["TAMPERED"]; bad {
			t.Fatal("the environment map is shared between answers")
		}
	})
	t.Run("an untracked target is refused", func(t *testing.T) {
		fx := mk(t)
		if fx.Untracked == nil {
			t.Skip("this implementation does not look at the target")
		}
		if argv, _, err := fx.Templates.DevServer(ctx, *fx.Untracked, fx.Command); err == nil {
			t.Fatalf("offered %v for an untracked target", argv)
		}
	})
}

// BlocksFixture is a devservers.BlockSource under test with two worktrees.
type BlocksFixture struct {
	Blocks devservers.BlockSource
	Main   devservers.Target
	WT1    devservers.Target
	WT2    devservers.Target
}

// BlocksContract: a main checkout has no block, a worktree gets a valid one
// that does not change, and two worktrees never overlap (WE-02).
func BlocksContract(t *testing.T, mk func(t *testing.T) BlocksFixture) {
	t.Helper()
	ctx := context.Background()
	t.Run("a main checkout has an empty block", func(t *testing.T) {
		fx := mk(t)
		b, err := fx.Blocks.BlockFor(ctx, fx.Main)
		if err != nil || !b.Empty() {
			t.Fatalf("block %+v, err %v", b, err)
		}
	})
	t.Run("a worktree gets a valid block and keeps it", func(t *testing.T) {
		fx := mk(t)
		a, err := fx.Blocks.BlockFor(ctx, fx.WT1)
		if err != nil || a.Empty() || a.Validate() != nil {
			t.Fatalf("block %+v, err %v", a, err)
		}
		b, err := fx.Blocks.BlockFor(ctx, fx.WT1)
		if err != nil || a != b {
			t.Fatalf("second call %+v, err %v; want %+v", b, err, a)
		}
	})
	t.Run("two worktrees get blocks that do not overlap", func(t *testing.T) {
		fx := mk(t)
		a, err1 := fx.Blocks.BlockFor(ctx, fx.WT1)
		b, err2 := fx.Blocks.BlockFor(ctx, fx.WT2)
		if err1 != nil || err2 != nil {
			t.Fatalf("%v %v", err1, err2)
		}
		if a.Base < b.Base+b.Size && b.Base < a.Base+a.Size {
			t.Fatalf("blocks overlap: %+v and %+v", a, b)
		}
	})
}

// PortsFake is a fake port lister: Listen "binds" a port for a pid.
type PortsFake struct {
	mu    sync.Mutex
	ports map[int][]int
}

// Lister is the function the service takes (platform.ListeningPorts).
func (p *PortsFake) Lister(pid int) ([]int, error) {
	p.mu.Lock()
	defer p.mu.Unlock()
	return slices.Clone(p.ports[pid]), nil
}

// Bind makes pid listen on port.
func (p *PortsFake) Bind(pid, port int) {
	p.mu.Lock()
	defer p.mu.Unlock()
	if p.ports == nil {
		p.ports = map[int][]int{}
	}
	p.ports[pid] = append(p.ports[pid], port)
}

// Unbind makes pid stop listening on port.
func (p *PortsFake) Unbind(pid, port int) {
	p.mu.Lock()
	defer p.mu.Unlock()
	p.ports[pid] = slices.DeleteFunc(p.ports[pid], func(n int) bool { return n == port })
}

// PortsHarness is a port lister under test. Listen makes some process listen
// on a fresh port and returns its pid, the port and a function that ends it.
type PortsHarness struct {
	Ports  func(pid int) ([]int, error)
	Listen func(t *testing.T) (pid, port int, stop func())
}

// PortsContract: a listener is reported once it binds, only for its own pid,
// and is gone after it closes (DV-02).
func PortsContract(t *testing.T, mk func(t *testing.T) PortsHarness) {
	t.Helper()
	eventually := func(t *testing.T, what string, ok func() bool) {
		t.Helper()
		for end := time.Now().Add(5 * time.Second); time.Now().Before(end); time.Sleep(10 * time.Millisecond) {
			if ok() {
				return
			}
		}
		t.Fatalf("timed out waiting for %s", what)
	}
	t.Run("a listener is reported after it binds", func(t *testing.T) {
		h := mk(t)
		pid, port, stop := h.Listen(t)
		defer stop()
		eventually(t, "the port to be listed", func() bool {
			got, err := h.Ports(pid)
			return err == nil && slices.Contains(got, port)
		})
	})
	t.Run("a process that listens on nothing has no ports", func(t *testing.T) {
		h := mk(t)
		_, port, stop := h.Listen(t)
		defer stop()
		got, err := h.Ports(1 << 22) // no such process
		if slices.Contains(got, port) {
			t.Fatalf("another process's port %d reported for pid 4194304 (err %v)", port, err)
		}
	})
	t.Run("a closed listener disappears", func(t *testing.T) {
		h := mk(t)
		pid, port, stop := h.Listen(t)
		eventually(t, "the port to be listed", func() bool {
			got, _ := h.Ports(pid)
			return slices.Contains(got, port)
		})
		stop()
		eventually(t, "the port to disappear", func() bool {
			got, _ := h.Ports(pid)
			return !slices.Contains(got, port)
		})
	})
}
