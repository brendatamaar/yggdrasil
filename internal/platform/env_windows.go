//go:build windows

package platform

import "strings"

const noCurDirVar = "NoDefaultCurrentDirectoryInExePath"

// withNoCurDir returns env for a child process: a copy with
// NoDefaultCurrentDirectoryInExePath=1, so cmd.exe does not look in the
// working directory before PATH. Without it, a batch file (the npm shim
// claude.cmd calling bare "node") would run a node.cmd that an agent wrote
// into its worktree (invariant 5). The caller's slice is never changed; an
// entry the caller already set is replaced, so there is exactly one.
func withNoCurDir(env []string) []string {
	out := make([]string, 0, len(env)+1)
	for _, kv := range env {
		if k, _, _ := strings.Cut(kv, "="); strings.EqualFold(k, noCurDirVar) {
			continue
		}
		out = append(out, kv)
	}
	return append(out, noCurDirVar+"=1")
}
