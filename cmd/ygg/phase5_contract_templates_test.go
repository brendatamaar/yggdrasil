package main

import (
	"os"
	"path/filepath"
	"testing"

	"github.com/brendatamaar/yggdrasil/internal/devservers"
	"github.com/brendatamaar/yggdrasil/internal/devservers/devserverstest"
)

// The live Templates (the Catalog over the tracked-folder resolver, exactly as
// newDevServers builds it) passes the same suite as the fake, and refuses a
// target that is not tracked.
func TestPhase5ContractTemplatesCatalog(t *testing.T) {
	devserverstest.TemplatesContract(t, func(t *testing.T) devserverstest.TemplatesFixture {
		w := newP5World(t)
		manifest := `{"name":"shop-web","scripts":{"dev":"vite"}}`
		if err := os.WriteFile(filepath.Join(w.repoA.Path, "package.json"), []byte(manifest), 0o600); err != nil {
			t.Fatal(err)
		}
		cat := devservers.NewCatalog(devRepos{w.dirs}, devservers.RealClock{})
		tg := devservers.Target{Repo: w.repoA.ID}
		cmds, err := cat.Commands(t.Context(), tg)
		if err != nil || len(cmds) == 0 {
			t.Fatalf("the fixture repo offers no dev command: %v %v", cmds, err)
		}
		untracked := devservers.Target{Repo: "rep_zz"}
		return devserverstest.TemplatesFixture{
			Templates: cat, Target: tg, Command: cmds[0].Key, WantArgv: cmds[0].Argv,
			Unknown: "no-such-command", Untracked: &untracked,
		}
	})
}

// A worktree gets its own scan: the command list of a worktree folder comes
// from that folder, not from the main checkout.
func TestPhase5ContractTemplatesFollowTheTargetFolder(t *testing.T) {
	w := newP5World(t)
	if err := os.WriteFile(filepath.Join(w.wtADir, "package.json"), []byte(`{"name":"x","scripts":{"dev":"vite"}}`), 0o600); err != nil {
		t.Fatal(err)
	}
	cat := devservers.NewCatalog(devRepos{w.dirs}, devservers.RealClock{})
	if cmds, err := cat.Commands(t.Context(), devservers.Target{Repo: w.repoA.ID}); err != nil || len(cmds) != 0 {
		t.Fatalf("the main checkout has no manifest but offers %v (%v)", cmds, err)
	}
	if cmds, err := cat.Commands(t.Context(), devservers.Target{Repo: w.repoA.ID, Worktree: w.wtA.ID}); err != nil || len(cmds) == 0 {
		t.Fatalf("the worktree has a manifest but offers %v (%v)", cmds, err)
	}
}
