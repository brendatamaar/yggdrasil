package gittest

import (
	"context"
	"crypto/sha256"
	"encoding/hex"
	"errors"
	"fmt"
	"slices"
	"sort"
	"strings"
	"sync"

	"github.com/brendatamaar/yggdrasil/internal/git"
)

// FakeLanding is an in-memory git.LandingGit and git.BaseDiffer. It models one
// repo (commits with a full file snapshot, local branches) with several
// checkouts (the main one and linked worktrees), each with its own branch,
// working files and merge state. Merges are file-level: a path changed
// differently on both sides conflicts. It has no hooks, remote or ignore
// rules. Patches are synthesised, not git's bytes: only properties are
// comparable with real git (see LandingGitContract).
type FakeLanding struct {
	mu       sync.Mutex
	seq      int
	commits  map[string]*lcommit
	branches map[string]string
	checks   map[string]*lcheckout
	noIdent  bool
	ops      []string
}

type lcommit struct {
	parents []string
	files   map[string]string
	msg     string
}

type lcheckout struct {
	branch    string
	tree      map[string]string // working files
	mergeHead string
}

var (
	_ git.LandingGit = (*FakeLanding)(nil)
	_ git.BaseDiffer = (*FakeLanding)(nil)
)

// NewFakeLanding returns a fake with one repo: a checkout at dir on branch
// "main" holding README.md ("hello\n") in one root commit.
func NewFakeLanding(dir string) *FakeLanding {
	f := &FakeLanding{commits: map[string]*lcommit{}, branches: map[string]string{}, checks: map[string]*lcheckout{}}
	files := map[string]string{"README.md": "hello\n"}
	id := f.newCommit(nil, files, "init")
	f.branches["main"] = id
	f.checks[dir] = &lcheckout{branch: "main", tree: cloneFiles(files)}
	return f
}

// Ops returns the recorded calls.
func (f *FakeLanding) Ops() []string {
	f.mu.Lock()
	defer f.mu.Unlock()
	return slices.Clone(f.ops)
}

func (f *FakeLanding) newCommit(parents []string, files map[string]string, msg string) string {
	f.seq++
	h := sha256.Sum256(fmt.Appendf(nil, "fake-landing-%d", f.seq))
	id := hex.EncodeToString(h[:])[:40]
	f.commits[id] = &lcommit{parents: parents, files: cloneFiles(files), msg: msg}
	return id
}

func cloneFiles(m map[string]string) map[string]string {
	out := make(map[string]string, len(m))
	for k, v := range m {
		out[k] = v
	}
	return out
}

func fakeID(s string) bool {
	if len(s) != 40 && len(s) != 64 {
		return false
	}
	for _, c := range s {
		if (c < '0' || c > '9') && (c < 'a' || c > 'f') {
			return false
		}
	}
	return true
}

func gitErr(op string, err error, msg string) error {
	return &git.Error{Op: op, Err: err, UserMsg: msg}
}

func (f *FakeLanding) begin(op, dir string) (*lcheckout, error) {
	f.ops = append(f.ops, op)
	c, ok := f.checks[dir]
	if !ok {
		return nil, gitErr(op, fmt.Errorf("no repo at %q", dir), "This folder is not a git repository.")
	}
	return c, nil
}

func (f *FakeLanding) tip(c *lcheckout) *lcommit { return f.commits[f.branches[c.branch]] }

// resolve turns a commit id, branch or HEAD into a commit id.
func (f *FakeLanding) resolve(c *lcheckout, ref string) (string, bool) {
	switch {
	case ref == "" || strings.HasPrefix(ref, "-"):
		return "", false
	case ref == "HEAD":
		return f.branches[c.branch], true
	}
	if id, ok := f.branches[ref]; ok {
		return id, true
	}
	if _, ok := f.commits[ref]; ok {
		return ref, true
	}
	return "", false
}

func (f *FakeLanding) ancestors(id string) map[string]bool {
	seen := map[string]bool{}
	stack := []string{id}
	for len(stack) > 0 {
		x := stack[len(stack)-1]
		stack = stack[:len(stack)-1]
		if seen[x] {
			continue
		}
		seen[x] = true
		stack = append(stack, f.commits[x].parents...)
	}
	return seen
}

func (f *FakeLanding) mergeBase(a, b string) string {
	aa, bb := f.ancestors(a), f.ancestors(b)
	var common []string
	for id := range aa {
		if bb[id] {
			common = append(common, id)
		}
	}
	sort.Strings(common)
	for _, id := range common {
		best := true
		for _, other := range common {
			if other != id && f.ancestors(other)[id] {
				best = false // id is an ancestor of another common commit
			}
		}
		if best {
			return id
		}
	}
	return ""
}

// merge3 merges at file level: unchanged on one side takes the other side; the
// same change on both sides is kept; anything else conflicts.
func merge3(base, ours, theirs map[string]string) (merged map[string]string, conflicts []string) {
	paths := map[string]bool{}
	for _, m := range []map[string]string{base, ours, theirs} {
		for p := range m {
			paths[p] = true
		}
	}
	merged = map[string]string{}
	for p := range paths {
		b, bok := base[p]
		o, ook := ours[p]
		t, tok := theirs[p]
		same := func(x string, xok bool, y string, yok bool) bool { return xok == yok && x == y }
		var v string
		var ok bool
		switch {
		case same(o, ook, t, tok):
			v, ok = o, ook
		case same(o, ook, b, bok):
			v, ok = t, tok
		case same(t, tok, b, bok):
			v, ok = o, ook
		default:
			conflicts = append(conflicts, p)
			v, ok = "<<<<<<< ours\n"+o+"=======\n"+t+">>>>>>> theirs\n", true
		}
		if ok {
			merged[p] = v
		}
	}
	sort.Strings(conflicts)
	return merged, conflicts
}

func treeID(files map[string]string) string {
	keys := make([]string, 0, len(files))
	for k := range files {
		keys = append(keys, k)
	}
	sort.Strings(keys)
	h := sha256.New()
	for _, k := range keys {
		fmt.Fprintf(h, "%d:%s\x00%d:%s\x00", len(k), k, len(files[k]), files[k])
	}
	return hex.EncodeToString(h.Sum(nil))[:40]
}

func equalFiles(a, b map[string]string) bool {
	if len(a) != len(b) {
		return false
	}
	for k, v := range a {
		if w, ok := b[k]; !ok || w != v {
			return false
		}
	}
	return true
}

// MergeNoFF implements git.LandingGit.
func (f *FakeLanding) MergeNoFF(_ context.Context, dir, branchHead, message string) (string, error) {
	f.mu.Lock()
	defer f.mu.Unlock()
	c, err := f.begin("merge", dir)
	if err != nil {
		return "", err
	}
	if !fakeID(branchHead) {
		return "", gitErr("check commit", git.ErrInvalidArg, "A commit ID is not valid.")
	}
	if strings.TrimSpace(message) == "" {
		return "", gitErr("merge", git.ErrInvalidArg, "The merge message is empty.")
	}
	if f.noIdent {
		return "", gitErr("merge", git.ErrIdentityMissing, "Git does not know who you are. Set user.name and user.email.")
	}
	theirs, ok := f.commits[branchHead]
	if !ok {
		return "", gitErr("merge", errors.New("unknown commit"), "Git could not merge the branch.")
	}
	if c.mergeHead != "" {
		return "", gitErr("merge", errors.New("merge in progress"), "A merge is already in progress.")
	}
	headID := f.branches[c.branch]
	head := f.commits[headID]
	if !equalFiles(c.tree, head.files) {
		return "", gitErr("merge", errors.New("local changes would be overwritten"), "Git could not merge the branch.")
	}
	if f.ancestors(headID)[branchHead] {
		return headID, nil
	}
	base := f.commits[f.mergeBase(headID, branchHead)]
	baseFiles := map[string]string{}
	if base != nil {
		baseFiles = base.files
	}
	merged, conflicts := merge3(baseFiles, head.files, theirs.files)
	if len(conflicts) > 0 {
		c.tree = merged
		c.mergeHead = branchHead
		return "", &git.MergeConflictError{Files: conflicts}
	}
	id := f.newCommit([]string{headID, branchHead}, merged, strings.TrimSpace(message))
	f.branches[c.branch] = id
	c.tree = cloneFiles(merged)
	return id, nil
}

// MergeAbort implements git.LandingGit.
func (f *FakeLanding) MergeAbort(_ context.Context, dir string) error {
	f.mu.Lock()
	defer f.mu.Unlock()
	c, err := f.begin("merge-abort", dir)
	if err != nil {
		return err
	}
	if c.mergeHead == "" {
		return nil
	}
	c.tree = cloneFiles(f.tip(c).files)
	c.mergeHead = ""
	return nil
}

// MergeInProgress implements git.LandingGit.
func (f *FakeLanding) MergeInProgress(_ context.Context, dir string) (bool, string, error) {
	f.mu.Lock()
	defer f.mu.Unlock()
	c, err := f.begin("merge-head", dir)
	if err != nil {
		return false, "", err
	}
	return c.mergeHead != "", c.mergeHead, nil
}

// IsAncestor implements git.LandingGit.
func (f *FakeLanding) IsAncestor(_ context.Context, dir, ancestor, descendant string) (bool, error) {
	f.mu.Lock()
	defer f.mu.Unlock()
	c, err := f.begin("is-ancestor", dir)
	if err != nil {
		return false, err
	}
	a, aok := f.resolve(c, ancestor)
	d, dok := f.resolve(c, descendant)
	if !aok || !dok {
		return false, gitErr("is ancestor", errors.New("unknown revision"), "Git could not find that commit.")
	}
	return f.ancestors(d)[a], nil
}

// BranchHead implements git.LandingGit.
func (f *FakeLanding) BranchHead(_ context.Context, dir, branch string) (string, error) {
	f.mu.Lock()
	defer f.mu.Unlock()
	if _, err := f.begin("branch-head", dir); err != nil {
		return "", err
	}
	if branch == "" || strings.HasPrefix(branch, "-") || strings.ContainsAny(branch, " :?*[\\^~") || strings.Contains(branch, "..") {
		return "", gitErr("check branch", git.ErrInvalidArg, "The branch name is not valid.")
	}
	return f.branches[branch], nil
}

// MergeTreeWriteTree implements git.LandingGit.
func (f *FakeLanding) MergeTreeWriteTree(_ context.Context, dir, ours, theirs string) (git.Prediction, error) {
	f.mu.Lock()
	defer f.mu.Unlock()
	c, err := f.begin("merge-tree", dir)
	if err != nil {
		return git.Prediction{}, err
	}
	o, ook := f.resolve(c, ours)
	t, tok := f.resolve(c, theirs)
	if !ook || !tok {
		return git.Prediction{}, gitErr("merge-tree", git.ErrInvalidArg, "Git could not find that commit.")
	}
	baseFiles := map[string]string{}
	if b := f.commits[f.mergeBase(o, t)]; b != nil {
		baseFiles = b.files
	}
	merged, conflicts := merge3(baseFiles, f.commits[o].files, f.commits[t].files)
	return git.Prediction{Clean: len(conflicts) == 0, Files: conflicts, Tree: treeID(merged)}, nil
}

// WorktreePrune implements git.LandingGit. The fake has no vanished folders.
func (f *FakeLanding) WorktreePrune(_ context.Context, repoDir string) error {
	f.mu.Lock()
	defer f.mu.Unlock()
	_, err := f.begin("worktree-prune", repoDir)
	return err
}

// --- git.BaseDiffer ---

// changedLines returns the lines of old and new left after dropping the common
// leading and trailing lines.
func changedLines(old, nw string) (o, n []string) {
	ol, nl := strings.Split(old, "\n"), strings.Split(nw, "\n")
	i := 0
	for i < len(ol) && i < len(nl) && ol[i] == nl[i] {
		i++
	}
	j := 0
	for j < len(ol)-i && j < len(nl)-i && ol[len(ol)-1-j] == nl[len(nl)-1-j] {
		j++
	}
	trim := func(x []string) []string {
		x = x[i : len(x)-j]
		if len(x) == 1 && x[0] == "" {
			return nil
		}
		return x
	}
	return trim(ol), trim(nl)
}

func fakePatch(path, status, old, nw string) (patch string, added, deleted int) {
	var b strings.Builder
	fmt.Fprintf(&b, "diff --git a/%s b/%s\n", path, path)
	switch status {
	case git.FileAdded:
		b.WriteString("new file mode 100644\n--- /dev/null\n")
	case git.FileDeleted:
		b.WriteString("deleted file mode 100644\n")
	}
	o, n := changedLines(old, nw)
	if status == git.FileAdded {
		o, n = nil, splitLinesTrim(nw)
	}
	if status == git.FileDeleted {
		o, n = splitLinesTrim(old), nil
	}
	fmt.Fprintf(&b, "@@ -1,%d +1,%d @@\n", len(o), len(n))
	for _, l := range o {
		b.WriteString("-" + l + "\n")
	}
	for _, l := range n {
		b.WriteString("+" + l + "\n")
	}
	return b.String(), len(n), len(o)
}

func splitLinesTrim(s string) []string {
	if s == "" {
		return nil
	}
	return strings.Split(strings.TrimSuffix(s, "\n"), "\n")
}

func baseFileHash(status, path, payload string) string {
	h := sha256.New()
	fmt.Fprintf(h, "fake-basediff\x00%s\x00%s\x00%s", status, path, payload)
	return hex.EncodeToString(h.Sum(nil))
}

// DiffAgainstBase implements git.BaseDiffer.
func (f *FakeLanding) DiffAgainstBase(_ context.Context, dir, base string, opts git.BaseDiffOptions) (git.BaseDiff, error) {
	f.mu.Lock()
	defer f.mu.Unlock()
	c, err := f.begin("diff-base", dir)
	if err != nil {
		return git.BaseDiff{}, err
	}
	bc, ok := f.commits[base]
	if !fakeID(base) || !ok {
		return git.BaseDiff{}, gitErr("diff base", git.ErrInvalidArg, "A commit ID is not valid.")
	}
	limit := opts.PatchLimit
	if limit <= 0 {
		limit = 256 << 10
	}
	maxFiles := opts.MaxFiles
	if maxFiles <= 0 {
		maxFiles = 5000
	}
	paths := map[string]bool{}
	for p := range bc.files {
		paths[p] = true
	}
	for p := range c.tree {
		paths[p] = true
	}
	var names []string
	for p := range paths {
		old, inOld := bc.files[p]
		nw, inNew := c.tree[p]
		if inOld == inNew && old == nw {
			continue
		}
		names = append(names, p)
	}
	sort.Strings(names)
	res := git.BaseDiff{HasChanges: len(names) > 0}
	if len(names) > maxFiles {
		names, res.Truncated = names[:maxFiles], true
	}
	for _, p := range names {
		old, inOld := bc.files[p]
		nw, inNew := c.tree[p]
		bf := git.BaseFile{Path: p}
		switch {
		case !inOld:
			bf.Status = git.FileAdded
		case !inNew:
			bf.Status = git.FileDeleted
		default:
			bf.Status = git.FileModified
		}
		if strings.ContainsRune(old, 0) || strings.ContainsRune(nw, 0) {
			bf.Binary = true
			bf.Hash = baseFileHash(bf.Status, p, "bin:"+nw)
			res.Files = append(res.Files, bf)
			continue
		}
		patch, added, deleted := fakePatch(p, bf.Status, old, nw)
		bf.Added, bf.Deleted = added, deleted
		bf.EOLOnly = bf.Status == git.FileModified && strings.ReplaceAll(old, "\r\n", "\n") == strings.ReplaceAll(nw, "\r\n", "\n")
		if len(patch) > limit {
			patch, bf.PatchTruncated, res.Truncated = patch[:limit], true, true
			bf.Hash = baseFileHash(bf.Status, p, "cut:"+nw)
		} else {
			bf.Hash = baseFileHash(bf.Status, p, "patch:"+patch)
		}
		bf.Patch = patch
		res.Files = append(res.Files, bf)
	}
	return res, nil
}

// HasChanges implements git.BaseDiffer.
func (f *FakeLanding) HasChanges(ctx context.Context, dir, base string) (bool, error) {
	d, err := f.DiffAgainstBase(ctx, dir, base, git.BaseDiffOptions{MaxFiles: 1})
	return d.HasChanges, err
}

// --- harness side (not part of the interfaces) ---

// SetNoIdentity makes MergeNoFF fail as git does when the user is unknown.
func (f *FakeLanding) SetNoIdentity(v bool) {
	f.mu.Lock()
	defer f.mu.Unlock()
	f.noIdent = v
}

func (f *FakeLanding) commitTree(branch string, files map[string]string, msg string) string {
	id := f.newCommit([]string{f.branches[branch]}, files, msg)
	f.branches[branch] = id
	return id
}

// CommitOnBranch commits one file on branch (made from the tip of "main" when
// it does not exist), without touching any checkout.
func (f *FakeLanding) CommitOnBranch(branch, rel, content string) string {
	f.mu.Lock()
	defer f.mu.Unlock()
	if _, ok := f.branches[branch]; !ok {
		f.branches[branch] = f.branches["main"]
	}
	files := cloneFiles(f.commits[f.branches[branch]].files)
	files[rel] = content
	return f.commitTree(branch, files, "commit on "+branch)
}

// WriteFile changes a working file of the checkout at dir.
func (f *FakeLanding) WriteFile(dir, rel, content string) {
	f.mu.Lock()
	defer f.mu.Unlock()
	f.checks[dir].tree[rel] = content
}

// ReadFile reads a working file of the checkout at dir.
func (f *FakeLanding) ReadFile(dir, rel string) (string, bool) {
	f.mu.Lock()
	defer f.mu.Unlock()
	v, ok := f.checks[dir].tree[rel]
	return v, ok
}

// CommitAll commits the whole working tree of the checkout at dir on its branch.
func (f *FakeLanding) CommitAll(dir, msg string) string {
	f.mu.Lock()
	defer f.mu.Unlock()
	c := f.checks[dir]
	return f.commitTree(c.branch, c.tree, msg)
}

// AddWorktree adds a checkout at dir on a new branch started from "main".
func (f *FakeLanding) AddWorktree(dir, branch string) {
	f.mu.Lock()
	defer f.mu.Unlock()
	f.branches[branch] = f.branches["main"]
	f.checks[dir] = &lcheckout{branch: branch, tree: cloneFiles(f.commits[f.branches["main"]].files)}
}

// TreeUnchanged says whether the checkout at dir has no change from its branch
// tip and no merge in progress.
func (f *FakeLanding) TreeUnchanged(dir string) bool {
	f.mu.Lock()
	defer f.mu.Unlock()
	c := f.checks[dir]
	return c.mergeHead == "" && equalFiles(c.tree, f.tip(c).files)
}

// MoveFile renames a working file of the checkout at dir.
func (f *FakeLanding) MoveFile(dir, from, to string) {
	f.mu.Lock()
	defer f.mu.Unlock()
	c := f.checks[dir]
	c.tree[to] = c.tree[from]
	delete(c.tree, from)
}

// Parents returns the parents of a commit.
func (f *FakeLanding) Parents(id string) []string {
	f.mu.Lock()
	defer f.mu.Unlock()
	if c, ok := f.commits[id]; ok {
		return slices.Clone(c.parents)
	}
	return nil
}
