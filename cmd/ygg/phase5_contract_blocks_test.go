package main

import (
	"log/slog"
	"testing"

	"github.com/brendatamaar/yggdrasil/internal/devservers"
	"github.com/brendatamaar/yggdrasil/internal/devservers/devserverstest"
)

// The live BlockSource (the worktree environment service behind devBlocks, over
// the database) passes the same suite as the fake: no block for a main
// checkout, a stable valid block per worktree, no overlap between worktrees.
func TestPhase5ContractBlocks(t *testing.T) {
	devserverstest.BlocksContract(t, func(t *testing.T) devserverstest.BlocksFixture {
		w := newP5World(t)
		svc, err := newWorktreeEnv(w.store, w.redactor, &lateEnvHub{}, slog.Default())
		if err != nil {
			t.Fatal(err)
		}
		return devserverstest.BlocksFixture{
			Blocks: devBlocks{svc},
			Main:   devservers.Target{Repo: w.repoA.ID},
			WT1:    devservers.Target{Repo: w.repoA.ID, Worktree: w.wtA.ID},
			WT2:    devservers.Target{Repo: w.repoB.ID, Worktree: w.wtB.ID},
		}
	})
}

// A worktree that does not exist gets no block (nothing is allocated for an ID
// that is not tracked).
func TestPhase5ContractBlocksUnknownWorktree(t *testing.T) {
	w := newP5World(t)
	svc, err := newWorktreeEnv(w.store, w.redactor, &lateEnvHub{}, slog.Default())
	if err != nil {
		t.Fatal(err)
	}
	b, err := devBlocks{svc}.BlockFor(t.Context(), devservers.Target{Repo: w.repoA.ID, Worktree: "wt_zz"})
	if err == nil && !b.Empty() {
		t.Fatalf("a block %+v was allocated for an unknown worktree", b)
	}
}
