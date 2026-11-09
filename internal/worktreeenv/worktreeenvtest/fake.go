// Package worktreeenvtest has fakes for the capabilities of internal/worktreeenv
// and the contract suites that run against both a fake and the real
// implementation (docs/AGENTS.md 5.11).
package worktreeenvtest

import (
	"bytes"
	"context"
	"path"
	"sort"
	"strings"
	"sync"
	"time"

	"github.com/brendatamaar/yggdrasil/internal/core"
	env "github.com/brendatamaar/yggdrasil/internal/core/worktreeenv"
	"github.com/brendatamaar/yggdrasil/internal/worktreeenv"
)

// Store is an in-memory PortStore, SettingsStore and AuditSink.
type Store struct {
	mu       sync.Mutex
	blocks   map[core.WorktreeID]core.PortBlock
	released []core.WorktreeID
	envs     map[core.RepoID]worktreeenv.RepoEnv
	repos    map[core.RepoID]bool
	composes map[core.RepoID]worktreeenv.ComposeSettings
	rng      *env.Range
	audits   []core.AuditEntry
}

// NewStore returns an empty store that knows the given repos.
func NewStore(repos ...core.RepoID) *Store {
	s := &Store{
		blocks: map[core.WorktreeID]core.PortBlock{}, envs: map[core.RepoID]worktreeenv.RepoEnv{},
		repos: map[core.RepoID]bool{},
	}
	for _, r := range repos {
		s.repos[r] = true
	}
	return s
}

// Audits returns the audit entries written so far.
func (s *Store) Audits() []core.AuditEntry {
	s.mu.Lock()
	defer s.mu.Unlock()
	return append([]core.AuditEntry(nil), s.audits...)
}

// SeedBlock puts a live block into the store without the checks of SaveBlock,
// as old rows written under an earlier range would be.
func (s *Store) SeedBlock(w core.WorktreeID, b core.PortBlock) {
	s.mu.Lock()
	defer s.mu.Unlock()
	s.blocks[w] = b
}

// LoadBlocks implements worktreeenv.PortStore.
func (s *Store) LoadBlocks(context.Context) (map[core.WorktreeID]core.PortBlock, error) {
	s.mu.Lock()
	defer s.mu.Unlock()
	out := make(map[core.WorktreeID]core.PortBlock, len(s.blocks))
	for k, v := range s.blocks {
		out[k] = v
	}
	return out, nil
}

// SaveBlock implements worktreeenv.PortStore.
func (s *Store) SaveBlock(_ context.Context, w core.WorktreeID, b core.PortBlock, _ time.Time, audit core.AuditEntry) error {
	s.mu.Lock()
	defer s.mu.Unlock()
	if _, ok := s.blocks[w]; ok {
		return worktreeenv.ErrConflict
	}
	for _, o := range s.blocks {
		if o.Base == b.Base {
			return worktreeenv.ErrConflict
		}
	}
	s.blocks[w] = b
	s.audits = append(s.audits, audit)
	return nil
}

// ReleaseBlock implements worktreeenv.PortStore.
func (s *Store) ReleaseBlock(_ context.Context, w core.WorktreeID, _ time.Time, audit core.AuditEntry) (bool, error) {
	s.mu.Lock()
	defer s.mu.Unlock()
	if _, ok := s.blocks[w]; !ok {
		return false, nil
	}
	delete(s.blocks, w)
	s.released = append(s.released, w)
	s.audits = append(s.audits, audit)
	return true, nil
}

// RepoEnv implements worktreeenv.SettingsStore.
func (s *Store) RepoEnv(_ context.Context, repo core.RepoID) (worktreeenv.RepoEnv, error) {
	s.mu.Lock()
	defer s.mu.Unlock()
	if !s.repos[repo] {
		return worktreeenv.RepoEnv{}, worktreeenv.ErrNotFound
	}
	return s.envs[repo], nil
}

// PutRepoEnv implements worktreeenv.SettingsStore.
func (s *Store) PutRepoEnv(_ context.Context, repo core.RepoID, e worktreeenv.RepoEnv, _ time.Time, audit core.AuditEntry) error {
	s.mu.Lock()
	defer s.mu.Unlock()
	if !s.repos[repo] {
		return worktreeenv.ErrNotFound
	}
	s.envs[repo] = e
	s.audits = append(s.audits, audit)
	return nil
}

// PortRange implements worktreeenv.SettingsStore.
func (s *Store) PortRange(context.Context) (env.Range, error) {
	s.mu.Lock()
	defer s.mu.Unlock()
	if s.rng == nil {
		return env.DefaultRange, nil
	}
	return *s.rng, nil
}

// SetPortRange implements worktreeenv.SettingsStore.
func (s *Store) SetPortRange(_ context.Context, r env.Range, audit core.AuditEntry) error {
	s.mu.Lock()
	defer s.mu.Unlock()
	s.rng = &r
	s.audits = append(s.audits, audit)
	return nil
}

// Append implements worktreeenv.AuditSink.
func (s *Store) Append(_ context.Context, e core.AuditEntry) error {
	s.mu.Lock()
	defer s.mu.Unlock()
	s.audits = append(s.audits, e)
	return nil
}

// Prober is a PortProber with a fixed list of busy ports.
type Prober struct {
	mu   sync.Mutex
	Used map[int]bool
	// Fail makes Busy return an error.
	Fail error
	// Calls counts Busy calls.
	Calls int
}

// NewProber returns a prober where the given ports are busy.
func NewProber(busy ...int) *Prober {
	p := &Prober{Used: map[int]bool{}}
	for _, b := range busy {
		p.Used[b] = true
	}
	return p
}

// Busy implements worktreeenv.PortProber.
func (p *Prober) Busy(_ context.Context, lo, hi int) ([]int, error) {
	p.mu.Lock()
	defer p.mu.Unlock()
	p.Calls++
	if p.Fail != nil {
		return nil, p.Fail
	}
	var out []int
	for port := range p.Used {
		if port >= lo && port <= hi {
			out = append(out, port)
		}
	}
	sort.Ints(out)
	return out, nil
}

type kind int

const (
	kFile kind = iota + 1
	kDir
	kLink
	kOther // a file where a folder should be
)

type node struct {
	kind kind
	data []byte
}

// FS is an in-memory FileSystem. Paths are slash-separated; roots are any
// absolute-looking string.
type FS struct {
	mu    sync.Mutex
	nodes map[string]node
}

// NewFS returns an empty file system.
func NewFS() *FS { return &FS{nodes: map[string]node{}} }

func key(root string, parts ...string) string { return path.Join(append([]string{root}, parts...)...) }

// WriteFile creates a file with its parent folders.
func (f *FS) WriteFile(root, rel string, data []byte) {
	f.mu.Lock()
	defer f.mu.Unlock()
	f.mkdirs(root, rel)
	f.nodes[key(root, rel)] = node{kind: kFile, data: append([]byte(nil), data...)}
}

// Link creates a link entry (its target does not matter to the fake).
func (f *FS) Link(root, rel string) {
	f.mu.Lock()
	defer f.mu.Unlock()
	f.mkdirs(root, rel)
	f.nodes[key(root, rel)] = node{kind: kLink}
}

// Mkdir creates a folder.
func (f *FS) Mkdir(root, rel string) {
	f.mu.Lock()
	defer f.mu.Unlock()
	f.mkdirs(root, rel+"/x")
}

func (f *FS) mkdirs(root, rel string) {
	parts := strings.Split(rel, "/")
	for i := 1; i < len(parts); i++ {
		k := key(root, parts[:i]...)
		if _, ok := f.nodes[k]; !ok {
			f.nodes[k] = node{kind: kDir}
		}
	}
}

// Read returns a file's bytes.
func (f *FS) Read(root, rel string) ([]byte, bool) {
	f.mu.Lock()
	defer f.mu.Unlock()
	n, ok := f.nodes[key(root, rel)]
	return n.data, ok && n.kind == kFile
}

// Count returns the number of entries; a test uses it to see that nothing
// disappeared.
func (f *FS) Count() int {
	f.mu.Lock()
	defer f.mu.Unlock()
	return len(f.nodes)
}

func (f *FS) walk(root string, rel env.RelPath) (kind, node, bool) {
	parts := strings.Split(string(rel), "/")
	for i := range parts {
		n, ok := f.nodes[key(root, parts[:i+1]...)]
		if !ok {
			return 0, n, false
		}
		if i == len(parts)-1 {
			return n.kind, n, true
		}
		if n.kind == kLink {
			return kLink, n, true
		}
		if n.kind != kDir {
			return kOther, n, true
		}
	}
	return 0, node{}, false
}

// Observe implements worktreeenv.FileSystem.
func (f *FS) Observe(_ context.Context, srcRoot, dstRoot string, rel env.RelPath) (env.Source, env.Dest, error) {
	if rel.Validate() != nil {
		return env.SourceUnreadable{}, env.DestExists, nil
	}
	f.mu.Lock()
	defer f.mu.Unlock()
	var src env.Source = env.SourceMissing{}
	if k, n, ok := f.walk(srcRoot, rel); ok {
		switch k {
		case kFile:
			src = env.SourceFile{Size: int64(len(n.data))}
		case kDir, kOther:
			src = env.SourceDirectory{}
		case kLink:
			src = env.SourceLink{}
		}
	}
	dest := env.DestExists
	if _, _, ok := f.walk(dstRoot, rel); !ok {
		dest = env.DestAbsent
	}
	return src, dest, nil
}

// CopyExclusive implements worktreeenv.FileSystem.
func (f *FS) CopyExclusive(_ context.Context, srcRoot, dstRoot string, rel env.RelPath, maxBytes int64) error {
	if rel.Validate() != nil {
		return worktreeenv.ErrNotRegular
	}
	f.mu.Lock()
	defer f.mu.Unlock()
	k, n, ok := f.walk(srcRoot, rel)
	if !ok || k != kFile {
		return worktreeenv.ErrNotRegular
	}
	if int64(len(n.data)) > maxBytes {
		return worktreeenv.ErrTooLarge
	}
	parts := strings.Split(string(rel), "/")
	for i := 1; i < len(parts); i++ {
		d, ok := f.nodes[key(dstRoot, parts[:i]...)]
		switch {
		case !ok:
			f.nodes[key(dstRoot, parts[:i]...)] = node{kind: kDir}
		case d.kind != kDir:
			return worktreeenv.ErrNotRegular
		}
	}
	if _, exists := f.nodes[key(dstRoot, string(rel))]; exists {
		return worktreeenv.ErrDestExists
	}
	f.nodes[key(dstRoot, string(rel))] = node{kind: kFile, data: bytes.Clone(n.data)}
	return nil
}
