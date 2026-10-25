package tasks

// Step is (task, input) -> (task, commands): one task's turns, attempts,
// stop, finish, usage and caps (the Step table of docs/design/tasks.md). An
// input it does not handle in the current state, or one with a stale Turn or
// Attempt, changes nothing.
//
// STUB (task K1 implements it): returns the task unchanged and no commands.
func Step(t Task, in Input) (Task, []Command) {
	_ = in
	return t, nil
}
