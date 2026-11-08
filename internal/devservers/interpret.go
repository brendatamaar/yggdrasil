package devservers

import (
	"context"
	"errors"
	"fmt"
	"runtime/debug"
	"slices"
	"time"

	dsc "github.com/brendatamaar/yggdrasil/internal/core/devservers"
	"github.com/brendatamaar/yggdrasil/internal/platform"
	"github.com/brendatamaar/yggdrasil/internal/safety"
)

// sinkFor returns the log of a server, created on first use (s.mu held; the
// caller must not call the sink before releasing it).
func (s *Service) sinkFor(id string) LogSink {
	if sink, ok := s.sinks[id]; ok {
		return sink
	}
	publish := func(line LogLine) {
		if s.d.Hub != nil {
			s.d.Hub.PublishLog(id, line)
		}
	}
	size := s.d.LogLines
	if size < 1 {
		size = RingLines
	}
	sink, err := NewFileSink(s.logDir(), id, s.d.Redactor.Redact, size, s.d.LogFileBytes, publish)
	if err != nil {
		s.log.Warn("dev server log file unavailable; keeping the log in memory", "id", id, "err", err)
		sink, _ = NewMemorySink(s.d.Redactor.Redact, size, publish) // the redactor is required by New
	}
	s.sinks[id] = sink
	return sink
}

// startRun begins the attempt: it owns the process goroutine.
func (s *Service) startRun(key dsc.Key, id, dir string, c dsc.StartProcess) {
	ctx, cancel := context.WithCancel(s.base)
	r := &run{attempt: c.Attempt, cancel: cancel, announced: map[dsc.Port]struct{}{}}
	s.mu.Lock()
	s.runs[id] = r
	sink := s.sinkFor(id)
	s.mu.Unlock()
	sink.Write(StreamSystem, fmt.Sprintf("--- starting (attempt %d) ---", c.Attempt))
	if !s.spawn(func() { s.runProcess(ctx, r, key, id, dir, c, sink) }) {
		cancel()
		s.mu.Lock()
		if s.runs[id] == r {
			delete(s.runs, id)
		}
		s.mu.Unlock()
	}
}

// runProcess starts the process and waits for it. Every start goes through
// platform.Start with the clean environment (invariant 3, SF-04), in the
// tracked folder the Repos capability resolved from IDs (invariant 6).
func (s *Service) runProcess(ctx context.Context, r *run, key dsc.Key, id, dir string, c dsc.StartProcess, sink LogSink) {
	exited := false
	procID := ProcessID(id, c.Attempt)
	defer func() {
		if p := recover(); p != nil {
			s.log.Error("dev server goroutine panicked", "id", id, "panic", fmt.Sprint(p), "stack", string(debug.Stack()))
			if !exited {
				s.feed(key, dsc.Exited{Attempt: c.Attempt, Code: -1, Now: s.clock.Now()})
			}
		}
		r.cancel()
		s.mu.Lock()
		if s.runs[id] == r {
			delete(s.runs, id)
		}
		s.signalLocked()
		s.mu.Unlock()
	}()

	fail := func(reason string, err error) {
		if err != nil {
			s.log.Warn("dev server could not start", "id", id, "err", err)
		}
		s.feed(key, dsc.StartFailed{Attempt: c.Attempt, Reason: reason, Now: s.clock.Now()})
		exited = true
	}
	env, err := s.d.Env.Build(safety.EnvOptions{Extra: c.Expanded.Env})
	if err != nil {
		fail("Yggdrasil could not prepare a safe environment for this server.", err)
		return
	}
	argv := slices.Clone(c.Expanded.Argv)
	exe, err := s.d.Resolve(argv[0], dir, env)
	if err != nil {
		if errors.Is(err, platform.ErrNotFound) {
			fail(fmt.Sprintf("Install %s and try again.", argv[0]), err)
		} else {
			fail("Yggdrasil could not find the program this server runs.", err)
		}
		return
	}
	argv[0] = exe
	wOut := newLineWriter(s.d.Redactor.Redact, func(line string) { s.onLine(r, key, id, sink, StreamStdout, line) })
	wErr := newLineWriter(s.d.Redactor.Redact, func(line string) { s.onLine(r, key, id, sink, StreamStderr, line) })
	proc, err := s.d.Start(ctx, platform.Spec{Argv: argv, Dir: dir, Env: env, TaskID: procID, Stdout: wOut, Stderr: wErr})
	if err != nil {
		switch {
		case errors.Is(err, platform.ErrNotFound):
			fail(fmt.Sprintf("Install %s and try again.", c.Expanded.Argv[0]), err)
		case errors.Is(err, platform.ErrDirMissing):
			fail("The folder for this server no longer exists.", err)
		case ctx.Err() != nil:
			fail("The server was stopped before it started.", nil)
		default:
			fail("The server could not be started. Check that its program is installed.", err)
		}
		return
	}
	r.mu.Lock()
	r.pid = proc.Pid()
	r.mu.Unlock()
	if err := s.d.Orphans.Record(procID, proc.Pid()); err != nil {
		s.log.Warn("could not record the dev server process", "id", id, "err", err)
	}
	s.feed(key, dsc.Started{Attempt: c.Attempt, Pid: proc.Pid(), Now: s.clock.Now()})
	code, werr := proc.Wait()
	if werr != nil {
		s.log.Warn("waiting for a dev server failed", "id", id, "err", werr)
	}
	_ = proc.Truncated()
	wOut.Flush()
	wErr.Flush()
	if err := s.d.Orphans.Forget(procID); err != nil {
		s.log.Warn("could not forget the dev server process", "id", id, "err", err)
	}
	exited = true
	s.feed(key, dsc.Exited{Attempt: c.Attempt, Code: code, Now: s.clock.Now()})
}

// onLine logs a line and collects the ports it announces (DV-02). Announced
// ports reach the core batched, so a chatty server is one commit per 250 ms.
func (s *Service) onLine(r *run, key dsc.Key, id string, sink LogSink, stream Stream, line string) {
	sink.Write(stream, line)
	ports := dsc.ParseAnnouncement(line)
	if len(ports) == 0 {
		return
	}
	r.mu.Lock()
	for _, p := range ports {
		r.announced[p] = struct{}{}
	}
	schedule := !r.pending
	r.pending = true
	r.mu.Unlock()
	if schedule {
		s.wakes.At(fmt.Sprintf("announce|%s|%d", id, r.attempt), s.clock.Now().Add(announceDelay), func(time.Time) {
			s.sendAnnounced(key, r)
		})
	}
}

func (r *run) announcedList() []dsc.Port {
	r.mu.Lock()
	defer r.mu.Unlock()
	out := make([]dsc.Port, 0, len(r.announced))
	for p := range r.announced {
		out = append(out, p)
	}
	slices.Sort(out)
	return out
}

func (r *run) sameAnnounced(stored []dsc.Port) bool {
	return slices.Equal(r.announcedList(), stored) || (len(stored) == 0 && len(r.announcedList()) == 0)
}

func (s *Service) sendAnnounced(key dsc.Key, r *run) {
	r.mu.Lock()
	r.pending = false
	r.mu.Unlock()
	if list := r.announcedList(); len(list) > 0 {
		s.feed(key, dsc.OutputAnnounced{Attempt: r.attempt, Ports: list})
	}
}

// kill ends the attempt's process tree: it cancels the process context and
// kills the attempt's job, which is keyed by attempt: it cannot reach a newer
// attempt of the same server (the work runs in the group).
func (s *Service) kill(id string, attempt int) {
	s.mu.Lock()
	r := s.runs[id]
	s.mu.Unlock()
	if r == nil || r.attempt != attempt {
		return
	}
	s.spawn(func() {
		r.cancel()
		if err := s.d.KillTask(ProcessID(id, attempt)); err != nil {
			s.log.Warn("could not kill the dev server job", "id", id, "err", err)
		}
	})
}

// poll reads the listening ports of the attempt's process tree.
func (s *Service) poll(key dsc.Key, id string, attempt int) {
	s.mu.Lock()
	r := s.runs[id]
	s.mu.Unlock()
	if r == nil || r.attempt != attempt {
		return
	}
	r.mu.Lock()
	pid := r.pid
	r.mu.Unlock()
	if pid == 0 {
		return
	}
	s.spawn(func() {
		raw, err := s.d.Ports(pid)
		if err != nil {
			s.log.Debug("could not read the ports of a dev server", "id", id, "err", err)
			return // keep the last ports; the next poll tries again
		}
		ports, dropped := dsc.NormalizePorts(raw)
		if dropped > 0 {
			s.log.Debug("ignored ports outside 1..65535", "id", id, "count", dropped)
		}
		s.feed(key, dsc.PortsObserved{Attempt: attempt, Ports: ports, Now: s.clock.Now()})
	})
}

// stopOrphan stops a process left by a crashed run. A stop that failed while
// the process is still there is an input too (OrphanStopFailed), so it is
// audited and shown.
func (s *Service) stopOrphan(key dsc.Key, id string, attempt, pid int) {
	procID := ProcessID(id, attempt)
	s.spawn(func() {
		if err := s.d.Orphans.Stop(procID); err != nil {
			s.log.Warn("could not stop an orphaned dev server", "id", id, "pid", pid, "err", err)
			if !s.orphanGone(procID) {
				s.feed(key, dsc.OrphanStopFailed{Now: s.clock.Now()})
				return
			}
		}
		s.feed(key, dsc.OrphanStopped{Now: s.clock.Now()})
	})
}

func (s *Service) orphanGone(id string) bool {
	list, err := s.d.Orphans.List()
	if err != nil {
		return false
	}
	for _, o := range list {
		if o.ID == id {
			return false
		}
	}
	return true
}

// Reconcile settles the servers a previous run left live (DV-03). It is
// called once at startup, before requests are served. With a job object
// (Windows) nothing survives a crashed run, so ListOrphans finds none and
// every live row becomes Stopped{Interrupted}. Elsewhere a recorded process
// that is still alive becomes Orphaned, which the user can stop.
func (s *Service) Reconcile(ctx context.Context) error {
	rows, err := s.d.Store.ListLive(ctx)
	if err != nil {
		return fmt.Errorf("list live dev servers: %w", err)
	}
	alive := map[string]int{}
	if list, err := s.d.Orphans.List(); err != nil {
		s.log.Warn("could not list left-over dev server processes", "err", err)
	} else {
		for _, o := range list {
			alive[o.ID] = o.Pid
		}
	}
	for _, row := range rows {
		cur, err := dsc.ServerFromRow(row.Row)
		if err != nil {
			s.log.Warn("skipping an unreadable dev server row", "id", row.ID, "err", err)
			continue
		}
		procID := ProcessID(row.ID, row.Row.Attempt)
		pid, isAlive := alive[procID]
		if _, orphan := cur.(dsc.Orphaned); orphan {
			if isAlive {
				continue
			}
			s.feed(row.Row.Key, dsc.OrphanStopped{Now: s.clock.Now()})
			continue
		}
		var live dsc.Liveness = dsc.Dead{}
		if isAlive {
			live = dsc.Alive{Pid: pid}
		} else if err := s.d.Orphans.Forget(procID); err != nil {
			s.log.Debug("could not forget a dead dev server process", "id", row.ID, "err", err)
		}
		s.feed(row.Row.Key, dsc.Recovered{Liveness: live, Now: s.clock.Now()})
	}
	return nil
}
