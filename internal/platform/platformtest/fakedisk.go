package platformtest

import (
	"context"
	"errors"
	"fmt"
	"io/fs"
	"path/filepath"
	"strings"
	"sync"
	"testing"

	"github.com/brendatamaar/yggdrasil/internal/platform"
)

// FSFixture lets a contract suite arrange and inspect the disk behind a
// SafeInitFS or IgnoreWriter without going through the capability under test:
// real folders for the OS implementation, an in-memory tree for the fakes.
// Optional funcs left nil skip their clauses.
type FSFixture struct {
	// Mkdir creates dir and its parents.
	Mkdir func(t *testing.T, dir string)
	// WriteFile creates or replaces a file (parents must exist).
	WriteFile func(t *testing.T, path string, data []byte)
	// ReadFile returns a file's bytes; exists is false when absent.
	ReadFile func(t *testing.T, path string) (data []byte, exists bool)
	// Symlink makes link a symlink to the existing target and reports false
	// if the OS cannot (Windows without the privilege).
	Symlink func(t *testing.T, link, target string) bool
	// Move renames a file or folder (with its content).
	Move func(t *testing.T, from, to string)
	// Lock holds the file open the way another program does and returns the
	// release func, or nil release when this disk cannot lock.
	Lock func(t *testing.T, path string) (release func())
}

// FixtureProvider is implemented by the capability values handed to the
// contract suites, so a suite can reach the matching FSFixture.
type FixtureProvider interface {
	Fixture() FSFixture
}

type nodeKind int

const (
	kindDir nodeKind = iota
	kindFile
	kindLink
)

type diskNode struct {
	kind nodeKind
	id   string
	data []byte
}

type injection struct {
	op    string
	nth   int
	err   error
	after bool
}

// FakeDisk is an in-memory disk shared by FakeSafeInitFS and
// FakeIgnoreWriter. It supports scripted failures: FailOn makes the nth call
// of an operation fail before anything happens; FailAfter lets the effect
// happen and then returns the error (a lost reply). Both let crash tests stop
// a flow at an exact step. Safe for concurrent use.
type FakeDisk struct {
	mu     sync.Mutex
	nodes  map[string]*diskNode
	locked map[string]int
	nextID int
	calls  map[string]int
	script []injection
}

// NewFakeDisk returns an empty disk. Create folders with Fixture().Mkdir or
// the Mkdir method.
func NewFakeDisk() *FakeDisk {
	return &FakeDisk{nodes: map[string]*diskNode{}, locked: map[string]int{}, calls: map[string]int{}}
}

// FailOn scripts the nth (1-based) call of op to fail with err before it has
// any effect. Operation names: CreateChildDir, VerifyChildDir, CreateFile,
// FileHash, ReadIgnore, AppendIgnore.
func (d *FakeDisk) FailOn(op string, nth int, err error) {
	d.mu.Lock()
	defer d.mu.Unlock()
	d.script = append(d.script, injection{op: op, nth: nth, err: err})
}

// FailAfter scripts the nth call of op to take effect and then return err.
func (d *FakeDisk) FailAfter(op string, nth int, err error) {
	d.mu.Lock()
	defer d.mu.Unlock()
	d.script = append(d.script, injection{op: op, nth: nth, err: err, after: true})
}

// Calls is how many times op was called.
func (d *FakeDisk) Calls(op string) int {
	d.mu.Lock()
	defer d.mu.Unlock()
	return d.calls[op]
}

// begin counts a call and returns its scripted failure, if any.
func (d *FakeDisk) begin(ctx context.Context, op string) (before, after error) {
	d.calls[op]++
	if err := ctx.Err(); err != nil {
		return fmt.Errorf("the operation was cancelled: %w", err), nil
	}
	for _, in := range d.script {
		if in.op == op && in.nth == d.calls[op] {
			if in.after {
				return nil, in.err
			}
			return in.err, nil
		}
	}
	return nil, nil
}

func (d *FakeDisk) newID() string {
	d.nextID++
	return fmt.Sprintf("fake:%d", d.nextID)
}

func key(p string) string { return filepath.Clean(p) }

// ancestors lists the folders above p, outermost first, without the volume
// root itself.
func ancestors(p string) []string {
	var out []string
	for cur := filepath.Dir(key(p)); ; {
		parent := filepath.Dir(cur)
		if parent == cur {
			break
		}
		out = append([]string{cur}, out...)
		cur = parent
	}
	return out
}

func (d *FakeDisk) mkdirAll(dir string) {
	for _, a := range append(ancestors(dir), key(dir)) {
		if _, ok := d.nodes[a]; !ok {
			d.nodes[a] = &diskNode{kind: kindDir, id: d.newID()}
		}
	}
}

// Mkdir creates dir and its parents as plain folders.
func (d *FakeDisk) Mkdir(dir string) {
	d.mu.Lock()
	defer d.mu.Unlock()
	d.mkdirAll(dir)
}

// Fixture returns the in-memory FSFixture of the disk.
func (d *FakeDisk) Fixture() FSFixture {
	return FSFixture{
		Mkdir: func(_ *testing.T, dir string) { d.Mkdir(dir) },
		WriteFile: func(t *testing.T, path string, data []byte) {
			t.Helper()
			d.mu.Lock()
			defer d.mu.Unlock()
			if n := d.nodes[filepath.Dir(key(path))]; n == nil || n.kind != kindDir {
				t.Fatalf("fake disk: parent of %s is not a folder", path)
			}
			d.nodes[key(path)] = &diskNode{kind: kindFile, id: d.newID(), data: append([]byte(nil), data...)}
		},
		ReadFile: func(_ *testing.T, path string) ([]byte, bool) {
			d.mu.Lock()
			defer d.mu.Unlock()
			n := d.nodes[key(path)]
			if n == nil || n.kind != kindFile {
				return nil, false
			}
			return append([]byte(nil), n.data...), true
		},
		Symlink: func(t *testing.T, link, target string) bool {
			t.Helper()
			d.mu.Lock()
			defer d.mu.Unlock()
			if d.nodes[key(target)] == nil {
				t.Fatalf("fake disk: link target %s does not exist", target)
			}
			d.nodes[key(link)] = &diskNode{kind: kindLink, id: d.newID(), data: []byte(key(target))}
			return true
		},
		Move: func(t *testing.T, from, to string) {
			t.Helper()
			d.mu.Lock()
			defer d.mu.Unlock()
			f, tk := key(from), key(to)
			if d.nodes[f] == nil || d.nodes[tk] != nil {
				t.Fatalf("fake disk: cannot move %s to %s", from, to)
			}
			moved := map[string]*diskNode{}
			for k, n := range d.nodes {
				if k == f || strings.HasPrefix(k, f+string(filepath.Separator)) {
					moved[tk+strings.TrimPrefix(k, f)] = n
					delete(d.nodes, k)
				}
			}
			for k, n := range moved {
				d.nodes[k] = n
			}
		},
		Lock: func(t *testing.T, path string) func() {
			t.Helper()
			d.mu.Lock()
			defer d.mu.Unlock()
			d.locked[key(path)]++
			return func() {
				d.mu.Lock()
				defer d.mu.Unlock()
				d.locked[key(path)]--
			}
		},
	}
}

// openFolder walks parent like the real implementations: every ancestor and
// the folder itself must be a plain folder; a link is ErrUnsafePath.
func (d *FakeDisk) openFolder(parent string) (*diskNode, error) {
	if !filepath.IsAbs(parent) {
		return nil, fmt.Errorf("%q is not a full path: %w", parent, platform.ErrUnsafePath)
	}
	for _, p := range append(ancestors(parent), key(parent)) {
		n := d.nodes[p]
		switch {
		case n == nil:
			return nil, fmt.Errorf("the folder %q does not exist: %w", p, fs.ErrNotExist)
		case n.kind != kindDir:
			return nil, fmt.Errorf("%q is a link or not a folder: %w", p, platform.ErrUnsafePath)
		}
	}
	return d.nodes[key(parent)], nil
}

// FakeSafeInitFS is the in-memory platform.SafeInitFS.
type FakeSafeInitFS struct{ d *FakeDisk }

// NewFakeSafeInitFS returns a SafeInitFS over d.
func NewFakeSafeInitFS(d *FakeDisk) *FakeSafeInitFS { return &FakeSafeInitFS{d: d} }

// Fixture returns the fixture of the underlying disk.
func (f *FakeSafeInitFS) Fixture() FSFixture { return f.d.Fixture() }

// Disk returns the underlying disk.
func (f *FakeSafeInitFS) Disk() *FakeDisk { return f.d }

var _ platform.SafeInitFS = (*FakeSafeInitFS)(nil)

// child opens parent and its leaf and requires identity id.
func (d *FakeDisk) child(parent, leaf string, id platform.DirIdentity) (string, error) {
	if id == "" {
		return "", fmt.Errorf("no folder identity was given: %w", platform.ErrInvalidArgument)
	}
	p, err := d.openFolder(parent)
	if err != nil {
		return "", err
	}
	_ = p
	path := filepath.Join(key(parent), leaf)
	n := d.nodes[path]
	if n == nil || n.kind != kindDir {
		return "", fmt.Errorf("the folder %q is no longer the one Yggdrasil created: %w", leaf, platform.ErrChanged)
	}
	if platform.DirIdentity(n.id) != id {
		return "", fmt.Errorf("the folder %q was replaced: %w", leaf, platform.ErrChanged)
	}
	return path, nil
}

func (f *FakeSafeInitFS) CreateChildDir(ctx context.Context, parent, leaf string) (platform.DirIdentity, error) {
	d := f.d
	d.mu.Lock()
	defer d.mu.Unlock()
	before, after := d.begin(ctx, "CreateChildDir")
	if before != nil {
		return "", before
	}
	if err := platform.ValidateChildName(leaf); err != nil {
		return "", err
	}
	if _, err := d.openFolder(parent); err != nil {
		return "", err
	}
	path := filepath.Join(key(parent), leaf)
	if d.nodes[path] != nil {
		return "", fmt.Errorf("%q already exists: %w", leaf, platform.ErrExists)
	}
	n := &diskNode{kind: kindDir, id: d.newID()}
	d.nodes[path] = n
	if after != nil {
		return "", after
	}
	return platform.DirIdentity(n.id), nil
}

func (f *FakeSafeInitFS) VerifyChildDir(ctx context.Context, parent, leaf string, id platform.DirIdentity) error {
	d := f.d
	d.mu.Lock()
	defer d.mu.Unlock()
	before, after := d.begin(ctx, "VerifyChildDir")
	if before != nil {
		return before
	}
	if err := platform.ValidateChildName(leaf); err != nil {
		return err
	}
	if _, err := d.child(parent, leaf, id); err != nil {
		return err
	}
	return after
}

func (f *FakeSafeInitFS) CreateFile(ctx context.Context, parent, leaf string, id platform.DirIdentity, name string, content []byte) (string, error) {
	d := f.d
	d.mu.Lock()
	defer d.mu.Unlock()
	before, after := d.begin(ctx, "CreateFile")
	if before != nil {
		return "", before
	}
	if err := errors.Join(platform.ValidateChildName(leaf), platform.ValidateChildName(name)); err != nil {
		return "", err
	}
	dir, err := d.child(parent, leaf, id)
	if err != nil {
		return "", err
	}
	path := filepath.Join(dir, name)
	if d.nodes[path] != nil {
		return "", fmt.Errorf("%q already exists and is never overwritten: %w", name, platform.ErrExists)
	}
	d.nodes[path] = &diskNode{kind: kindFile, id: d.newID(), data: append([]byte(nil), content...)}
	if after != nil {
		return "", after
	}
	return platform.HashHex(content), nil
}

func (f *FakeSafeInitFS) FileHash(ctx context.Context, parent, leaf string, id platform.DirIdentity, name string) (string, bool, error) {
	d := f.d
	d.mu.Lock()
	defer d.mu.Unlock()
	before, after := d.begin(ctx, "FileHash")
	if before != nil {
		return "", false, before
	}
	if err := errors.Join(platform.ValidateChildName(leaf), platform.ValidateChildName(name)); err != nil {
		return "", false, err
	}
	dir, err := d.child(parent, leaf, id)
	if err != nil {
		return "", false, err
	}
	n := d.nodes[filepath.Join(dir, name)]
	switch {
	case n == nil:
		return "", false, after
	case n.kind != kindFile:
		return "", false, fmt.Errorf("%q is not a plain file: %w", name, platform.ErrUnsafePath)
	}
	return platform.HashHex(n.data), true, after
}

// FakeIgnoreWriter is the in-memory platform.IgnoreWriter.
type FakeIgnoreWriter struct{ d *FakeDisk }

// NewFakeIgnoreWriter returns an IgnoreWriter over d.
func NewFakeIgnoreWriter(d *FakeDisk) *FakeIgnoreWriter { return &FakeIgnoreWriter{d: d} }

// Fixture returns the fixture of the underlying disk.
func (w *FakeIgnoreWriter) Fixture() FSFixture { return w.d.Fixture() }

// Disk returns the underlying disk.
func (w *FakeIgnoreWriter) Disk() *FakeDisk { return w.d }

var _ platform.IgnoreWriter = (*FakeIgnoreWriter)(nil)

// ignoreNode finds root/.gitignore. The root itself is opened as given (a
// link there is followed, like the real writers); the file must not be a link.
func (d *FakeDisk) ignoreNode(root string) (path string, n *diskNode, err error) {
	if !filepath.IsAbs(root) {
		return "", nil, fmt.Errorf("%q is not a full path: %w", root, platform.ErrUnsafePath)
	}
	r := d.nodes[key(root)]
	if r != nil && r.kind == kindLink {
		root = string(r.data)
		r = d.nodes[key(root)]
	}
	if r == nil || r.kind != kindDir {
		return "", nil, fmt.Errorf("the repo folder %q does not exist: %w", root, fs.ErrNotExist)
	}
	path = filepath.Join(key(root), ".gitignore")
	n = d.nodes[path]
	if n != nil && n.kind != kindFile {
		return "", nil, fmt.Errorf(".gitignore is a link or a folder: %w", platform.ErrUnsafePath)
	}
	return path, n, nil
}

func snapshot(data []byte) platform.IgnoreSnapshot {
	return platform.IgnoreSnapshot{Hash: platform.HashHex(data), Size: int64(len(data))}
}

const maxIgnore = 1 << 20

func (w *FakeIgnoreWriter) ReadIgnore(ctx context.Context, root string) (platform.IgnoreSnapshot, error) {
	d := w.d
	d.mu.Lock()
	defer d.mu.Unlock()
	before, after := d.begin(ctx, "ReadIgnore")
	if before != nil {
		return platform.IgnoreSnapshot{}, before
	}
	_, n, err := d.ignoreNode(root)
	switch {
	case err != nil:
		return platform.IgnoreSnapshot{}, err
	case n == nil:
		return platform.IgnoreSnapshot{Absent: true}, after
	case len(n.data) > maxIgnore:
		return platform.IgnoreSnapshot{}, fmt.Errorf(".gitignore is larger than 1 MiB: %w", platform.ErrTooLarge)
	}
	return snapshot(n.data), after
}

func (w *FakeIgnoreWriter) AppendIgnore(ctx context.Context, root string, entries []string, expect platform.IgnoreExpectation) (platform.IgnoreOutcome, platform.IgnoreSnapshot, error) {
	d := w.d
	d.mu.Lock()
	defer d.mu.Unlock()
	before, after := d.begin(ctx, "AppendIgnore")
	if before != nil {
		return "", platform.IgnoreSnapshot{}, before
	}
	if expect.Absent == (expect.Hash != "") || len(entries) == 0 {
		return "", platform.IgnoreSnapshot{}, fmt.Errorf("unusable expectation or no entries: %w", platform.ErrInvalidArgument)
	}
	path, n, err := d.ignoreNode(root)
	if err != nil {
		return "", platform.IgnoreSnapshot{}, err
	}
	if d.locked[path] > 0 {
		return "", platform.IgnoreSnapshot{}, fmt.Errorf(".gitignore is open in another program: %w", platform.ErrLocked)
	}
	if n == nil {
		if !expect.Absent {
			return "", platform.IgnoreSnapshot{}, fmt.Errorf(".gitignore is gone: %w", platform.ErrChanged)
		}
		content, err := platform.PlanIgnoreAppend(nil, entries)
		if err != nil {
			return "", platform.IgnoreSnapshot{}, err
		}
		d.nodes[path] = &diskNode{kind: kindFile, id: d.newID(), data: content}
		return platform.IgnoreCreated, snapshot(content), after
	}
	switch {
	case expect.Absent:
		return "", platform.IgnoreSnapshot{}, fmt.Errorf(".gitignore now exists: %w", platform.ErrChanged)
	case len(n.data) > maxIgnore:
		return "", platform.IgnoreSnapshot{}, fmt.Errorf(".gitignore is larger than 1 MiB: %w", platform.ErrTooLarge)
	case platform.HashHex(n.data) != expect.Hash:
		return "", platform.IgnoreSnapshot{}, fmt.Errorf(".gitignore changed: %w", platform.ErrChanged)
	}
	add, err := platform.PlanIgnoreAppend(n.data, entries)
	if err != nil {
		return "", platform.IgnoreSnapshot{}, err
	}
	if add == nil {
		return platform.IgnoreAlreadyPresent, snapshot(n.data), after
	}
	n.data = append(append([]byte(nil), n.data...), add...)
	return platform.IgnoreAppended, snapshot(n.data), after
}
