package claudecode

import (
	"errors"
	"os"
	"path/filepath"
	"strings"

	"github.com/brendatamaar/yggdrasil/internal/agents"
	"github.com/brendatamaar/yggdrasil/internal/core"
	"github.com/brendatamaar/yggdrasil/internal/core/guard"
	"github.com/brendatamaar/yggdrasil/internal/platform"
)

// emptyMCPConfig is what --mcp-config gets when the run has no Yggdrasil MCP
// server: with --strict-mcp-config no other server loads (docs/spikes.md B5).
const emptyMCPConfig = "{\"mcpServers\":{}}\n"

// prepareMCPFile writes the MCP config the CLI will read into the run folder:
// a copy of src (it holds the bearer token), or an empty config when src is "".
// The folder is outside every policy root and is removed with the run.
func prepareMCPFile(work, src string) (string, error) {
	data := []byte(emptyMCPConfig)
	if src != "" {
		b, err := os.ReadFile(src) //nolint:gosec // the path was written by WriteMCPConfig and checked in Start
		if err != nil {
			return "", errors.New("the MCP config file can't be read")
		}
		data = b
	}
	path := filepath.Join(work, "mcp.json")
	if err := os.WriteFile(path, data, 0o600); err != nil { //nolint:gosec // path is a fixed name in our own run folder
		return "", errors.New("the MCP config file can't be written")
	}
	return path, nil
}

// checkMCPFileOutsidePolicy refuses an MCP config file inside a folder the
// agent may read or write: the file holds a bearer token (invariant 4).
func checkMCPFileOutsidePolicy(p guard.Policy, path string) error {
	return checkOutsidePolicy(p, path, "the MCP config file")
}

// within reports whether p is root or below it, comparing lexically (both are
// canonical already). fold ignores case.
func within(root, p string, fold bool) bool {
	root, p = filepath.Clean(root), filepath.Clean(p)
	if fold {
		root, p = strings.ToLower(root), strings.ToLower(p)
	}
	if p == root {
		return true
	}
	prefix := root
	if !strings.HasSuffix(prefix, string(filepath.Separator)) {
		prefix += string(filepath.Separator)
	}
	return strings.HasPrefix(p, prefix)
}

func withinAny(roots []string, p string, fold bool) bool {
	for _, r := range roots {
		if within(r, p, fold) {
			return true
		}
	}
	return false
}

// checkPolicyNotWider confirms the policy reaches no further than the folders
// Start was given (dir and, in project mode, ExtraDirs): a wider policy would
// let the hook allow what the task was never given (invariants 5 and 6). A
// narrower one is fine.
func checkPolicyNotWider(o agents.StartOptions, dir string) error {
	fold := platform.FoldCase() || o.Policy.FoldCase
	var folders []string
	for _, d := range append([]string{dir}, o.ExtraDirs...) {
		canon, err := platform.ResolveRoot(d)
		if err != nil {
			return err
		}
		folders = append(folders, canon)
	}
	writable := folders[:1] // outside project mode only the task's own folder
	if o.Mode == core.ModeProject {
		writable = folders
	}
	for _, r := range o.Policy.ReadRoots {
		if !withinAny(folders, r, fold) {
			return errors.New("a readable folder of the policy is outside the task's folders")
		}
	}
	for _, r := range o.Policy.WriteRoots {
		if !withinAny(writable, r, fold) {
			return errors.New("a writable folder of the policy is outside the task's folders")
		}
	}
	return nil
}

// deniesFromLog lists the refusals in the hook log. Tool names are cleaned and
// reasons are the fixed texts of the guard; no tool input is ever in the log.
func deniesFromLog(path string) []agents.Deny { return summarizeHookLog(path).denies }

func capReason(s string) string {
	var b strings.Builder
	for _, r := range s {
		if r < 0x20 || r == 0x7f {
			continue
		}
		b.WriteRune(r)
		if b.Len() >= 200 {
			break
		}
	}
	return b.String()
}
