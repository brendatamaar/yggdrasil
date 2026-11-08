package devservers

import (
	"bytes"
	"errors"
	"fmt"
	"io"
	"os"
	"path/filepath"
	"strings"
	"sync"
)

// Log limits (docs/design/devservers.md, Persistence).
const (
	// RingLines is how many recent lines a server keeps in memory.
	RingLines = 1000
	// LogFileBytes is the size at which a log file rotates.
	LogFileBytes = 5 << 20
	// LogFilesKept is the number of log files kept per server.
	LogFilesKept = 3
	// MaxLineBytes cuts longer lines.
	MaxLineBytes = 8 << 10
)

// LogSink receives the output lines of one server. Lines are redacted before
// they are stored or published. Write after Close does nothing.
type LogSink interface {
	Write(line string)
	// Tail returns the last n lines (all if fewer), oldest first.
	Tail(n int) []string
	Close() error
}

// Redact masks secrets in a line (safety.Redactor.Redact).
type Redact func(string) string

// sinkParts wires a sink; file may be nil.
type sinkParts struct {
	redact  Redact
	size    int
	publish func(line string)
	file    *rotatingFile
}

// logSink is the ring + file + publish implementation of LogSink.
type logSink struct {
	mu      sync.Mutex
	closed  bool
	redact  Redact
	ring    []string
	start   int
	n       int
	publish func(string)
	file    *rotatingFile
}

// NewMemorySink returns a LogSink that keeps the last size lines in memory
// and publishes each (redacted) line. publish may be nil.
func NewMemorySink(redact Redact, size int, publish func(line string)) LogSink {
	return newSink(sinkParts{redact: redact, size: size, publish: publish})
}

// NewFileSink is NewMemorySink plus a log file in dir named id.log. The file
// rotates at maxBytes and keeps LogFilesKept files (id.log, id.log.1, ...).
// A disk error stops the file, adds one warning line to the ring, and the
// ring keeps working.
func NewFileSink(dir, id string, redact Redact, size int, maxBytes int64, publish func(line string)) (LogSink, error) {
	if err := os.MkdirAll(dir, 0o700); err != nil {
		return nil, fmt.Errorf("create log folder: %w", err)
	}
	f, err := openRotating(filepath.Join(dir, id+".log"), maxBytes, LogFilesKept)
	if err != nil {
		return nil, err
	}
	return newSink(sinkParts{redact: redact, size: size, publish: publish, file: f}), nil
}

func newSink(p sinkParts) *logSink {
	if p.redact == nil {
		p.redact = func(s string) string { return s }
	}
	if p.size < 1 {
		p.size = RingLines
	}
	return &logSink{redact: p.redact, ring: make([]string, p.size), publish: p.publish, file: p.file}
}

func (l *logSink) Write(line string) {
	// Redact the whole line first, then cut it: cutting first could split a
	// secret in two halves that no pattern matches.
	line = l.redact(strings.TrimRight(line, "\r\n"))
	if len(line) > MaxLineBytes {
		line = line[:MaxLineBytes] + " [cut]"
	}
	l.mu.Lock()
	if l.closed {
		l.mu.Unlock()
		return
	}
	l.push(line)
	if l.file != nil {
		if err := l.file.write(line); err != nil {
			l.file = nil
			l.push("[the log file stopped because of a disk error; recent lines stay here]")
		}
	}
	pub := l.publish
	l.mu.Unlock()
	if pub != nil {
		pub(line)
	}
}

func (l *logSink) push(line string) {
	if l.n < len(l.ring) {
		l.ring[(l.start+l.n)%len(l.ring)] = line
		l.n++
		return
	}
	l.ring[l.start] = line
	l.start = (l.start + 1) % len(l.ring)
}

func (l *logSink) Tail(n int) []string {
	l.mu.Lock()
	defer l.mu.Unlock()
	if n < 0 || n > l.n {
		n = l.n
	}
	out := make([]string, 0, n)
	for i := l.n - n; i < l.n; i++ {
		out = append(out, l.ring[(l.start+i)%len(l.ring)])
	}
	return out
}

func (l *logSink) Close() error {
	l.mu.Lock()
	defer l.mu.Unlock()
	if l.closed {
		return nil
	}
	l.closed = true
	if l.file != nil {
		err := l.file.close()
		l.file = nil
		return err
	}
	return nil
}

// rotatingFile appends lines to path and rotates by rename at maxBytes: path
// becomes path.1, path.1 becomes path.2, and the rename replaces (overwrites)
// the oldest rotated file. That is the only way a log file goes away; no file
// is removed with os.Remove, and only Yggdrasil's own log files are touched.
type rotatingFile struct {
	path string
	max  int64
	keep int
	f    *os.File
	size int64
}

func openRotating(path string, max int64, keep int) (*rotatingFile, error) {
	if max < 1 {
		max = LogFileBytes
	}
	r := &rotatingFile{path: path, max: max, keep: keep}
	if err := r.open(); err != nil {
		return nil, err
	}
	return r, nil
}

func (r *rotatingFile) open() error {
	f, err := os.OpenFile(r.path, os.O_CREATE|os.O_WRONLY|os.O_APPEND, 0o600)
	if err != nil {
		return fmt.Errorf("open log file: %w", err)
	}
	st, err := f.Stat()
	if err != nil {
		_ = f.Close()
		return fmt.Errorf("stat log file: %w", err)
	}
	r.f, r.size = f, st.Size()
	return nil
}

func (r *rotatingFile) write(line string) error {
	if r.f == nil {
		return errors.New("log file closed")
	}
	n := int64(len(line) + 1)
	if r.size > 0 && r.size+n > r.max {
		if err := r.rotate(); err != nil {
			return err
		}
	}
	if _, err := io.WriteString(r.f, line+"\n"); err != nil {
		return err
	}
	r.size += n
	return nil
}

func (r *rotatingFile) rotate() error {
	if err := r.f.Close(); err != nil {
		return err
	}
	r.f = nil
	for i := r.keep - 1; i >= 1; i-- {
		from := r.path
		if i > 1 {
			from = fmt.Sprintf("%s.%d", r.path, i-1)
		}
		if err := os.Rename(from, fmt.Sprintf("%s.%d", r.path, i)); err != nil && !errors.Is(err, os.ErrNotExist) {
			return err
		}
	}
	return r.open()
}

func (r *rotatingFile) close() error {
	if r.f == nil {
		return nil
	}
	err := r.f.Close()
	r.f = nil
	return err
}

// lineWriter turns the bytes of a process's output into lines. It is safe for
// the two goroutines that copy stdout and stderr.
type lineWriter struct {
	mu     sync.Mutex
	buf    []byte
	redact Redact
	emit   func(line string)
}

// carryBytes is how much of an over-long, unterminated line is held back
// when a piece is emitted, so a secret that straddles the cut is redacted
// whole in the next piece.
const carryBytes = 1024

func newLineWriter(redact Redact, emit func(string)) *lineWriter {
	if redact == nil {
		redact = func(s string) string { return s }
	}
	return &lineWriter{redact: redact, emit: emit}
}

func (w *lineWriter) Write(p []byte) (int, error) {
	w.mu.Lock()
	defer w.mu.Unlock()
	w.buf = append(w.buf, p...)
	for {
		i := bytes.IndexByte(w.buf, '\n')
		if i < 0 {
			break
		}
		w.emit(strings.TrimRight(string(w.buf[:i]), "\r"))
		w.buf = w.buf[i+1:]
	}
	if len(w.buf) > MaxLineBytes {
		// No line break for a long while. Redact everything buffered, emit all
		// but the last carryBytes, and keep that tail (already redacted where a
		// secret was complete) in front of the next bytes.
		red := w.redact(string(w.buf))
		if len(red) > carryBytes {
			w.emit(red[:len(red)-carryBytes])
			w.buf = []byte(red[len(red)-carryBytes:])
		} else {
			w.buf = []byte(red)
		}
	}
	return len(p), nil
}

// Flush emits a last line that had no line break.
func (w *lineWriter) Flush() {
	w.mu.Lock()
	defer w.mu.Unlock()
	if len(w.buf) > 0 {
		w.emit(strings.TrimRight(string(w.buf), "\r"))
		w.buf = nil
	}
}
