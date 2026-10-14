//go:build !windows

package jobobject

import (
	"context"
	"errors"
	"os/exec"
)

// Job objects are Windows-only. On Unix, Phase 1 would use process groups
// (Setpgid + kill(-pgid)); this stub only keeps the package compiling.

// Job is a placeholder on non-Windows systems.
type Job struct{}

// New always fails on non-Windows systems.
func New(Options) (*Job, error) { return nil, errors.ErrUnsupported }

// Close does nothing.
func (j *Job) Close() error { return nil }

// AssignSelf is unsupported.
func (j *Job) AssignSelf() error { return errors.ErrUnsupported }

// Terminate is unsupported.
func (j *Job) Terminate(uint32) error { return errors.ErrUnsupported }

// Contains is unsupported.
func (j *Job) Contains(int) (bool, error) { return false, errors.ErrUnsupported }

// PIDs is unsupported.
func (j *Job) PIDs() ([]int, error) { return nil, errors.ErrUnsupported }

// Start is unsupported.
func (j *Job) Start(*exec.Cmd) error { return errors.ErrUnsupported }

// Task is a placeholder on non-Windows systems.
type Task struct{}

// NewTask always fails on non-Windows systems.
func NewTask(context.Context, *Job) (*Task, error) { return nil, errors.ErrUnsupported }

// Start is unsupported.
func (t *Task) Start(*exec.Cmd) error { return errors.ErrUnsupported }

// Close does nothing.
func (t *Task) Close() error { return nil }

// StartOutside is unsupported.
func StartOutside(func() *exec.Cmd, int) (OutsideResult, error) {
	return OutsideResult{}, errors.ErrUnsupported
}
