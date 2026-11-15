package tasks

import "github.com/brendatamaar/yggdrasil/internal/core"

// PreviousMode is the writing mode that guard.Admit compares a new turn with.
// The first stored turn fixes it, because an imported draft is persisted as an
// Ask task before the user picks that first mode. When turns are counted but
// no turn row can be read, the task's own recorded mode is used instead. It is
// never any other mode, so the fallback cannot widen what a turn may do: Admit
// still allows a switch only between Ask and this mode.
func PreviousMode(recorded core.TaskMode, turns []TurnRow) core.TaskMode {
	if len(turns) > 0 {
		return turns[0].Mode
	}
	return recorded
}
