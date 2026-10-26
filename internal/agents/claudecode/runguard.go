package claudecode

import (
	"crypto/sha256"
	"errors"
	"log/slog"
	"os"
	"path/filepath"
	"time"

	"github.com/brendatamaar/yggdrasil/internal/agents"
	"github.com/brendatamaar/yggdrasil/internal/core/guard"
	"github.com/brendatamaar/yggdrasil/internal/platform"
)

const (
	// maxDenies caps the refusals a run reports; DenyCount is the true number.
	maxDenies = 100
	// tamperMessage is shown when the agent changed Yggdrasil's own files.
	tamperMessage = "The agent changed Yggdrasil's own safety files, so the task was stopped."
)

// checkOutsidePolicy refuses a path inside a folder the agent may read or
// write. what names the file in the (path-free) error.
func checkOutsidePolicy(p guard.Policy, path, what string) error {
	canon, err := platform.ResolveRoot(path)
	if err != nil {
		canon = filepath.Clean(path)
	}
	fold := platform.FoldCase() || p.FoldCase
	for _, roots := range [][]string{p.ReadRoots, p.WriteRoots} {
		if withinAny(roots, canon, fold) || withinAny(roots, path, fold) {
			return errors.New(what + " is inside a folder of the policy")
		}
	}
	return nil
}

// hookSummary is what the adapter keeps of the hook log before it is deleted.
type hookSummary struct {
	denies    []agents.Deny
	denyCount int
	hooked    []string
}

// summarizeHookLog reads the log once. A log the hook could not finish
// (unreadable, over the cap) yields what was read; IDs beyond it count as
// unhooked, which fails closed.
func summarizeHookLog(path string) hookSummary {
	recs, _ := readHookLog(path)
	var s hookSummary
	for _, r := range recs {
		if r.Decision != decisionDeny && r.Decision != decisionAllow {
			continue
		}
		if r.ToolUseID != "" && len(s.hooked) < maxToolUses {
			s.hooked = append(s.hooked, r.ToolUseID)
		}
		if r.Decision != decisionDeny {
			continue
		}
		s.denyCount++
		if len(s.denies) < maxDenies {
			at, _ := time.Parse(time.RFC3339Nano, r.Time)
			s.denies = append(s.denies, agents.Deny{Tool: safeName(r.Tool), Reason: capReason(r.Reason), At: at})
		}
	}
	return s
}

// unhooked lists the seen IDs that have no hook record.
func unhooked(seen, hooked []string) []string {
	have := make(map[string]struct{}, len(hooked))
	for _, id := range hooked {
		have[id] = struct{}{}
	}
	var out []string
	for _, id := range seen {
		if _, ok := have[id]; !ok {
			out = append(out, id)
		}
	}
	return out
}

// seenListFull is the sentinel reported when the parser's list of seen tool
// uses overflowed: some calls can no longer be checked against the hook log,
// so the run counts as unhooked.
const seenListFull = "<seen-list-full>"

// unhookedToolUses lists the seen IDs without a hook record.
func (p *parser) unhookedToolUses(hooked []string) []string {
	out := unhooked(p.toolUses, hooked)
	if p.seenFull {
		out = append(out, seenListFull)
	}
	return out
}

// fileHashes remembers the SHA-256 of Yggdrasil's own run files.
type fileHashes map[string][sha256.Size]byte

// hashFiles hashes the files; an unreadable file is an error.
func hashFiles(paths ...string) (fileHashes, error) {
	h := make(fileHashes, len(paths))
	for _, p := range paths {
		data, err := os.ReadFile(p) //nolint:gosec // our own run files
		if err != nil {
			return nil, errors.New("a run file can't be read")
		}
		h[p] = sha256.Sum256(data)
	}
	return h, nil
}

// changed reports whether any file differs from its remembered hash or is gone.
func (h fileHashes) changed() bool {
	for p, want := range h {
		data, err := os.ReadFile(p) //nolint:gosec // our own run files
		if err != nil || sha256.Sum256(data) != want {
			return true
		}
	}
	return false
}

// makeReadOnly marks the files read-only (0400; the read-only attribute on
// Windows). A failure is not fatal: the hash checks still apply. It is logged
// without the path.
func makeReadOnly(paths ...string) {
	for _, p := range paths {
		if err := os.Chmod(p, 0o400); err != nil {
			slog.Warn("claudecode: a run file could not be made read-only", "file", filepath.Base(p))
		}
	}
}

// removeRunFolder deletes the run folder. A failure (a locked file, antivirus)
// gets one retry after pause; a second failure is logged without the path.
func removeRunFolder(work string, removeAll func(string) error, pause time.Duration) {
	if err := removeAll(work); err == nil {
		return
	}
	time.Sleep(pause)
	if err := removeAll(work); err != nil {
		slog.Warn("claudecode: the run folder could not be removed; it may hold the policy and MCP files")
	}
}
