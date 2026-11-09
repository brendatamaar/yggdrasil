package worktrees

// SetLongPath replaces the long-path lookup and returns a restore function.
func SetLongPath(f func(string) (string, error)) (restore func()) {
	old := longPath
	longPath = f
	return func() { longPath = old }
}
