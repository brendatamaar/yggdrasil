package db

import (
	"errors"
	"fmt"

	"modernc.org/sqlite"
)

// SQLite extended result codes (https://sqlite.org/rescode.html) that mean
// something to callers.
const (
	codeConstraintForeignKey = 787  // SQLITE_CONSTRAINT_FOREIGNKEY
	codeConstraintPrimaryKey = 1555 // SQLITE_CONSTRAINT_PRIMARYKEY
	codeConstraintTrigger    = 1811 // SQLITE_CONSTRAINT_TRIGGER: RAISE(ABORT, …) in a trigger
	codeConstraintUnique     = 2067 // SQLITE_CONSTRAINT_UNIQUE
)

// sqliteCode returns the extended result code of a driver error.
func sqliteCode(err error) (int, bool) {
	var se *sqlite.Error
	if errors.As(err, &se) {
		return se.Code(), true
	}
	return 0, false
}

// classify turns the driver errors callers can act on into this package's
// sentinels, so no caller matches driver codes or message text:
//
//   - a UNIQUE or PRIMARY KEY violation is ErrConflict (the key is taken);
//   - a FOREIGN KEY violation is ErrNotFound (the referenced row is missing).
//
// The one-level sub-task trigger is mapped where it can fire (CreateTask):
// elsewhere a trigger abort, such as the append-only audit log, is not a
// sub-task error. An error that already is a sentinel, or that is not a
// constraint violation, is returned as it is.
func classify(err error) error {
	if err == nil || errors.Is(err, ErrConflict) || errors.Is(err, ErrNotFound) || errors.Is(err, ErrSubTaskDepth) {
		return err
	}
	code, ok := sqliteCode(err)
	if !ok {
		return err
	}
	switch code {
	case codeConstraintUnique, codeConstraintPrimaryKey:
		return fmt.Errorf("%w: %v", ErrConflict, err)
	case codeConstraintForeignKey:
		return fmt.Errorf("%w: %v", ErrNotFound, err)
	}
	return err
}
