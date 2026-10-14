// Package jobobject is Phase 0 spike B2 (PL-02, PL-05): Windows job objects
// that leave no orphan processes, nested per-task jobs stopped by a
// context.Context, and starting a process outside the job so it outlives
// Yggdrasil.
//
// Throwaway code. The findings drive the Phase 1 internal/platform design.
package jobobject

import "os/exec"

// Options sets a job's limits.
type Options struct {
	// KillOnClose kills every process in the job when the last job handle
	// closes (JOB_OBJECT_LIMIT_KILL_ON_JOB_CLOSE), including when the owner
	// process dies.
	KillOnClose bool
	// BreakawayOK lets processes in the job start children outside it with
	// CREATE_BREAKAWAY_FROM_JOB (JOB_OBJECT_LIMIT_BREAKAWAY_OK).
	BreakawayOK bool
}

// OutsideMethod says how StartOutside got a process out of the job.
type OutsideMethod string

const (
	// MethodBreakaway: CREATE_BREAKAWAY_FROM_JOB.
	MethodBreakaway OutsideMethod = "breakaway"
	// MethodBroker: started with a broker process (outside our job) as its
	// parent via PROC_THREAD_ATTRIBUTE_PARENT_PROCESS.
	MethodBroker OutsideMethod = "broker"
)

// OutsideResult describes a StartOutside call.
type OutsideResult struct {
	Cmd    *exec.Cmd
	Method OutsideMethod
	// BreakawayErr is why breakaway wasn't good enough, when the broker
	// fallback was used.
	BreakawayErr error
	// StillInJob: the process is still in some job, e.g. the broker's own.
	StillInJob bool
}
