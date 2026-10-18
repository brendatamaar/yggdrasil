// Package platformtest is the test support for internal/platform: a
// deterministic fake of platform.Start and the contract suite that both the
// fake and the real Start must pass (docs/AGENTS.md 5.11). Packages that run
// processes (builds, devservers, agents) take a platform.StartFunc and use the
// fake in their tests.
package platformtest

import (
	"context"
	"errors"
	"fmt"
	"io"
	"strconv"
	"sync"

	"github.com/brendatamaar/yggdrasil/internal/platform"
)

// Script says what a fake process does.
type Script struct {
	// Stdout and Stderr are written to Spec.Stdout and Spec.Stderr (if set) at
	// start, before Wait can return.
	Stdout, Stderr string
	// Exit is the exit code when the process ends by itself.
	Exit int
	// Block keeps the process running until Kill or its context is cancelled.
	// It then ends with KilledExit.
	Block bool
	// StartErr, if set, is returned by Start instead of a process.
	StartErr error
	// Truncated makes Process.Truncated report true: the output was cut off.
	Truncated bool
}

// KilledExit is the exit code of a fake process that was killed or cancelled.
const KilledExit = 1

// ArgvPrefix is Argv[0] of the commands the fake understands when no Script is
// queued:
//
//	fake exit <code>   end with that code
//	fake print <text>  write text and a newline to stdout, exit 0
//	fake block         run until killed
//
// Any other Argv[0] is a program that doesn't exist (platform.ErrNotFound).
const ArgvPrefix = "fake"

// Fake is a platform.StartFunc without processes. It is safe for concurrent
// use.
type Fake struct {
	mu      sync.Mutex
	queue   []Script
	started []platform.Spec
	pid     int
	tasks   map[string][]*fakeProcess
	killed  []string

	// DirExists, if set, says whether Spec.Dir exists; Start fails with
	// platform.ErrDirMissing when it returns false. Nil means every folder
	// exists.
	DirExists func(dir string) bool
}

// Push queues scripts; each Start takes the next one. When the queue is empty,
// Start reads the script from Spec.Argv (see ArgvPrefix).
func (f *Fake) Push(s ...Script) {
	f.mu.Lock()
	defer f.mu.Unlock()
	f.queue = append(f.queue, s...)
}

// Started returns the specs of the processes started so far, in order.
func (f *Fake) Started() []platform.Spec {
	f.mu.Lock()
	defer f.mu.Unlock()
	return append([]platform.Spec(nil), f.started...)
}

var (
	_ platform.StartFunc    = (*Fake)(nil).Start
	_ platform.KillTaskFunc = (*Fake)(nil).KillTask
)

// KillTask kills every fake process started with Spec.TaskID == taskID, like
// platform.KillTask, and records the call (also for an unknown ID, which does
// nothing else).
func (f *Fake) KillTask(taskID string) error {
	f.mu.Lock()
	f.killed = append(f.killed, taskID)
	procs := append([]*fakeProcess(nil), f.tasks[taskID]...)
	f.mu.Unlock()
	for _, p := range procs {
		_ = p.Kill()
	}
	return nil
}

// KilledTasks returns the task IDs passed to KillTask, in order.
func (f *Fake) KilledTasks() []string {
	f.mu.Lock()
	defer f.mu.Unlock()
	return append([]string(nil), f.killed...)
}

// Start applies the same checks as platform.Start (environment, argv, context)
// and then runs the next script.
func (f *Fake) Start(ctx context.Context, spec platform.Spec) (platform.Process, error) {
	if spec.Env == nil {
		return nil, platform.ErrNoEnv
	}
	if len(spec.Argv) == 0 {
		return nil, errors.New("platform: Argv is empty")
	}
	if err := ctx.Err(); err != nil {
		return nil, err
	}
	if spec.Dir != "" && f.DirExists != nil && !f.DirExists(spec.Dir) {
		return nil, fmt.Errorf("%w: %s", platform.ErrDirMissing, spec.Dir)
	}
	f.mu.Lock()
	var s Script
	var err error
	if len(f.queue) > 0 {
		s, f.queue = f.queue[0], f.queue[1:]
	} else {
		s, err = scriptFromArgv(spec.Argv)
	}
	if err == nil && s.StartErr != nil {
		err = s.StartErr
	}
	if err != nil {
		f.mu.Unlock()
		return nil, err
	}
	f.started = append(f.started, spec)
	f.pid++
	pid := 40000 + f.pid
	f.mu.Unlock()

	write(spec.Stdout, s.Stdout)
	write(spec.Stderr, s.Stderr)
	p := &fakeProcess{pid: pid, truncated: s.Truncated, done: make(chan struct{})}
	if !s.Block {
		p.finish(s.Exit)
		return p, nil
	}
	if spec.TaskID != "" {
		f.mu.Lock()
		if f.tasks == nil {
			f.tasks = map[string][]*fakeProcess{}
		}
		f.tasks[spec.TaskID] = append(f.tasks[spec.TaskID], p)
		f.mu.Unlock()
	}
	stop := context.AfterFunc(ctx, func() { _ = p.Kill() })
	go func() { <-p.done; stop() }()
	return p, nil
}

func write(w io.Writer, s string) {
	if w != nil && s != "" {
		_, _ = io.WriteString(w, s)
	}
}

func scriptFromArgv(argv []string) (Script, error) {
	if argv[0] != ArgvPrefix {
		return Script{}, fmt.Errorf("fake start %s: %w", argv[0], platform.ErrNotFound)
	}
	if len(argv) < 2 {
		return Script{}, errors.New("fake: no command")
	}
	switch argv[1] {
	case "exit":
		if len(argv) != 3 {
			return Script{}, errors.New("fake: exit needs a code")
		}
		n, err := strconv.Atoi(argv[2])
		if err != nil {
			return Script{}, fmt.Errorf("fake: exit code: %w", err)
		}
		return Script{Exit: n}, nil
	case "print":
		if len(argv) != 3 {
			return Script{}, errors.New("fake: print needs one text")
		}
		return Script{Stdout: argv[2] + "\n"}, nil
	case "block":
		return Script{Block: true}, nil
	case "leak":
		if len(argv) != 3 {
			return Script{}, errors.New("fake: leak needs a code")
		}
		n, err := strconv.Atoi(argv[2])
		if err != nil {
			return Script{}, fmt.Errorf("fake: leak code: %w", err)
		}
		return Script{Stdout: "before-leak\n", Exit: n, Truncated: true}, nil
	case "batch-unsafe":
		return Script{StartErr: fmt.Errorf("%w: argument 1 holds a double quote", platform.ErrBatchArg)}, nil
	}
	return Script{}, fmt.Errorf("fake: unknown command %q", argv[1])
}

type fakeProcess struct {
	pid       int
	truncated bool
	once      sync.Once
	done      chan struct{}
	code      int
}

func (p *fakeProcess) Truncated() bool {
	<-p.done
	return p.truncated
}

func (p *fakeProcess) finish(code int) {
	p.once.Do(func() {
		p.code = code
		close(p.done)
	})
}

func (p *fakeProcess) Pid() int { return p.pid }

func (p *fakeProcess) Wait() (int, error) {
	<-p.done
	return p.code, nil
}

func (p *fakeProcess) Kill() error {
	p.finish(KilledExit)
	return nil
}
