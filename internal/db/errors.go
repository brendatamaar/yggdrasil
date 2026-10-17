package db

import (
	"errors"

	"modernc.org/sqlite"
)

// SQLite extended result codes (https://sqlite.org/rescode.html) that mean
// something to callers.
const (
	codeConstraintTrigger = 1811 // SQLITE_CONSTRAINT_TRIGGER: RAISE(ABORT, …) in a trigger
)

// sqliteCode returns the extended result code of a driver error.
func sqliteCode(err error) (int, bool) {
	var se *sqlite.Error
	if errors.As(err, &se) {
		return se.Code(), true
	}
	return 0, false
}
