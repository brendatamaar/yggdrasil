package platform

import (
	"io"
	"os"
	"os/exec"
	"sync"
	"time"
)

// outputs copies a child's stdout and stderr to the caller's writers.
//
// exec.Cmd can do this itself, but when the process exits with a non-zero code
// and a grandchild still holds the pipe, Wait returns the ExitError and the
// "output was cut off" signal (exec.ErrWaitDelay) is lost. A failed build with a
// cut-off log is the log people read, so Start copies the output itself and
// knows exactly whether it drained everything.
type outputs struct {
	childEnds []*os.File // write ends, closed in the parent after the start
	readEnds  []*os.File
	gates     []*gate
	wg        sync.WaitGroup
}

// gate is a writer that can be shut: once closed, later writes are dropped, so
// a copier that is still blocked after a timeout can never write into the
// caller's buffer once Wait has returned.
type gate struct {
	mu     sync.Mutex
	w      io.Writer
	closed bool
}

func (g *gate) Write(p []byte) (int, error) {
	g.mu.Lock()
	defer g.mu.Unlock()
	if g.closed {
		return len(p), nil
	}
	return g.w.Write(p)
}

func (g *gate) shut() {
	g.mu.Lock()
	g.closed = true
	g.mu.Unlock()
}

// newOutputs wires cmd.Stdout and cmd.Stderr. A nil writer goes to the null
// device, an *os.File is passed through, anything else gets a pipe and a
// copier. The same writer for both streams shares one pipe, so their lines
// keep their order. Call started after cmd.Start, whether it succeeded or not.
func newOutputs(cmd *exec.Cmd, stdout, stderr io.Writer) (*outputs, error) {
	o := &outputs{}
	var shared io.Writer
	var sharedEnd *os.File
	wire := func(w io.Writer) (any, error) {
		if w == nil {
			return nil, nil
		}
		if f, ok := w.(*os.File); ok {
			return f, nil
		}
		if shared != nil && sameWriter(shared, w) {
			return sharedEnd, nil
		}
		r, wr, err := os.Pipe()
		if err != nil {
			return nil, err
		}
		g := &gate{w: w}
		o.readEnds, o.childEnds, o.gates = append(o.readEnds, r), append(o.childEnds, wr), append(o.gates, g)
		o.wg.Add(1)
		go func() {
			defer o.wg.Done()
			_, _ = io.Copy(g, r)
		}()
		shared, sharedEnd = w, wr
		return wr, nil
	}
	so, err := wire(stdout)
	if err != nil {
		o.abort()
		return nil, err
	}
	se, err := wire(stderr)
	if err != nil {
		o.abort()
		return nil, err
	}
	// A nil *os.File in an interface is not a nil writer; set only what exists.
	if f, ok := so.(*os.File); ok {
		cmd.Stdout = f
	}
	if f, ok := se.(*os.File); ok {
		cmd.Stderr = f
	}
	return o, nil
}

// sameWriter reports whether a and b are the same writer. Writers with an
// uncomparable dynamic type are never the same.
func sameWriter(a, b io.Writer) (same bool) {
	defer func() {
		if recover() != nil {
			same = false
		}
	}()
	return a == b
}

// started closes the parent's copies of the pipes' write ends, so the copiers
// see the end of the output once the child (and any grandchild) lets go.
func (o *outputs) started() {
	for _, f := range o.childEnds {
		_ = f.Close()
	}
	o.childEnds = nil
}

// abort releases everything after a failed start.
func (o *outputs) abort() {
	o.started()
	for _, g := range o.gates {
		g.shut()
	}
	for _, f := range o.readEnds {
		_ = f.Close()
	}
	o.readEnds = nil
}

// drain waits up to d for all output to arrive. It reports whether it did. If
// not (a grandchild still holds the pipe), the caller's writers are shut and
// the pipes closed: the output was cut off.
func (o *outputs) drain(d time.Duration) bool {
	done := make(chan struct{})
	go func() { o.wg.Wait(); close(done) }()
	timer := time.NewTimer(d)
	defer timer.Stop()
	select {
	case <-done:
		for _, f := range o.readEnds {
			_ = f.Close()
		}
		return true
	case <-timer.C:
		o.abort()
		return false
	}
}
