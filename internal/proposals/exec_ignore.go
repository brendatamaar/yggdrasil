package proposals

import (
	"context"
	"errors"
	"strconv"

	coreprop "github.com/brendatamaar/yggdrasil/internal/core/proposals"
	"github.com/brendatamaar/yggdrasil/internal/platform"
)

// execIgnore appends the confirmed entries to the top-level .gitignore of the
// repo's main checkout (WS-09). It runs under the shared main lock and writes
// only through the platform IgnoreWriter, which opens the file without following
// links, checks the reviewed checksum (or absence) through the write, preserves
// the existing bytes and line endings and never renames over, truncates or
// deletes. The "started" audit entry is committed before the write.
func (s *Service) execIgnore(ctx context.Context, p coreprop.Proposal, a coreprop.IgnoreEntries, attempt int) outcome {
	if s.cfg.Ignore == nil {
		return failed(coreprop.FailPrecondition, false, "Editing .gitignore is not set up in this build of Yggdrasil, so nothing was changed.")
	}
	if err := a.Validate(); err != nil {
		return failed(coreprop.FailPrecondition, false, "This ignore proposal is not valid any more, so nothing was changed. Ask for a new one.")
	}
	wd, stop := s.open(ctx, p, a.Where, true)
	if stop != nil {
		return *stop
	}
	defer wd.done()
	ctx, cancel := context.WithTimeout(ctx, s.cfg.ExecTimeout)
	defer cancel()

	// Read-only check first: a changed file is refused before anything is
	// recorded as started. The writer checks again through the write itself.
	snap, err := s.cfg.Ignore.ReadIgnore(ctx, wd.dir)
	if err != nil {
		return ignoreFailure(err)
	}
	if !coreprop.IgnoreMatches(a, coreprop.IgnoreFile{Absent: snap.Absent, Hash: snap.Hash}) {
		s.auditStageBestEffort(ctx, p, attempt, stageIgnoreRefused)
		return ignoreStale()
	}
	if err := s.auditStage(ctx, p, attempt, stageIgnoreStarted); err != nil {
		return failed(coreprop.FailExec, true, "Yggdrasil could not record that it is about to change .gitignore, so it did nothing. Try again.")
	}
	res, _, err := s.cfg.Ignore.AppendIgnore(ctx, wd.dir, a.Entries, platform.IgnoreExpectation{Absent: a.ExpectedAbsent, Hash: a.ExpectedHash})
	if err != nil {
		out := ignoreFailure(err)
		stage := stageIgnoreRefused
		if isContextEnd(err) || (out.kind == coreprop.FailInterrupted) {
			stage = stageIgnoreUnsure
		}
		s.auditStageBestEffort(ctx, p, attempt, stage)
		return out
	}
	switch res {
	case platform.IgnoreCreated:
		s.auditStageBestEffort(ctx, p, attempt, stageIgnoreCreated)
		return done(coreprop.Result{Note: "Created .gitignore with " + entriesText(len(a.Entries)) + "."})
	case platform.IgnoreAppended:
		s.auditStageBestEffort(ctx, p, attempt, stageIgnoreAppended)
		return done(coreprop.Result{Note: "Added " + entriesText(len(a.Entries)) + " to .gitignore. Nothing else was changed."})
	case platform.IgnoreAlreadyPresent:
		s.auditStageBestEffort(ctx, p, attempt, stageIgnoreAlreadyThere)
		return done(coreprop.Result{Note: "Every entry was already in .gitignore, so nothing was changed."})
	}
	// An outcome this code does not know is not reported as success.
	s.auditStageBestEffort(ctx, p, attempt, stageIgnoreUnsure)
	return failed(coreprop.FailInterrupted, true, "Yggdrasil could not tell what happened to .gitignore. Check the file, then retry if needed.")
}

func entriesText(n int) string {
	if n == 1 {
		return "1 entry"
	}
	return strconv.Itoa(n) + " entries"
}

func ignoreStale() outcome {
	return failed(coreprop.FailStale, false, ".gitignore changed since this was proposed, so nothing was written. Reject this and ask for a new proposal.")
}

// ignoreFailure maps an error of the ignore writer to a plain outcome. No
// error text is shown: it can hold a machine path.
func ignoreFailure(err error) outcome {
	switch {
	case errors.Is(err, platform.ErrChanged):
		return ignoreStale()
	case errors.Is(err, platform.ErrLocked):
		return failed(coreprop.FailExec, true, ".gitignore is in use by another program. Close it, then retry.")
	case errors.Is(err, platform.ErrUnsafePath):
		return failed(coreprop.FailPrecondition, false, ".gitignore is a link or not a plain file, so Yggdrasil will not edit it. Nothing was changed.")
	case errors.Is(err, platform.ErrTooLarge):
		return failed(coreprop.FailPrecondition, false, ".gitignore is larger than 1 MiB, so Yggdrasil will not edit it. Nothing was changed.")
	case errors.Is(err, platform.ErrUnsupportedPlatform):
		return failed(coreprop.FailPrecondition, false, "This system cannot edit .gitignore safely, so nothing was changed.")
	case errors.Is(err, platform.ErrInvalidArgument):
		return failed(coreprop.FailPrecondition, false, "This ignore proposal cannot be applied, so nothing was changed. Ask for a new one.")
	case isContextEnd(err):
		return failed(coreprop.FailInterrupted, true, "The change to .gitignore was stopped before it finished. Check the file, then retry if needed.")
	}
	return failed(coreprop.FailInterrupted, true, "Yggdrasil could not finish changing .gitignore. Existing lines were never removed. Check the file, then retry if needed.")
}

// reconcileIgnore judges, read-only, a crashed ignore write: the file as
// reviewed means it never happened; anything else is reported for manual
// inspection and never repaired.
func (s *Service) reconcileIgnore(ctx context.Context, p coreprop.Proposal, a coreprop.IgnoreEntries) (coreprop.ReconcileOutcome, coreprop.Result) {
	if s.cfg.Ignore == nil {
		return coreprop.ReconcileUnknown, coreprop.Result{}
	}
	wd, stop := s.open(ctx, p, a.Where, false)
	if stop != nil {
		return coreprop.ReconcileUnknown, coreprop.Result{}
	}
	snap, err := s.cfg.Ignore.ReadIgnore(ctx, wd.dir)
	if err != nil {
		return coreprop.ReconcileUnknown, coreprop.Result{}
	}
	return coreprop.ReconcileIgnore(a, coreprop.IgnoreFile{Absent: snap.Absent, Hash: snap.Hash}), coreprop.Result{}
}
