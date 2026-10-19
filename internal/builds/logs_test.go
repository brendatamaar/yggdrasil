package builds_test

import (
	"bytes"
	"fmt"
	"strings"
	"sync"
	"testing"
	"testing/fstest"

	"github.com/brendatamaar/yggdrasil/internal/apperr"
	"github.com/brendatamaar/yggdrasil/internal/builds"
	"github.com/brendatamaar/yggdrasil/internal/core"
	"github.com/brendatamaar/yggdrasil/internal/safety"
)

// memLogs is the in-memory fake of builds.LogSink.
type memLogs struct {
	mu   sync.Mutex
	red  safety.Redactor
	pub  builds.LogPublisher
	data map[string]*bytes.Buffer
}

type memWriter struct {
	l    *memLogs
	key  string
	id   core.BuildID
	i, a int
	buf  []byte
}

func memKey(id core.BuildID, i, a int) string { return fmt.Sprintf("%s/%d-%d", id, i, a) }

func (m *memLogs) Open(id core.BuildID, i, a int) (builds.LogWriter, error) {
	m.mu.Lock()
	defer m.mu.Unlock()
	k := memKey(id, i, a)
	if m.data[k] == nil {
		m.data[k] = &bytes.Buffer{}
	}
	return &memWriter{l: m, key: k, id: id, i: i, a: a}, nil
}

func (w *memWriter) emit(line string) {
	line = w.l.red.Redact(line)
	w.l.mu.Lock()
	w.l.data[w.key].WriteString(line + "\n")
	w.l.mu.Unlock()
	w.l.pub.PublishBuildLog(w.id, w.i, w.a, line)
}

func (w *memWriter) Write(p []byte) (int, error) {
	w.buf = append(w.buf, p...)
	for {
		i := bytes.IndexByte(w.buf, '\n')
		if i < 0 {
			return len(p), nil
		}
		w.emit(strings.TrimSuffix(string(w.buf[:i]), "\r"))
		w.buf = w.buf[i+1:]
	}
}

func (w *memWriter) Close() error {
	if len(w.buf) > 0 {
		w.emit(string(w.buf))
		w.buf = nil
	}
	return nil
}

func (m *memLogs) Read(id core.BuildID, i, a int, after int64) ([]byte, int64, int64, error) {
	m.mu.Lock()
	defer m.mu.Unlock()
	b := m.data[memKey(id, i, a)]
	size := int64(0)
	if b != nil {
		size = int64(b.Len())
	}
	if after < 0 || after > size {
		return nil, 0, size, apperr.New(apperr.Invalid, "offset", nil)
	}
	if b == nil {
		return nil, 0, 0, nil
	}
	return b.Bytes()[after:], size, size, nil
}

// LogSinkContract is run against the file sink and the in-memory fake.
func TestLogSinkContract(t *testing.T) {
	impls := map[string]func(t *testing.T, red safety.Redactor, pub builds.LogPublisher) builds.LogSink{
		"file": func(t *testing.T, red safety.Redactor, pub builds.LogPublisher) builds.LogSink {
			return builds.NewFileLogs(t.TempDir(), red, pub)
		},
		"memory": func(_ *testing.T, red safety.Redactor, pub builds.LogPublisher) builds.LogSink {
			return &memLogs{red: red, pub: pub, data: map[string]*bytes.Buffer{}}
		},
	}
	for name, mk := range impls {
		t.Run(name, func(t *testing.T) {
			pub := &recPub{}
			sink := mk(t, safety.NewRedactor(sentinel), pub)
			w, err := sink.Open("bld_1", 2, 1)
			if err != nil {
				t.Fatal(err)
			}
			// Chunks that split a line, a token split across chunks, CRLF and a
			// last line without a newline.
			for _, chunk := range []string{"one\r\ntw", "o " + sentinel[:10], sentinel[10:] + "\nthree"} {
				if _, err := w.Write([]byte(chunk)); err != nil {
					t.Fatal(err)
				}
			}
			if got := len(pub.lines); got != 2 {
				t.Errorf("before Close %d lines published, want 2", got)
			}
			if err := w.Close(); err != nil {
				t.Fatal(err)
			}
			want := []string{"2:one", "2:two [REDACTED]", "2:three"}
			pub.mu.Lock()
			lines := append([]string(nil), pub.lines...)
			pub.mu.Unlock()
			if len(lines) != 3 {
				t.Fatalf("published %q", lines)
			}
			for i, l := range lines {
				if strings.Contains(l, sentinel) {
					t.Errorf("published token: %q", l)
				}
				if i == 1 && !strings.HasPrefix(l, "2:two ") {
					t.Errorf("line %d = %q, want %q", i, l, want[i])
				}
			}
			if lines[0] != want[0] || lines[2] != want[2] {
				t.Errorf("lines %q", lines)
			}
			data, next, size, err := sink.Read("bld_1", 2, 1, 0)
			if err != nil || next != size || int(size) != len(data) || bytes.Contains(data, []byte(sentinel)) {
				t.Fatalf("read %q %d %d %v", data, next, size, err)
			}
			if !strings.HasPrefix(string(data), "one\ntwo ") || !strings.HasSuffix(string(data), "three\n") {
				t.Errorf("data %q", data)
			}
			tail, next2, _, err := sink.Read("bld_1", 2, 1, 4)
			if err != nil || string(tail) != string(data[4:]) || next2 != size {
				t.Errorf("tail %q %d %v", tail, next2, err)
			}
			if _, _, _, err := sink.Read("bld_1", 2, 1, size+1); err == nil {
				t.Error("offset past the end accepted")
			}
			empty, _, _, err := sink.Read("bld_9", 0, 1, 0)
			if err != nil || len(empty) != 0 {
				t.Errorf("never-opened log: %q %v", empty, err)
			}
		})
	}
}

func TestFileLogsRejectBadIDs(t *testing.T) {
	sink := builds.NewFileLogs(t.TempDir(), nil, nil)
	for _, id := range []core.BuildID{"", "a/b", `a\b`, "..", "a..b"} {
		if _, err := sink.Open(id, 0, 1); err == nil {
			t.Errorf("Open accepted %q", id)
		}
	}
	if _, err := sink.Open("bld_1", -1, 1); err == nil {
		t.Error("negative step accepted")
	}
}

func TestFindArtifacts(t *testing.T) {
	fsys := fstest.MapFS{
		"package.json":         {Data: []byte("{}")},
		"dist/a.js":            {Data: []byte("aa")},
		"dist/sub/b.js":        {Data: []byte("bbb")},
		"target/app.jar":       {Data: []byte("jar")},
		"target/app.jar.sha1":  {Data: []byte("s")},
		"web/build/libs/x.jar": {Data: []byte("x")},
		"src/dist/c.js":        {Data: []byte("c")},
	}
	got, err := builds.FindArtifacts(fsys, "rep_a", []string{"dist/**", "target/*.jar", "web/build/libs/*.jar", "../escape/*", "/abs/*", "", "missing/**", "dist/a.js"})
	if err != nil {
		t.Fatal(err)
	}
	var paths []string
	for _, a := range got {
		paths = append(paths, a.Path)
		if a.RepoID != "rep_a" {
			t.Errorf("repo %q", a.RepoID)
		}
	}
	want := []string{"dist/a.js", "dist/sub/b.js", "target/app.jar", "web/build/libs/x.jar"}
	if strings.Join(paths, ",") != strings.Join(want, ",") {
		t.Errorf("paths %v, want %v", paths, want)
	}
	if got[0].Size != 2 {
		t.Errorf("size %d", got[0].Size)
	}
	none, err := builds.FindArtifacts(fsys, "rep_a", nil)
	if err != nil || none == nil || len(none) != 0 {
		t.Errorf("no patterns: %v %v (want a non-nil empty slice)", none, err)
	}
}

func TestFindArtifactsDirFSMatchesMapFS(t *testing.T) {
	dir := t.TempDir()
	mustWrite := func(rel, body string) {
		t.Helper()
		p := dir + "/" + rel
		if err := mkfile(p, body); err != nil {
			t.Fatal(err)
		}
	}
	mustWrite("dist/a.js", "aa")
	mustWrite("dist/sub/b.js", "bbb")
	got, err := builds.FindArtifacts(dirFS(dir), "rep_a", []string{"dist/**"})
	if err != nil || len(got) != 2 || got[1].Path != "dist/sub/b.js" || got[1].Size != 3 {
		t.Errorf("DirFS: %+v %v", got, err)
	}
}
