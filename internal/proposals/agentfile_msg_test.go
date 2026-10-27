package proposals

import (
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/brendatamaar/yggdrasil/internal/core"
	coreprop "github.com/brendatamaar/yggdrasil/internal/core/proposals"
)

// A file error never puts the machine path into the stored message.
func TestAgentFileFailureMessageHasNoMachinePath(t *testing.T) {
	w := newWorld(t)
	if err := os.MkdirAll(filepath.Join(w.wtDir, "CLAUDE.md"), 0o750); err != nil {
		t.Fatal(err)
	}
	v := w.create(coreprop.AgentFile{Where: coreprop.InWorktree{ID: wtID}, Path: "CLAUDE.md", Content: "x\n"})
	w.confirm(v)
	got := w.waitFor(v.ID, core.ProposalFailed)
	if got.Message == "" || strings.Contains(got.Message, w.wtDir) || strings.Contains(got.Message, filepath.Base(filepath.Dir(w.wtDir))) {
		t.Fatalf("message = %q", got.Message)
	}
}
