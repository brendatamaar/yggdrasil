package platform

import (
	"errors"
	"fmt"
	"regexp"
)

// Orphan is a process left by a crashed Yggdrasil run (DV-03, non-Windows).
// ID is the key the caller recorded with RecordProcess (a dev server ID).
type Orphan struct {
	ID  string
	Pid int
}

// The orphan functions (DV-03). On Windows the job object ends every child
// with Yggdrasil, so ListOrphans returns nothing there and the others are
// no-ops. On other systems the pid files live under dataDir/orphans:
//
//	// RecordProcess writes the pid file for id (called after Start).
//	func RecordProcess(dataDir, id string, pid int) error
//	// ForgetProcess removes the pid file for id (called after the process ended).
//	func ForgetProcess(dataDir, id string) error
//	// ListOrphans returns recorded processes that are still alive.
//	func ListOrphans(dataDir string) ([]Orphan, error)
//	// StopOrphan kills the process tree of a recorded, still-alive orphan
//	// after checking that the pid is the one recorded for id.
//	func StopOrphan(dataDir, id string) error
//
// Only the files these functions wrote are ever removed.

// ErrInvalidProcessID is returned for an id that is not safe as a file name.
var ErrInvalidProcessID = errors.New("platform: not a valid process id")

var orphanIDPattern = regexp.MustCompile(`^[A-Za-z0-9][A-Za-z0-9._-]{0,127}$`)

// CheckProcessID refuses ids that could escape the pid file folder or collide
// with other files.
func CheckProcessID(id string) error {
	if !orphanIDPattern.MatchString(id) {
		return fmt.Errorf("%w: %q (use letters, digits, '.', '_' and '-', at most 128 characters)", ErrInvalidProcessID, id)
	}
	return nil
}

// PythonBase finds the base interpreter used to create a new .venv (py
// launcher or PATH), never the one named by VIRTUAL_ENV (docs/AGENTS.md
// section 9). env is the clean environment; only its PATH is read.
//
//	func PythonBase(env []string) (string, error)
