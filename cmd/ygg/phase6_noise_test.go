package main

import (
	"os"
	"path/filepath"
	"strconv"
	"testing"

	"github.com/brendatamaar/yggdrasil/internal/api"
	"github.com/brendatamaar/yggdrasil/internal/core"
)

// Noise: the suggestion of a repo with many untracked generated files becomes a
// pending ignore proposal; nothing is written until a confirmation (WS-09).
func TestPhase6NoiseSuggestionMakesAPendingProposalOnly(t *testing.T) {
	r := newP6Rig(t)
	p := r.newRepo(t)
	nm := filepath.Join(p.dir, "node_modules", "left-pad")
	if err := os.MkdirAll(nm, 0o700); err != nil {
		t.Fatal(err)
	}
	for i := 0; i < 120; i++ {
		if err := os.WriteFile(filepath.Join(nm, "f"+strconv.Itoa(i)+".js"), []byte("x\n"), 0o600); err != nil {
			t.Fatal(err)
		}
	}
	r.refresh(t, p)
	var n api.NoiseResponse
	if code := r.get(t, "/api/repos/"+string(p.id)+"/noise", &n); code != 200 || !n.Available || len(n.Candidates) == 0 {
		t.Fatalf("noise = %d %+v", code, n)
	}
	before := r.tree(t, p)
	var prop api.ProposalDTO
	body := api.CreateIgnoreProposalRequest{RequestID: "req_ignore_1", Entries: []string{n.Candidates[0].Pattern}}
	if code, data := r.post(t, "/api/repos/"+string(p.id)+"/noise/proposals", body, &prop); code != 200 || prop.Status != core.ProposalPending {
		t.Fatalf("proposal = %d %s", code, data)
	}
	if _, err := os.Stat(filepath.Join(p.dir, ".gitignore")); err == nil {
		t.Fatal("the .gitignore was written before a confirmation")
	}
	if got := r.tree(t, p); got != before {
		t.Fatal("the tree changed")
	}
	// A pattern that is not a current candidate is refused.
	bad := api.CreateIgnoreProposalRequest{RequestID: "req_ignore_2", Entries: []string{"/src/"}}
	if code, _ := r.post(t, "/api/repos/"+string(p.id)+"/noise/proposals", bad, nil); code != 400 {
		t.Fatalf("a non-candidate pattern = %d, want 400", code)
	}
}
