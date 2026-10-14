package main

import (
	"bytes"
	"errors"
	"fmt"
	"io/fs"
	"maps"
	"os"
	"path/filepath"
	"runtime"
	"slices"
	"sort"
	"strings"
	"time"

	"github.com/go-git/go-billy/v5"
	"github.com/go-git/go-billy/v5/osfs"
	"github.com/go-git/go-git/v5"
	"github.com/go-git/go-git/v5/config"
	"github.com/go-git/go-git/v5/plumbing"
	"github.com/go-git/go-git/v5/plumbing/cache"
	"github.com/go-git/go-git/v5/plumbing/filemode"
	"github.com/go-git/go-git/v5/plumbing/format/packfile"
	"github.com/go-git/go-git/v5/plumbing/object"
	"github.com/go-git/go-git/v5/plumbing/storer"
	"github.com/go-git/go-git/v5/storage/filesystem"
	"github.com/go-git/go-git/v5/storage/memory"
)

// outName is the only folder name the generator writes into.
const outName = "demo-project"

const projectReadme = `# acme-shop (demo data)

acme-shop is a fictional project used by Yggdrasil for first-run demos, tests
and screenshots. Every repo, person, email address and commit in it is made up.

- shop-api: Java / Maven backend
- shop-web: TypeScript storefront (pnpm)
- shop-admin: JavaScript admin dashboard

The origin of each repo is a local bare repo under ../remotes. Nothing here
talks to a real git host.

Regenerate with ` + "`task fixtures`" + ` after removing the demo-project folder.
`

type author struct {
	name, email string
	zone        *time.Location
}

var (
	ada  = author{"Ada Lindqvist", "ada.lindqvist@example.com", time.FixedZone("", 1*60*60)}
	bram = author{"Bram Okafor", "bram.okafor@example.com", time.FixedZone("", 1*60*60)}
	chen = author{"Chen Wei", "chen.wei@example.com", time.FixedZone("", 8*60*60)}
	dana = author{"Dana Ruiz", "dana.ruiz@example.com", time.FixedZone("", -5*60*60)}
)

type repoSpec struct {
	name  string
	start time.Time
	build func(b *builder)
}

var repos = []repoSpec{
	{"shop-api", time.Date(2026, 3, 2, 8, 0, 0, 0, time.UTC), buildShopAPI},
	{"shop-web", time.Date(2026, 3, 9, 8, 0, 0, 0, time.UTC), buildShopWeb},
	{"shop-admin", time.Date(2026, 3, 16, 8, 0, 0, 0, time.UTC), buildShopAdmin},
}

// generate writes the demo project into out and returns its absolute path.
func generate(out string) (string, []repoSummary, error) {
	root, err := prepareOut(out)
	if err != nil {
		return "", nil, err
	}
	project := filepath.Join(root, "acme-shop")
	if err := os.MkdirAll(project, 0o755); err != nil {
		return "", nil, fmt.Errorf("create %s: %w", project, err)
	}
	if err := os.WriteFile(filepath.Join(project, "README.md"), []byte(projectReadme), 0o644); err != nil {
		return "", nil, fmt.Errorf("write project README: %w", err)
	}

	sums := make([]repoSummary, 0, len(repos))
	for _, spec := range repos {
		s, err := buildRepo(spec, filepath.Join(project, spec.name), filepath.Join(root, "remotes", spec.name+".git"))
		if err != nil {
			return "", nil, err
		}
		sums = append(sums, s)
	}
	return root, sums, nil
}

func buildRepo(spec repoSpec, dir, remoteDir string) (repoSummary, error) {
	b, err := newBuilder(dir, remoteDir, spec.start)
	if err != nil {
		return repoSummary{}, fmt.Errorf("create %s: %w", spec.name, err)
	}
	spec.build(b)
	b.flush()
	var s repoSummary
	if b.err == nil {
		s, err = summarize(spec.name, b.local)
		b.fail(err, "summarize")
		s.notes = b.notes
	}
	b.close()
	if b.err != nil {
		return repoSummary{}, fmt.Errorf("build %s: %w", spec.name, b.err)
	}
	return s, nil
}

// prepareOut checks that out is named demo-project and is empty or missing,
// then creates it. It never removes anything.
func prepareOut(out string) (string, error) {
	root, err := filepath.Abs(out)
	if err != nil {
		return "", fmt.Errorf("resolve %s: %w", out, err)
	}
	if filepath.Base(root) != outName {
		return "", fmt.Errorf("refusing to write to %s: the output folder must be named %s", root, outName)
	}
	entries, err := os.ReadDir(root)
	switch {
	case errors.Is(err, fs.ErrNotExist):
	case err != nil:
		return "", fmt.Errorf("read %s: %w", root, err)
	case len(entries) > 0:
		return "", fmt.Errorf("%s is not empty: remove it yourself, then run again (the generator never deletes or overwrites files)", root)
	}
	if err := os.MkdirAll(root, 0o755); err != nil {
		return "", fmt.Errorf("create %s: %w", root, err)
	}
	return root, nil
}

type file struct{ path, content string }

func f(path, content string) file { return file{path, content} }

// executable lists paths stored with mode 100755.
var executable = map[string]bool{"mvnw": true}

// builder writes blobs, trees and commits straight into object stores, so
// hashes depend only on the script below, not on the file system or clock.
// Objects, refs and upstreams stay in memory until flush writes them once:
// a file write per object or ref is very slow on Windows.
// The first error sticks and turns later steps into no-ops.
type builder struct {
	local, remote           *git.Repository
	localStore, remoteStore *filesystem.Storage
	localObjs, remoteObjs   *memory.Storage
	localRefs, remoteRefs   map[plumbing.ReferenceName]plumbing.Hash
	upstreams               []string
	flushed                 bool
	dir                     string
	trees                   map[plumbing.Hash]map[string]string
	clock                   time.Time
	step                    int
	notes                   []string
	err                     error
}

func newBuilder(dir, remoteDir string, start time.Time) (*builder, error) {
	b := &builder{
		localObjs:  memory.NewStorage(),
		remoteObjs: memory.NewStorage(),
		localRefs:  map[plumbing.ReferenceName]plumbing.Hash{},
		remoteRefs: map[plumbing.ReferenceName]plumbing.Hash{},
		dir:        dir,
		trees:      map[plumbing.Hash]map[string]string{},
		clock:      start,
	}
	var err error
	if b.remote, b.remoteStore, err = initRepo(remoteDir, true, nil); err != nil {
		return nil, err
	}
	// A local path with forward slashes is a valid remote URL for git on every OS.
	origin := &config.RemoteConfig{Name: "origin", URLs: []string{filepath.ToSlash(remoteDir)}}
	if b.local, b.localStore, err = initRepo(dir, false, origin); err != nil {
		b.close()
		return nil, err
	}
	return b, nil
}

// initRepo is git.PlainInit with the default branch main, plus storage
// options that keep packfiles open: reopening them for every object read is
// slow on Windows. The caller closes the storage.
func initRepo(dir string, bare bool, origin *config.RemoteConfig) (*git.Repository, *filesystem.Storage, error) {
	var wt, dot billy.Filesystem
	if bare {
		dot = osfs.New(dir)
	} else {
		wt = osfs.New(dir)
		var err error
		if dot, err = wt.Chroot(git.GitDirName); err != nil {
			return nil, nil, fmt.Errorf("init %s: %w", dir, err)
		}
	}
	st := filesystem.NewStorageWithOptions(dot, cache.NewObjectLRUDefault(), filesystem.Options{ExclusiveAccess: true, KeepDescriptors: true})
	r, err := git.InitWithOptions(st, wt, git.InitOptions{DefaultBranch: plumbing.Main})
	if err != nil {
		st.Close()
		return nil, nil, fmt.Errorf("init %s: %w", dir, err)
	}
	cfg, err := r.Config()
	if err != nil {
		st.Close()
		return nil, nil, fmt.Errorf("read config of %s: %w", dir, err)
	}
	if origin != nil {
		cfg.Remotes[origin.Name] = origin
	}
	// Git for Windows writes this on init: the file system has no executable
	// bit, so without it git status would report mvnw as changed (100755 -> 100644).
	if runtime.GOOS == "windows" {
		cfg.Raw.Section("core").SetOption("filemode", "false")
	}
	if err := r.Storer.SetConfig(cfg); err != nil {
		st.Close()
		return nil, nil, fmt.Errorf("write config of %s: %w", dir, err)
	}
	return r, st, nil
}

// close releases the open packfiles; Windows can't remove the folders otherwise.
func (b *builder) close() {
	for _, st := range []**filesystem.Storage{&b.localStore, &b.remoteStore} {
		if *st != nil {
			b.fail((*st).Close(), "close storage")
			*st = nil
		}
	}
}

// flush writes the objects of both repos as one packfile each, then their
// refs and upstream config. Later writes would be lost, so it runs once,
// before checkout and summary.
func (b *builder) flush() {
	if b.err != nil || b.flushed {
		return
	}
	b.flushed = true
	b.fail(writePack(b.localObjs, b.local.Storer), "write pack of %s", b.dir)
	b.fail(writePack(b.remoteObjs, b.remote.Storer), "write pack of origin")
	b.fail(writeRefs(b.localRefs, b.local.Storer), "write refs of %s", b.dir)
	b.fail(writeRefs(b.remoteRefs, b.remote.Storer), "write refs of origin")
	if _, ok := b.localRefs[plumbing.NewRemoteReferenceName("origin", "main")]; ok {
		head := plumbing.NewSymbolicReference(plumbing.NewRemoteHEADReferenceName("origin"), plumbing.NewRemoteReferenceName("origin", "main"))
		b.fail(b.local.Storer.SetReference(head), "set origin/HEAD")
	}
	if len(b.upstreams) == 0 || b.err != nil {
		return
	}
	cfg, err := b.local.Config()
	if err != nil {
		b.fail(err, "read config")
		return
	}
	for _, br := range b.upstreams {
		cfg.Branches[br] = &config.Branch{Name: br, Remote: "origin", Merge: plumbing.NewBranchReferenceName(br)}
	}
	b.fail(b.local.Storer.SetConfig(cfg), "set upstreams")
}

func writeRefs(refs map[plumbing.ReferenceName]plumbing.Hash, s storer.ReferenceStorer) error {
	for _, name := range slices.Sorted(maps.Keys(refs)) {
		if err := s.SetReference(plumbing.NewHashReference(name, refs[name])); err != nil {
			return err
		}
	}
	return nil
}

// ref looks up a ref the script set earlier.
func ref(refs map[plumbing.ReferenceName]plumbing.Hash, name plumbing.ReferenceName) (plumbing.Hash, error) {
	h, ok := refs[name]
	if !ok {
		return plumbing.ZeroHash, fmt.Errorf("resolve %s: %w", name, plumbing.ErrReferenceNotFound)
	}
	return h, nil
}

func writePack(src *memory.Storage, dst storer.Storer) error {
	if len(src.Objects) == 0 {
		return nil
	}
	pw, ok := dst.(storer.PackfileWriter)
	if !ok {
		return errors.New("storage cannot write packfiles")
	}
	hashes := slices.SortedFunc(maps.Keys(src.Objects), func(a, b plumbing.Hash) int { return bytes.Compare(a[:], b[:]) })
	w, err := pw.PackfileWriter()
	if err != nil {
		return err
	}
	if _, err := packfile.NewEncoder(w, src, false).Encode(hashes, 0); err != nil {
		w.Close()
		return err
	}
	return w.Close()
}

func (b *builder) fail(err error, format string, args ...any) {
	if err != nil && b.err == nil {
		b.err = fmt.Errorf(format+": %w", append(args, err)...)
	}
}

// commit adds a commit on top of a local branch. The first commit starts main.
func (b *builder) commit(branch string, a author, msg string, files ...file) {
	if b.err != nil {
		return
	}
	name := plumbing.NewBranchReferenceName(branch)
	var parent plumbing.Hash
	if len(b.trees) > 0 {
		var err error
		if parent, err = ref(b.localRefs, name); err != nil {
			b.fail(err, "commit on %s", branch)
			return
		}
	}
	h, err := b.writeCommit(b.localObjs, parent, a, msg, files)
	if err != nil {
		b.fail(err, "commit %q", msg)
		return
	}
	b.localRefs[name] = h
}

// remoteCommit adds a commit a teammate pushed to origin, then fetches it:
// origin/<branch> moves on, the local branch stays where it was.
func (b *builder) remoteCommit(branch string, a author, msg string, files ...file) {
	if b.err != nil {
		return
	}
	name := plumbing.NewBranchReferenceName(branch)
	parent, err := ref(b.remoteRefs, name)
	if err != nil {
		b.fail(err, "remote commit on %s", branch)
		return
	}
	h, err := b.writeCommit(b.remoteObjs, parent, a, msg, files)
	if err != nil {
		b.fail(err, "remote commit %q", msg)
		return
	}
	b.remoteRefs[name] = h
	b.fail(copyObjects(b.remoteObjs, b.localObjs, h), "fetch %s", branch)
	b.localRefs[plumbing.NewRemoteReferenceName("origin", branch)] = h
}

// branch creates a local branch at the head of another one.
func (b *builder) branch(name, from string) {
	if b.err != nil {
		return
	}
	h, err := ref(b.localRefs, plumbing.NewBranchReferenceName(from))
	if err != nil {
		b.fail(err, "branch %s from %s", name, from)
		return
	}
	b.localRefs[plumbing.NewBranchReferenceName(name)] = h
}

// push copies a local branch to origin and sets origin/<branch> as its upstream.
func (b *builder) push(branch string) {
	if b.err != nil {
		return
	}
	h, err := ref(b.localRefs, plumbing.NewBranchReferenceName(branch))
	if err != nil {
		b.fail(err, "push %s", branch)
		return
	}
	b.fail(copyObjects(b.localObjs, b.remoteObjs, h), "push %s", branch)
	b.remoteRefs[plumbing.NewBranchReferenceName(branch)] = h
	b.localRefs[plumbing.NewRemoteReferenceName("origin", branch)] = h
	if !slices.Contains(b.upstreams, branch) {
		b.upstreams = append(b.upstreams, branch)
	}
}

// tag creates a lightweight tag, or an annotated one when msg isn't empty.
func (b *builder) tag(name, branch string, a author, msg string) {
	if b.err != nil {
		return
	}
	target, err := ref(b.localRefs, plumbing.NewBranchReferenceName(branch))
	if err != nil {
		b.fail(err, "tag %s", name)
		return
	}
	h := target
	if msg != "" {
		t := &object.Tag{
			Name:       name,
			Tagger:     b.signature(a),
			Message:    msg + "\n",
			TargetType: plumbing.CommitObject,
			Target:     target,
		}
		obj := b.localObjs.NewEncodedObject()
		if err := t.Encode(obj); err != nil {
			b.fail(err, "encode tag %s", name)
			return
		}
		if h, err = store(b.localObjs, obj); err != nil {
			b.fail(err, "store tag %s", name)
			return
		}
	}
	b.localRefs[plumbing.NewTagReferenceName(name)] = h
}

// pushTag copies a tag to origin.
func (b *builder) pushTag(name string) {
	if b.err != nil {
		return
	}
	tag := plumbing.NewTagReferenceName(name)
	h, err := ref(b.localRefs, tag)
	if err != nil {
		b.fail(err, "push tag %s", name)
		return
	}
	b.fail(copyObjects(b.localObjs, b.remoteObjs, h), "push tag %s", name)
	b.remoteRefs[tag] = h
}

// checkout fills the new, empty working tree from a branch.
func (b *builder) checkout(branch string) {
	b.flush()
	if b.err != nil {
		return
	}
	wt, err := b.local.Worktree()
	if err != nil {
		b.fail(err, "open worktree")
		return
	}
	b.fail(wt.Checkout(&git.CheckoutOptions{Branch: plumbing.NewBranchReferenceName(branch)}), "check out %s", branch)
}

// writeFile changes the working tree without committing.
func (b *builder) writeFile(path, content string) {
	if b.err != nil {
		return
	}
	full := filepath.Join(b.dir, filepath.FromSlash(path))
	if err := os.MkdirAll(filepath.Dir(full), 0o755); err != nil {
		b.fail(err, "create folder for %s", path)
		return
	}
	b.fail(os.WriteFile(full, []byte(content), 0o644), "write %s", path)
}

func (b *builder) note(s string) { b.notes = append(b.notes, s) }

// signature advances the fixed clock by 6 to 18 hours.
func (b *builder) signature(a author) object.Signature {
	b.step++
	b.clock = b.clock.Add(time.Duration(6+(b.step*7)%13)*time.Hour + time.Duration((b.step*23)%60)*time.Minute)
	return object.Signature{Name: a.name, Email: a.email, When: b.clock.In(a.zone)}
}

func (b *builder) writeCommit(s storer.EncodedObjectStorer, parent plumbing.Hash, a author, msg string, files []file) (plumbing.Hash, error) {
	snap := maps.Clone(b.trees[parent])
	if snap == nil {
		snap = map[string]string{}
	}
	for _, fl := range files {
		snap[fl.path] = fl.content
	}
	tree, err := writeTree(s, snap, "")
	if err != nil {
		return plumbing.ZeroHash, err
	}
	sig := b.signature(a)
	c := &object.Commit{Author: sig, Committer: sig, Message: msg + "\n", TreeHash: tree}
	if !parent.IsZero() {
		c.ParentHashes = []plumbing.Hash{parent}
	}
	obj := s.NewEncodedObject()
	if err := c.Encode(obj); err != nil {
		return plumbing.ZeroHash, fmt.Errorf("encode commit: %w", err)
	}
	h, err := store(s, obj)
	if err != nil {
		return plumbing.ZeroHash, err
	}
	b.trees[h] = snap
	return h, nil
}

// writeTree stores the tree for the files under dir ("" for the root, else "a/b/").
func writeTree(s storer.EncodedObjectStorer, files map[string]string, dir string) (plumbing.Hash, error) {
	var entries []object.TreeEntry
	subdirs := map[string]bool{}
	for p, content := range files {
		rest, ok := strings.CutPrefix(p, dir)
		if !ok {
			continue
		}
		if name, _, isDir := strings.Cut(rest, "/"); isDir {
			if subdirs[name] {
				continue
			}
			subdirs[name] = true
			h, err := writeTree(s, files, dir+name+"/")
			if err != nil {
				return plumbing.ZeroHash, err
			}
			entries = append(entries, object.TreeEntry{Name: name, Mode: filemode.Dir, Hash: h})
			continue
		}
		obj := s.NewEncodedObject()
		obj.SetType(plumbing.BlobObject)
		w, err := obj.Writer()
		if err != nil {
			return plumbing.ZeroHash, fmt.Errorf("write blob %s: %w", p, err)
		}
		if _, err := w.Write([]byte(content)); err != nil {
			return plumbing.ZeroHash, fmt.Errorf("write blob %s: %w", p, err)
		}
		if err := w.Close(); err != nil {
			return plumbing.ZeroHash, fmt.Errorf("write blob %s: %w", p, err)
		}
		h, err := store(s, obj)
		if err != nil {
			return plumbing.ZeroHash, err
		}
		mode := filemode.Regular
		if executable[p] {
			mode = filemode.Executable
		}
		entries = append(entries, object.TreeEntry{Name: rest, Mode: mode, Hash: h})
	}
	// Git orders tree entries by name, with "/" appended to folder names.
	key := func(e object.TreeEntry) string {
		if e.Mode == filemode.Dir {
			return e.Name + "/"
		}
		return e.Name
	}
	sort.Slice(entries, func(i, j int) bool { return key(entries[i]) < key(entries[j]) })

	obj := s.NewEncodedObject()
	if err := (&object.Tree{Entries: entries}).Encode(obj); err != nil {
		return plumbing.ZeroHash, fmt.Errorf("encode tree %q: %w", dir, err)
	}
	return store(s, obj)
}

func store(s storer.EncodedObjectStorer, o plumbing.EncodedObject) (plumbing.Hash, error) {
	if s.HasEncodedObject(o.Hash()) == nil {
		return o.Hash(), nil
	}
	h, err := s.SetEncodedObject(o)
	if err != nil {
		return plumbing.ZeroHash, fmt.Errorf("store %s %s: %w", o.Type(), o.Hash(), err)
	}
	return h, nil
}

// copyObjects copies an object and everything it reaches, children first,
// which is what a push or fetch between the two repos would transfer.
func copyObjects(src, dst storer.EncodedObjectStorer, h plumbing.Hash) error {
	if dst.HasEncodedObject(h) == nil {
		return nil
	}
	o, err := src.EncodedObject(plumbing.AnyObject, h)
	if err != nil {
		return fmt.Errorf("read %s: %w", h, err)
	}
	var children []plumbing.Hash
	switch o.Type() {
	case plumbing.CommitObject:
		c, err := object.DecodeCommit(src, o)
		if err != nil {
			return fmt.Errorf("decode commit %s: %w", h, err)
		}
		children = append([]plumbing.Hash{c.TreeHash}, c.ParentHashes...)
	case plumbing.TreeObject:
		t, err := object.DecodeTree(src, o)
		if err != nil {
			return fmt.Errorf("decode tree %s: %w", h, err)
		}
		for _, e := range t.Entries {
			children = append(children, e.Hash)
		}
	case plumbing.TagObject:
		t, err := object.DecodeTag(src, o)
		if err != nil {
			return fmt.Errorf("decode tag %s: %w", h, err)
		}
		children = []plumbing.Hash{t.Target}
	}
	for _, c := range children {
		if err := copyObjects(src, dst, c); err != nil {
			return err
		}
	}
	_, err = store(dst, o)
	return err
}

func refHash(r *git.Repository, name plumbing.ReferenceName) (plumbing.Hash, error) {
	ref, err := r.Reference(name, true)
	if err != nil {
		return plumbing.ZeroHash, fmt.Errorf("resolve %s: %w", name, err)
	}
	return ref.Hash(), nil
}
