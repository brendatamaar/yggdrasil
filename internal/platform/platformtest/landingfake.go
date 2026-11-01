package platformtest

import (
	"errors"
	"path/filepath"
	"sort"
	"strings"
	"sync"
	"testing"

	"github.com/brendatamaar/yggdrasil/internal/platform"
)

// FakeLanding is a platform.Landing over an in-memory tree, with scripted
// locks and holders for executor tests. It applies the same path rules as
// RemoveStaged for the text of a path (not links or 8.3 names). Safe for
// concurrent use.
type FakeLanding struct {
	mu      sync.Mutex
	entries map[string]fakeKind
	links   map[string]string
	locks   map[string]platform.LockClass
	holders map[string][]string
	// MoveErr, if set, is returned (as a *platform.MoveError with its class)
	// by the next MoveDir calls until cleared; nothing moves.
	MoveErr *platform.MoveError
	// Calls records "move from -> to" and "remove staged" entries in order.
	Calls []string
}

type fakeKind int

const (
	fakeDir fakeKind = iota + 1
	fakeFile
	fakeLink
)

// NewFakeLanding returns an empty fake.
func NewFakeLanding() *FakeLanding {
	return &FakeLanding{
		entries: map[string]fakeKind{},
		links:   map[string]string{},
		locks:   map[string]platform.LockClass{},
		holders: map[string][]string{},
	}
}

// SetHolders scripts what LockHolders returns for path.
func (f *FakeLanding) SetHolders(path string, names ...string) {
	f.mu.Lock()
	defer f.mu.Unlock()
	f.holders[filepath.Clean(path)] = names
}

// SetLock makes every move or delete touching dir fail with class until
// release is called.
func (f *FakeLanding) SetLock(dir string, class platform.LockClass) (release func()) {
	f.mu.Lock()
	defer f.mu.Unlock()
	d := filepath.Clean(dir)
	f.locks[d] = class
	return func() {
		f.mu.Lock()
		defer f.mu.Unlock()
		delete(f.locks, d)
	}
}

// Fixture returns the contract fixture of this fake, rooted at root.
func (f *FakeLanding) Fixture(root string) LandingFixture {
	root = filepath.Clean(root)
	fx := LandingFixture{Root: root}
	fx.Mkdir = func(_ *testing.T, dir string) { f.put(dir, fakeDir) }
	fx.MakeTree = func(_ *testing.T, dir string) {
		f.put(dir, fakeDir)
		f.put(filepath.Join(dir, "a.txt"), fakeFile)
		f.put(filepath.Join(dir, "sub", "b.txt"), fakeFile)
		f.put(filepath.Join(dir, "ro.txt"), fakeFile)
		f.put(filepath.Join(dir, "empty"), fakeDir)
	}
	fx.MakeLongTree = func(t *testing.T, dir string) {
		fx.MakeTree(t, dir)
		f.put(filepath.Join(dir, strings.Repeat("d", 100), strings.Repeat("e", 100), strings.Repeat("f", 100), "x.txt"), fakeFile)
	}
	fx.Exists = func(p string) bool {
		f.mu.Lock()
		defer f.mu.Unlock()
		_, ok := f.entries[filepath.Clean(p)]
		return ok
	}
	fx.Lock = func(_ *testing.T, dir string) func() { return f.SetLock(dir, platform.LockSharing) }
	fx.Link = func(_ *testing.T, link, target string) bool {
		f.mu.Lock()
		defer f.mu.Unlock()
		f.putLocked(filepath.Dir(link), fakeDir)
		f.entries[filepath.Clean(link)] = fakeLink
		f.links[filepath.Clean(link)] = filepath.Clean(target)
		return true
	}
	return fx
}

func (f *FakeLanding) put(p string, k fakeKind) {
	f.mu.Lock()
	defer f.mu.Unlock()
	f.putLocked(p, k)
}

func (f *FakeLanding) putLocked(p string, k fakeKind) {
	p = filepath.Clean(p)
	f.entries[p] = k
	for d := filepath.Dir(p); filepath.Dir(d) != d; d = filepath.Dir(d) {
		if _, ok := f.entries[d]; ok {
			break
		}
		f.entries[d] = fakeDir
	}
}

func under(p, dir string) bool {
	return p == dir || strings.HasPrefix(p, dir+string(filepath.Separator))
}

// lockedLocked returns the class of a lock on or below dir, or "".
func (f *FakeLanding) lockedLocked(dir string) platform.LockClass {
	for d, c := range f.locks {
		if under(d, dir) || under(dir, d) {
			return c
		}
	}
	return ""
}

// MoveDir implements platform.Landing.
func (f *FakeLanding) MoveDir(from, to string) error {
	f.mu.Lock()
	defer f.mu.Unlock()
	from, to = filepath.Clean(from), filepath.Clean(to)
	f.Calls = append(f.Calls, "move "+from+" -> "+to)
	if f.MoveErr != nil {
		return f.MoveErr
	}
	if !filepath.IsAbs(from) || !filepath.IsAbs(to) || under(to, from) {
		return &platform.MoveError{Class: platform.LockOther, Err: platform.ErrMoveSame}
	}
	if _, ok := f.entries[to]; ok {
		return &platform.MoveError{Class: platform.LockOther, Err: platform.ErrMoveTargetExists}
	}
	if k, ok := f.entries[from]; !ok || k != fakeDir {
		return &platform.MoveError{Class: platform.LockOther, Err: errors.New("the folder does not exist")}
	}
	if c := f.lockedLocked(from); c != "" {
		return &platform.MoveError{Class: c, Err: errors.New("the folder is in use")}
	}
	var moved []string
	for p := range f.entries {
		if under(p, from) {
			moved = append(moved, p)
		}
	}
	sort.Strings(moved)
	for _, p := range moved {
		np := to + strings.TrimPrefix(p, from)
		f.entries[np] = f.entries[p]
		if t, ok := f.links[p]; ok {
			f.links[np] = t
			delete(f.links, p)
		}
		delete(f.entries, p)
	}
	f.putLocked(to, fakeDir)
	return nil
}

// RemoveStaged implements platform.Landing.
func (f *FakeLanding) RemoveStaged(root, staged string) (platform.RemoveReport, error) {
	f.mu.Lock()
	defer f.mu.Unlock()
	f.Calls = append(f.Calls, "remove "+staged)
	rep := platform.RemoveReport{Class: platform.LockOther}
	if !fakeInside(root, staged) {
		return rep, platform.ErrStagedRefused
	}
	s := filepath.Clean(staged)
	if f.entries[s] == fakeLink {
		return rep, platform.ErrStagedRefused
	}
	for l := range f.links {
		if under(s, l) {
			return rep, platform.ErrStagedRefused
		}
	}
	if c := f.lockedLocked(s); c != "" {
		rep.Remaining, rep.Class = 1, c
		rep.FirstErr = errors.New("a file is in use")
		return rep, rep.FirstErr
	}
	for p := range f.entries {
		if under(p, s) {
			delete(f.entries, p)
			delete(f.links, p)
		}
	}
	return rep, nil
}

// fakeInside is the textual rule of RemoveStaged.
func fakeInside(root, staged string) bool {
	if !filepath.IsAbs(root) || !filepath.IsAbs(staged) {
		return false
	}
	for _, seg := range strings.FieldsFunc(staged, func(r rune) bool { return r == '/' || r == '\\' }) {
		if seg == ".." {
			return false
		}
	}
	pre := filepath.Join(filepath.Clean(root), platform.StagingDir) + string(filepath.Separator)
	s := filepath.Clean(staged)
	return len(s) > len(pre) && strings.EqualFold(s[:len(pre)], pre)
}

// ClassifyLock implements platform.Landing; the fake's errors carry no
// errno, so only the *MoveError class and nil are known.
func (f *FakeLanding) ClassifyLock(err error) platform.LockClass {
	var me *platform.MoveError
	if errors.As(err, &me) {
		return me.Class
	}
	return platform.LockOther
}

// LockHolders implements platform.Landing.
func (f *FakeLanding) LockHolders(path string) []string {
	f.mu.Lock()
	defer f.mu.Unlock()
	return append([]string(nil), f.holders[filepath.Clean(path)]...)
}
