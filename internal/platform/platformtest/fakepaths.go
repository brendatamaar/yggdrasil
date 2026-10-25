package platformtest

import (
	"path/filepath"
	"strings"
	"sync"

	"github.com/brendatamaar/yggdrasil/internal/platform"
)

// FakePaths is a platform.PathResolver without a disk: a map from existing
// paths to their canonical form, plus scripted errors. It applies the same
// lexical rules as the real one (platform.CleanRootPath). Safe for concurrent
// use.
type FakePaths struct {
	// Fold is the FoldCase answer.
	Fold bool

	mu    sync.Mutex
	known map[string]string // key(path) -> canonical
	errs  map[string]error  // key(path) -> error for ResolveRoot
}

var _ platform.PathResolver = (*FakePaths)(nil)

// NewFakePaths returns an empty fake with the given case folding.
func NewFakePaths(fold bool) *FakePaths {
	return &FakePaths{Fold: fold, known: map[string]string{}, errs: map[string]error{}}
}

func (f *FakePaths) key(p string) string {
	if f.Fold {
		return strings.ToLower(p)
	}
	return p
}

// Add says that the existing path (and everything under it) resolves to
// canon. canon exists too and resolves to itself unless set otherwise.
func (f *FakePaths) Add(path, canon string) *FakePaths {
	f.mu.Lock()
	defer f.mu.Unlock()
	path, canon = filepath.Clean(path), filepath.Clean(canon)
	f.known[f.key(path)] = canon
	if _, ok := f.known[f.key(canon)]; !ok {
		f.known[f.key(canon)] = canon
	}
	return f
}

// Fail makes ResolveRoot(path) return err.
func (f *FakePaths) Fail(path string, err error) *FakePaths {
	f.mu.Lock()
	defer f.mu.Unlock()
	f.errs[f.key(filepath.Clean(path))] = err
	return f
}

// FoldCase implements platform.PathResolver.
func (f *FakePaths) FoldCase() bool { return f.Fold }

// ResolveRoot implements platform.PathResolver.
func (f *FakePaths) ResolveRoot(path string) (string, error) {
	p, err := platform.CleanRootPath(path)
	if err != nil {
		return "", err
	}
	f.mu.Lock()
	defer f.mu.Unlock()
	if err, ok := f.errs[f.key(p)]; ok {
		return "", err
	}
	var rest []string
	cur := p
	for {
		if canon, ok := f.known[f.key(cur)]; ok {
			out := canon
			for i := len(rest) - 1; i >= 0; i-- {
				out = filepath.Join(out, rest[i])
			}
			return out, nil
		}
		parent := filepath.Dir(cur)
		if parent == cur {
			return "", platform.ErrPathUnreadable
		}
		rest = append(rest, filepath.Base(cur))
		cur = parent
	}
}
