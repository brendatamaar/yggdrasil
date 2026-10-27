package proposals

import (
	"context"
	"strings"

	coreprop "github.com/brendatamaar/yggdrasil/internal/core/proposals"
)

// execDelete moves the named files into <target>/_to_delete/<relative path>.
// It never deletes: the user empties that folder themselves (invariant 1). A
// path that is gone or locked (Windows) is reported by name; files already
// moved stay moved, and a retry counts them as done.
func (s *Service) execDelete(ctx context.Context, p coreprop.Proposal, a coreprop.DeleteFiles) outcome {
	wd, stop := s.open(ctx, p, a.Where, true)
	if stop != nil {
		return *stop
	}
	defer wd.done()
	if why := checkPaths(wd.dir, a.Paths, false); why != "" {
		return failed(coreprop.FailPrecondition, false, "%s", why)
	}
	var moved []coreprop.RelPath
	var problems []string
	for _, rel := range a.Paths {
		if ctx.Err() != nil {
			return gitFailure(ctx.Err())
		}
		if movedAlready(wd.dir, string(rel)) {
			moved = append(moved, coreprop.RelPath(toDeleteDir+"/"+string(rel)))
			continue
		}
		if _, err := insideRoot(wd.dir, string(rel)); err != nil {
			problems = append(problems, string(rel)+" (outside the task folder)")
			continue
		}
		dest, err := moveAside(wd.dir, string(rel))
		if err != nil {
			problems = append(problems, string(rel)+" ("+moveReason(err)+")")
			continue
		}
		moved = append(moved, coreprop.RelPath(dest))
	}
	if len(problems) > 0 {
		return failed(coreprop.FailExec, true, "Moved %d of %d files into %s. Could not move: %s. Close programs that may be using them, then retry.",
			len(moved), len(a.Paths), toDeleteDir, strings.Join(problems, ", "))
	}
	return done(coreprop.Result{MovedPaths: moved, Note: "Moved the files into " + toDeleteDir + ". Nothing was deleted."})
}

func moveReason(err error) string {
	low := strings.ToLower(err.Error())
	switch {
	case strings.Contains(low, "not exist") || strings.Contains(low, "cannot find"):
		return "not found"
	case strings.Contains(low, "being used") || strings.Contains(low, "access is denied") || strings.Contains(low, "permission"):
		return "in use or locked"
	}
	return "could not be moved"
}
