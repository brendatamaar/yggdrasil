package platform

import (
	"errors"
	"fmt"
	"time"
)

// maxTermCells is the largest accepted terminal side. ConPTY takes int16
// coordinates; a browser never needs more than this.
const maxTermCells = 1000

// closeTimeout bounds how long PTY.Close waits for the process to exit.
const closeTimeout = 5 * time.Second

func (s Size) validate() error {
	if s.Cols < 1 || s.Cols > maxTermCells || s.Rows < 1 || s.Rows > maxTermCells {
		return fmt.Errorf("platform: terminal size %dx%d is out of range (1..%d)", s.Cols, s.Rows, maxTermCells)
	}
	return nil
}

func checkPTYSpec(spec PTYSpec) error {
	if spec.Env == nil {
		return ErrNoEnv
	}
	if len(spec.Argv) == 0 {
		return errors.New("platform: Argv is empty")
	}
	return spec.Size.validate()
}
