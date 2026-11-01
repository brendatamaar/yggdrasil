package platform

import (
	"errors"
	"io/fs"
	"os"
	"path/filepath"
	"runtime"
	"strings"
)

// RemoveReport is what RemoveStaged leaves behind when it could not finish.
type RemoveReport struct {
	// Remaining is the number of entries that could not be deleted.
	Remaining int
	// FirstErr is the first failure, without paths.
	FirstErr error
	// Class is ClassifyLock(FirstErr); LockOther when nothing failed.
	Class LockClass
}

// RemoveStaged deletes the folder tree at staged. It is the only function in
// Yggdrasil that deletes a folder tree (docs/AGENTS.md 6, invariant 1), so it
// is narrow on purpose:
//
//   - staged must be at least one level below <root>/.removing (StagingDir),
//     spelled exactly as it resolves on disk: no "..", no 8.3 names, no
//     symlink or junction in any part of the path, no alternate stream, not a
//     reparse point itself. Anything else returns ErrStagedRefused or a path
//     error and deletes nothing.
//   - It never follows a link. A symlink or junction inside the tree is
//     removed as a link; its target is not touched (os.RemoveAll is not used,
//     it does not treat every junction as a link).
//   - Read-only attributes are cleared before a delete; deep paths use the
//     \\?\ form.
//   - On a partial failure it continues with the other entries and returns the
//     report and a non-nil error; calling it again resumes. A missing staged
//     folder is success.
func RemoveStaged(root, staged string) (RemoveReport, error) {
	rs, err := checkStaged(root, staged)
	if err != nil {
		return RemoveReport{Class: LockOther}, err
	}
	rep := RemoveReport{Class: LockOther}
	if _, _, err := entryInfo(rs); err != nil {
		if errors.Is(err, fs.ErrNotExist) {
			return rep, nil
		}
		rep.record(err)
		return rep, rep.FirstErr
	}
	if link, err := IsReparsePoint(rs); err != nil || link {
		return rep, ErrStagedRefused
	}
	removeTree(rs, &rep)
	if rep.Remaining > 0 {
		return rep, rep.FirstErr
	}
	return rep, nil
}

// checkStaged returns the resolved staged path when it is safe to delete.
func checkStaged(root, staged string) (string, error) {
	for _, seg := range strings.FieldsFunc(staged, func(r rune) bool { return r == '/' || r == '\\' }) {
		if seg == ".." {
			return "", ErrStagedRefused
		}
	}
	cr, err := CleanRootPath(root)
	if err != nil {
		return "", err
	}
	cs, err := CleanRootPath(staged)
	if err != nil {
		return "", err
	}
	if runtime.GOOS == "windows" && (strings.HasPrefix(cr, `\\`) || strings.HasPrefix(cs, `\\`)) {
		return "", ErrStagedRefused
	}
	rr, err := ResolveRoot(cr)
	if err != nil {
		return "", err
	}
	rs, err := ResolveRoot(cs)
	if err != nil {
		return "", err
	}
	// A link or an 8.3 name anywhere in the path makes the resolved spelling
	// differ from the one given.
	if !eqPath(cs, rs) || !stagedInside(rr, rs) {
		return "", ErrStagedRefused
	}
	rel, err := filepath.Rel(filepath.Join(rr, StagingDir), rs)
	if err != nil || (runtime.GOOS == "windows" && HasShortNameSegment(rel)) {
		return "", ErrStagedRefused
	}
	return rs, nil
}

func (r *RemoveReport) record(err error) {
	r.Remaining++
	if r.FirstErr == nil {
		r.FirstErr = stripPath(err)
		r.Class = ClassifyLock(err)
	}
}

// removeTree deletes p bottom-up and reports whether p is gone. Links are
// deleted as links and never entered.
func removeTree(p string, rep *RemoveReport) bool {
	ep := fsPath(p)
	link, dir, err := entryInfo(ep)
	if err != nil {
		if errors.Is(err, fs.ErrNotExist) {
			return true
		}
		rep.record(err)
		return false
	}
	if dir && !link {
		prepStagedDir(ep)
		entries, err := os.ReadDir(ep)
		if err != nil {
			rep.record(err)
			return false
		}
		clean := true
		for _, e := range entries {
			if !removeTree(filepath.Join(p, e.Name()), rep) {
				clean = false
			}
		}
		if !clean {
			return false
		}
	}
	if err := deleteEntry(ep, dir); err != nil && !errors.Is(err, fs.ErrNotExist) {
		rep.record(err)
		return false
	}
	return true
}
