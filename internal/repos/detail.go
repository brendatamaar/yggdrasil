package repos

import (
	"context"
	"errors"
	"fmt"
	"strings"
	"time"

	"github.com/brendatamaar/yggdrasil/internal/apperr"
	"github.com/brendatamaar/yggdrasil/internal/core"
	"github.com/brendatamaar/yggdrasil/internal/db"
	"github.com/brendatamaar/yggdrasil/internal/git"
)

// MaxDiffBytes caps the diff text of one response.
const MaxDiffBytes = 2 << 20

// DetailTimeout limits one git read of the repo page.
const DetailTimeout = 15 * time.Second

// emptyTree is git's empty tree, the "parent" of a root commit.
const emptyTree = "4b825dc642cb6eb9a060e54bf8d69288fbee4904"

// DetailRepos finds a tracked repo. *db.Store implements it; its reads return
// tracked repos only (SF-02).
type DetailRepos interface {
	GetRepo(ctx context.Context, id core.RepoID) (core.Repo, error)
}

// DetailGit is the part of *git.Git the repo page reads use.
type DetailGit interface {
	Status(ctx context.Context, dir string) (git.Status, error)
	Diff(ctx context.Context, dir string, o git.DiffOptions) (string, error)
	LogRef(ctx context.Context, dir, ref string, n int) ([]git.Commit, error)
	Show(ctx context.Context, dir, rev string) (git.CommitDetail, error)
	Branches(ctx context.Context, dir string) ([]git.Branch, error)
}

// DiffResult is diff text, cut at MaxDiffBytes.
type DiffResult struct {
	Text      string
	Truncated bool
	// Binary: git reported at least one binary file; it has no text in Text.
	Binary bool
}

// CommitResult is a commit with its diff.
type CommitResult struct {
	Detail git.CommitDetail
	Diff   DiffResult
}

// DetailService answers the read-only questions of the repo page (WS-05). It
// holds no state, changes nothing and writes no audit entries. It implements
// api.RepoDetailService.
//
// Wiring (cmd/ygg): repos.NewDetailService(store, g) and
// Deps.RepoDetail = that value.
type DetailService struct {
	repos   DetailRepos
	git     DetailGit
	timeout time.Duration
}

// NewDetailService returns a service reading through g.
func NewDetailService(r DetailRepos, g DetailGit) *DetailService {
	return &DetailService{repos: r, git: g, timeout: DetailTimeout}
}

func (s *DetailService) dir(ctx context.Context, id core.RepoID) (string, error) {
	r, err := s.repos.GetRepo(ctx, id)
	if errors.Is(err, db.ErrNotFound) {
		return "", apperr.New(apperr.NotFound, "This repo isn't tracked (any more). Refresh the page.", err)
	}
	if err != nil {
		return "", fmt.Errorf("find repo %s: %w", id, err)
	}
	return r.Path, nil
}

// gitErr maps a git failure to an error class.
func gitErr(err error) error {
	if err == nil {
		return nil
	}
	var ge *git.Error
	if errors.As(err, &ge) {
		low := strings.ToLower(ge.Stderr)
		if errors.Is(err, git.ErrInvalidArg) || strings.Contains(low, "unknown revision") ||
			strings.Contains(low, "bad revision") || strings.Contains(low, "bad object") ||
			strings.Contains(low, "ambiguous argument") || strings.Contains(low, "invalid object name") {
			return apperr.New(apperr.NotFound, "That commit or branch wasn't found. Refresh the page.", err)
		}
		return apperr.New(apperr.Unavailable, ge.UserMessage(), err)
	}
	return apperr.New(apperr.Unavailable, "Git could not answer. Try again.", err)
}

// Changes lists the changed files of the working tree.
func (s *DetailService) Changes(ctx context.Context, id core.RepoID) (git.Status, error) {
	dir, err := s.dir(ctx, id)
	if err != nil {
		return git.Status{}, err
	}
	ctx, cancel := context.WithTimeout(ctx, s.timeout)
	defer cancel()
	st, err := s.git.Status(ctx, dir)
	if err != nil {
		return git.Status{}, gitErr(err)
	}
	if st.Files == nil {
		st.Files = []git.FileChange{}
	}
	return st, nil
}

// Diff returns the diff of one file, or of all files when path is empty.
func (s *DetailService) Diff(ctx context.Context, id core.RepoID, path RelPath, side DiffSide) (DiffResult, error) {
	dir, err := s.dir(ctx, id)
	if err != nil {
		return DiffResult{}, err
	}
	ctx, cancel := context.WithTimeout(ctx, s.timeout)
	defer cancel()
	mode := git.DiffWorktree
	if side == Staged {
		mode = git.DiffStaged
	}
	out, err := s.git.Diff(ctx, dir, git.DiffOptions{Mode: mode, Path: path.String(), Limit: MaxDiffBytes})
	if err != nil {
		return DiffResult{}, gitErr(err)
	}
	return capDiff(out), nil
}

// Log returns up to limit commits from ref (HEAD when empty).
func (s *DetailService) Log(ctx context.Context, id core.RepoID, limit Limit, ref Ref) ([]git.Commit, error) {
	dir, err := s.dir(ctx, id)
	if err != nil {
		return nil, err
	}
	ctx, cancel := context.WithTimeout(ctx, s.timeout)
	defer cancel()
	cs, err := s.git.LogRef(ctx, dir, ref.String(), int(limit))
	if err != nil {
		return nil, gitErr(err)
	}
	if cs == nil {
		cs = []git.Commit{}
	}
	return cs, nil
}

// Commit returns one commit with its files and diff against its first parent
// (against the empty tree for a root commit).
func (s *DetailService) Commit(ctx context.Context, id core.RepoID, oid OID) (CommitResult, error) {
	dir, err := s.dir(ctx, id)
	if err != nil {
		return CommitResult{}, err
	}
	ctx, cancel := context.WithTimeout(ctx, s.timeout)
	defer cancel()
	d, err := s.git.Show(ctx, dir, oid.String())
	if err != nil {
		return CommitResult{}, gitErr(err)
	}
	if d.Files == nil {
		d.Files = []git.CommitFile{}
	}
	from := emptyTree
	if len(d.Parents) > 0 {
		from = d.Parents[0]
	}
	out, err := s.git.Diff(ctx, dir, git.DiffOptions{Mode: git.DiffRefs, From: from, To: d.Hash, Limit: MaxDiffBytes})
	if err != nil {
		return CommitResult{}, gitErr(err)
	}
	return CommitResult{Detail: d, Diff: capDiff(out)}, nil
}

// Branches lists local and remote-tracking branches.
func (s *DetailService) Branches(ctx context.Context, id core.RepoID) ([]git.Branch, error) {
	dir, err := s.dir(ctx, id)
	if err != nil {
		return nil, err
	}
	ctx, cancel := context.WithTimeout(ctx, s.timeout)
	defer cancel()
	bs, err := s.git.Branches(ctx, dir)
	if err != nil {
		return nil, gitErr(err)
	}
	if bs == nil {
		bs = []git.Branch{}
	}
	return bs, nil
}

// capDiff cuts text at MaxDiffBytes on a line end and flags binary files.
func capDiff(text string) DiffResult {
	res := DiffResult{Text: text, Binary: strings.HasPrefix(text, "Binary files ") ||
		strings.Contains(text, "\nBinary files ") || strings.Contains(text, "\nGIT binary patch")}
	if len(text) > MaxDiffBytes {
		cut := text[:MaxDiffBytes]
		if i := strings.LastIndexByte(cut, '\n'); i > 0 {
			cut = cut[:i+1]
		}
		res.Text, res.Truncated = cut, true
	}
	return res
}
