package builds

import (
	"bytes"
	"context"
	"errors"
	"fmt"
	"io"
	"os"
	"path/filepath"
	"strconv"
	"strings"
	"sync"

	"github.com/brendatamaar/yggdrasil/internal/apperr"
	"github.com/brendatamaar/yggdrasil/internal/core"
	corebuilds "github.com/brendatamaar/yggdrasil/internal/core/builds"
	"github.com/brendatamaar/yggdrasil/internal/safety"
)

const (
	// maxLogLine cuts a line that never ends, so a binary dump can't grow the
	// buffer without bound.
	maxLogLine = 64 << 10
	// MaxLogChunk is the most bytes one Read returns.
	MaxLogChunk = 1 << 20
)

// LogSink stores the output of build steps (BL-06). Writes are redacted before
// they reach the file or the live stream (invariant 4, S5).
type LogSink interface {
	// Open creates the log of a step run and returns its writer. Write takes
	// raw output in any chunking; the sink splits it into lines. Close flushes
	// a last partial line.
	Open(id core.BuildID, index, attempt int) (LogWriter, error)
	// Read returns the bytes from offset after (at most MaxLogChunk), the
	// offset to ask for next and the current size. An offset beyond the size
	// is an apperr.Invalid error. A log that was never opened reads as empty.
	Read(id core.BuildID, index, attempt int, after int64) (data []byte, next, size int64, err error)
}

// LogWriter receives the output of one step run.
type LogWriter interface {
	io.Writer
	Close() error
}

// LogPublisher receives each redacted line for the live stream.
type LogPublisher interface {
	PublishBuildLog(id core.BuildID, step, attempt int, line string)
}

func logRoot(dataDir string) string { return filepath.Join(dataDir, "logs", "builds") }

// FileLogs is the LogSink that writes <root>/<buildID>/<index>-<attempt>.log.
type FileLogs struct {
	root string
	red  safety.Redactor
	pub  LogPublisher
}

// NewFileLogs returns a file log sink. pub may be nil.
func NewFileLogs(root string, red safety.Redactor, pub LogPublisher) *FileLogs {
	if red == nil {
		red = safety.NewRedactor()
	}
	return &FileLogs{root: root, red: red, pub: pub}
}

func (f *FileLogs) path(id core.BuildID, index, attempt int) (string, error) {
	if err := id.Validate(); err != nil || strings.Contains(string(id), "..") {
		return "", fmt.Errorf("log path: invalid build id %q", string(id))
	}
	if index < 0 || attempt < 0 {
		return "", errors.New("log path: negative step or attempt")
	}
	return filepath.Join(f.root, string(id), strconv.Itoa(index)+"-"+strconv.Itoa(attempt)+".log"), nil
}

// Open implements LogSink.
func (f *FileLogs) Open(id core.BuildID, index, attempt int) (LogWriter, error) {
	p, err := f.path(id, index, attempt)
	if err != nil {
		return nil, err
	}
	if err := os.MkdirAll(filepath.Dir(p), 0o700); err != nil {
		return nil, fmt.Errorf("create log folder: %w", err)
	}
	file, err := os.OpenFile(p, os.O_CREATE|os.O_WRONLY|os.O_APPEND, 0o600) //nolint:gosec // p is built from a validated build ID under the data dir
	if err != nil {
		return nil, fmt.Errorf("open step log: %w", err)
	}
	return &lineWriter{out: file, red: f.red, pub: f.pub, id: id, index: index, attempt: attempt}, nil
}

// Read implements LogSink.
func (f *FileLogs) Read(id core.BuildID, index, attempt int, after int64) ([]byte, int64, int64, error) {
	p, err := f.path(id, index, attempt)
	if err != nil {
		return nil, 0, 0, apperr.New(apperr.Invalid, "That isn't a valid build step.", err)
	}
	file, err := os.Open(p) //nolint:gosec // see path
	if errors.Is(err, os.ErrNotExist) {
		if after != 0 {
			return nil, 0, 0, offsetErr(after, 0)
		}
		return nil, 0, 0, nil
	}
	if err != nil {
		return nil, 0, 0, fmt.Errorf("open step log: %w", err)
	}
	defer func() { _ = file.Close() }()
	fi, err := file.Stat()
	if err != nil {
		return nil, 0, 0, fmt.Errorf("stat step log: %w", err)
	}
	size := fi.Size()
	if after < 0 || after > size {
		return nil, 0, size, offsetErr(after, size)
	}
	n := min(size-after, MaxLogChunk)
	buf := make([]byte, n)
	if _, err := file.ReadAt(buf, after); err != nil && !errors.Is(err, io.EOF) {
		return nil, 0, size, fmt.Errorf("read step log: %w", err)
	}
	return buf, after + n, size, nil
}

func offsetErr(after, size int64) error {
	return apperr.New(apperr.Invalid, fmt.Sprintf("The log offset %d is past the end of the log (%d bytes). Ask again from 0.", after, size), nil)
}

// lineWriter splits output into lines, redacts each, writes it and publishes it.
type lineWriter struct {
	mu      sync.Mutex
	out     io.WriteCloser
	red     safety.Redactor
	pub     LogPublisher
	id      core.BuildID
	index   int
	attempt int
	buf     []byte
	closed  bool
	err     error
}

func (w *lineWriter) Write(p []byte) (int, error) {
	w.mu.Lock()
	defer w.mu.Unlock()
	if w.closed {
		return len(p), nil
	}
	w.buf = append(w.buf, p...)
	for {
		i := bytes.IndexByte(w.buf, '\n')
		switch {
		case i >= 0:
			w.emit(string(bytes.TrimSuffix(w.buf[:i], []byte{'\r'})))
			w.buf = w.buf[i+1:]
		case len(w.buf) >= maxLogLine:
			w.emit(string(w.buf))
			w.buf = w.buf[:0]
		default:
			return len(p), nil
		}
	}
}

func (w *lineWriter) emit(line string) {
	line = w.red.Redact(line)
	if _, err := io.WriteString(w.out, line+"\n"); err != nil && w.err == nil {
		w.err = err
	}
	if w.pub != nil {
		w.pub.PublishBuildLog(w.id, w.index, w.attempt, line)
	}
}

// Close flushes a last line without a newline and closes the file.
func (w *lineWriter) Close() error {
	w.mu.Lock()
	defer w.mu.Unlock()
	if w.closed {
		return w.err
	}
	w.closed = true
	if len(w.buf) > 0 {
		w.emit(string(bytes.TrimSuffix(w.buf, []byte{'\r'})))
		w.buf = nil
	}
	if err := w.out.Close(); err != nil && w.err == nil {
		w.err = err
	}
	return w.err
}

// StepLog returns the log of one step of a build from byte offset after, and
// the offset to ask for next. A step that has not started has an empty log.
func (s *Service) StepLog(ctx context.Context, id core.BuildID, step int, after int64) ([]byte, int64, error) {
	st, err := s.Get(ctx, id)
	if err != nil {
		return nil, 0, err
	}
	var steps []corebuilds.StepState
	switch x := st.(type) {
	case corebuilds.Running:
		steps = x.Steps
	case corebuilds.Cancelling:
		steps = x.Steps
	case corebuilds.Finished:
		steps = x.Steps
	}
	if _, ok := st.(corebuilds.Queued); ok {
		spec, _ := specOf(st)
		if step < 0 || step >= len(spec.Steps) {
			return nil, 0, apperr.New(apperr.NotFound, "That step doesn't exist.", nil)
		}
		return nil, after, nil
	}
	if step < 0 || step >= len(steps) {
		return nil, 0, apperr.New(apperr.NotFound, "That step doesn't exist.", nil)
	}
	attempt := 0
	switch x := steps[step].(type) {
	case corebuilds.StepRunning:
		attempt = x.Attempt
	case corebuilds.StepDone:
		attempt = x.Attempt
	default:
		return nil, after, nil
	}
	data, next, _, err := s.d.Logs.Read(id, step, attempt, after)
	if err != nil {
		if _, ok := apperr.ClassOf(err); ok {
			return nil, 0, err
		}
		return nil, 0, unavailable("Yggdrasil couldn't read the log. Try again.", err)
	}
	return data, next, nil
}
