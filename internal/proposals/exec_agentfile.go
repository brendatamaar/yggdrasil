package proposals

import (
	"context"
	"errors"
	"io/fs"
	"os"

	coreprop "github.com/brendatamaar/yggdrasil/internal/core/proposals"
)

// currentFileHash is the hash of the file now, or "" when it does not exist.
func currentFileHash(path string) (string, error) {
	b, err := os.ReadFile(path) //nolint:gosec // path was resolved inside the task folder by insideRoot
	if errors.Is(err, fs.ErrNotExist) {
		return "", nil
	}
	if err != nil {
		return "", err
	}
	return FileHash(b), nil
}

// execAgentFile writes one agent-config file. It checks the file still has the
// hash the user reviewed (ExpectHash) and replaces it with a temp file plus a
// rename. In the main checkout it runs under the shared lock.
func (s *Service) execAgentFile(ctx context.Context, p coreprop.Proposal, a coreprop.AgentFile) outcome {
	wd, stop := s.open(ctx, p, a.Where, true)
	if stop != nil {
		return *stop
	}
	defer wd.done()
	if err := a.Validate(); err != nil {
		return failed(coreprop.FailPrecondition, false, "This agent-file proposal is not valid any more.")
	}
	if why := checkPaths(wd.dir, []coreprop.RelPath{a.Path}, true); why != "" {
		return failed(coreprop.FailPrecondition, false, "%s", why)
	}
	full, err := insideRoot(wd.dir, string(a.Path))
	if err != nil {
		return failed(coreprop.FailPrecondition, false, "The file path is not inside the folder of this task.")
	}
	cur, err := currentFileHash(full)
	if err != nil {
		return failed(coreprop.FailExec, true, "Yggdrasil could not read %s to check it is unchanged: %v", a.Path, err)
	}
	if cur != a.ExpectHash {
		return failed(coreprop.FailStale, false, "%s changed since this was proposed. Reject it and ask the agent for a new one.", a.Path)
	}
	if err := writeAtomic(full, []byte(a.Content)); err != nil {
		return failed(coreprop.FailExec, true, "Yggdrasil could not write %s: %v", a.Path, err)
	}
	return done(coreprop.Result{Note: "Wrote " + string(a.Path) + "."})
}
