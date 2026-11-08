package platform

import "errors"

// ErrNotImplemented is returned by foundation stubs until the platform task
// (T2, docs/phases/phase-5.md) fills them in.
var ErrNotImplemented = errors.New("platform: not implemented yet")

// Orphan is a process left by a crashed Yggdrasil run (DV-03, non-Windows).
// ID is the key the caller recorded with RecordProcess (a dev server ID).
type Orphan struct {
	ID  string
	Pid int
}

// The orphan functions (DV-03). On Windows the job object ends every child
// with Yggdrasil, so ListOrphans returns nothing there and the others are
// no-ops. On other systems the pid files live under dataDir:
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

// PythonBase finds the base interpreter used to create a new .venv (py
// launcher or PATH), never the one named by VIRTUAL_ENV (docs/AGENTS.md
// section 9). env is the clean environment.
//
//	func PythonBase(env []string) (string, error)

// RecordProcess is a foundation stub; task T2 implements it.
func RecordProcess(dataDir, id string, pid int) error { return ErrNotImplemented }

// ForgetProcess is a foundation stub; task T2 implements it.
func ForgetProcess(dataDir, id string) error { return ErrNotImplemented }

// ListOrphans is a foundation stub; task T2 implements it.
func ListOrphans(dataDir string) ([]Orphan, error) { return nil, ErrNotImplemented }

// StopOrphan is a foundation stub; task T2 implements it.
func StopOrphan(dataDir, id string) error { return ErrNotImplemented }

// PythonBase is a foundation stub; task T2 implements it.
func PythonBase(env []string) (string, error) { return "", ErrNotImplemented }
