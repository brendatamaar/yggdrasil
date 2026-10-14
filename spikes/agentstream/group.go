package agentstream

// group holds one run's process tree: a job object on Windows
// (proc_windows.go), a process group elsewhere (proc_other.go).
type group interface {
	// terminate kills every process in the group. Safe to call more than once
	// and after everything has exited.
	terminate() error
	// active is the number of live processes, or -1 if unknown.
	active() int
	// close releases the group; anything still in it dies.
	close() error
}
