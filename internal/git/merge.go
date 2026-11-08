package git

import (
	"context"
	"errors"
	"fmt"
	"sort"
	"strings"
)

// Errors of the landing operations (RV-05).
var (
	// ErrIdentityMissing: git has no user name or email to write the merge
	// commit with.
	ErrIdentityMissing = errors.New("git identity is not set")
)

// MergeConflictError is returned by MergeNoFF when the merge stopped on
// conflicts. The repo is left in the merge state git leaves it in; the caller
// decides whether to MergeAbort.
type MergeConflictError struct {
	// Files are the unmerged paths, sorted.
	Files []string
}

func (e *MergeConflictError) Error() string {
	return fmt.Sprintf("git merge: conflicts in %d file(s)", len(e.Files))
}

// UserMessage says what happened and what to do next.
func (e *MergeConflictError) UserMessage() string {
	return "The branch cannot be merged without conflicts. Nothing was committed. Resolve the conflicting files in the worktree first."
}

// isCommitID reports whether s is a full lower-case hex object id (SHA-1 or
// SHA-256).
func isCommitID(s string) bool {
	if len(s) != 40 && len(s) != 64 {
		return false
	}
	for i := 0; i < len(s); i++ {
		c := s[i]
		if (c < '0' || c > '9') && (c < 'a' || c > 'f') {
			return false
		}
	}
	return true
}

func checkCommitID(kind, s string) error {
	if !isCommitID(s) {
		return &Error{Op: "check " + kind, Err: fmt.Errorf("%w: %s", ErrInvalidArg, kind),
			UserMsg: "A commit ID is not valid. Refresh and try again."}
	}
	return nil
}

// MergeArgv is the argument list of the merge itself (after "git"): always a
// merge commit (--no-ff) of one exact commit, hooks switched off, the message
// as a single argument.
func MergeArgv(hooksDir, branchHead, message string) []string {
	return []string{"-c", "core.hooksPath=" + hooksDir, "-c", "core.fsmonitor=false",
		"merge", "--no-ff", "--no-verify", "-q", "-m", message, branchHead}
}

// MergeAbortArgv is the argument list of the abort.
func MergeAbortArgv() []string {
	return []string{"-c", "core.fsmonitor=false", "merge", "--abort"}
}

// MergeNoFF merges the exact commit branchHead into the checked-out branch of
// dir with a merge commit, hooks off, and returns the new HEAD. It never
// pushes. When branchHead is already part of HEAD nothing is merged and the
// unchanged HEAD is returned (compare with the HEAD before the call).
//
// Errors: *MergeConflictError (the merge is left in progress; call MergeAbort
// to restore the tree), ErrIdentityMissing, or a plain *Error.
func (g *Git) MergeNoFF(ctx context.Context, dir, branchHead, message string) (string, error) {
	if err := checkCommitID("commit", branchHead); err != nil {
		return "", err
	}
	if strings.TrimSpace(message) == "" || strings.ContainsRune(message, 0) {
		return "", &Error{Op: "merge", Err: ErrInvalidArg, UserMsg: "The merge message is empty. Write a message."}
	}
	hooks, err := g.noHooks()
	if err != nil {
		return "", err
	}
	if _, _, code, err := g.exec(ctx, dir, "author", nil, "var", "GIT_COMMITTER_IDENT"); err != nil {
		return "", err
	} else if code != 0 {
		return "", identityMissing()
	}
	_, se, code, err := g.exec(ctx, dir, "merge", nil, MergeArgv(hooks, branchHead, strings.TrimSpace(message))...)
	if err != nil {
		return "", err
	}
	if code != 0 {
		low := strings.ToLower(se)
		if strings.Contains(low, "please tell me who you are") || strings.Contains(low, "empty ident name") ||
			strings.Contains(low, "unable to auto-detect") {
			return "", identityMissing()
		}
		if files, ferr := g.unmergedFiles(ctx, dir); ferr == nil && len(files) > 0 {
			if in, _, ierr := g.MergeInProgress(ctx, dir); ierr == nil && in {
				return "", &MergeConflictError{Files: files}
			}
		}
		return "", &Error{Op: "merge", Code: code, Stderr: se, Err: errors.New("exit status " + fmt.Sprint(code)),
			UserMsg: "Git could not merge the branch. Nothing was committed. See the details and fix the problem in the repo, then try again."}
	}
	return g.Head(ctx, dir)
}

func identityMissing() error {
	return &Error{Op: "merge", Err: ErrIdentityMissing,
		UserMsg: `Git does not know who you are. Run: git config --global user.name "Your Name" and git config --global user.email "you@example.com", then retry.`}
}

// unmergedFiles lists the paths git marks as unmerged (conflicted), sorted.
func (g *Git) unmergedFiles(ctx context.Context, dir string) ([]string, error) {
	out, err := g.run(ctx, dir, "unmerged files", true, "-c", "core.fsmonitor=false", "diff", "--name-only", "--diff-filter=U", "-z", "--")
	if err != nil {
		return nil, err
	}
	files := splitNUL(out)
	sort.Strings(files)
	return files, nil
}

// MergeAbort ends a merge in progress and restores the tree to HEAD. With no
// merge in progress it does nothing and returns nil.
func (g *Git) MergeAbort(ctx context.Context, dir string) error {
	in, _, err := g.MergeInProgress(ctx, dir)
	if err != nil {
		return err
	}
	if !in {
		return nil
	}
	_, err = g.execOK(ctx, dir, "merge abort", nil, MergeAbortArgv()...)
	return err
}

// MergeInProgress says whether a merge is unfinished in dir and, if so, the
// commit being merged (MERGE_HEAD).
func (g *Git) MergeInProgress(ctx context.Context, dir string) (inProgress bool, mergeHead string, err error) {
	out, _, code, err := g.exec(ctx, dir, "merge head", nil, "--no-optional-locks", "rev-parse", "-q", "--verify", "MERGE_HEAD")
	if err != nil {
		return false, "", err
	}
	switch code {
	case 0:
		return true, strings.TrimSpace(out), nil
	case 1:
		return false, "", nil
	}
	return false, "", &Error{Op: "merge head", Code: code, Err: errors.New("exit status " + fmt.Sprint(code)),
		UserMsg: "Yggdrasil could not tell whether a merge is in progress. Check that the folder is a git repository."}
}

// IsAncestor says whether ancestor is reachable from descendant (or equal).
// Both are commit IDs or refs.
func (g *Git) IsAncestor(ctx context.Context, dir, ancestor, descendant string) (bool, error) {
	if err := checkName("revision", ancestor); err != nil {
		return false, err
	}
	if err := checkName("revision", descendant); err != nil {
		return false, err
	}
	_, se, code, err := g.exec(ctx, dir, "is ancestor", nil, "--no-optional-locks", "merge-base", "--is-ancestor", ancestor, descendant)
	if err != nil {
		return false, err
	}
	switch code {
	case 0:
		return true, nil
	case 1:
		return false, nil
	}
	return false, &Error{Op: "is ancestor", Code: code, Stderr: se, Err: errors.New("exit status " + fmt.Sprint(code)), UserMsg: classify("is ancestor", se)}
}

// BranchHead returns the commit a local branch points to, or "" (and nil) when
// the branch does not exist.
func (g *Git) BranchHead(ctx context.Context, dir, branch string) (string, error) {
	if err := checkBranchName(branch); err != nil {
		return "", err
	}
	out, se, code, err := g.exec(ctx, dir, "branch head", nil, "--no-optional-locks", "rev-parse", "-q", "--verify", "refs/heads/"+branch+"^{commit}")
	if err != nil {
		return "", err
	}
	switch code {
	case 0:
		return strings.TrimSpace(out), nil
	case 1:
		return "", nil
	}
	return "", &Error{Op: "branch head", Code: code, Stderr: se, Err: errors.New("exit status " + fmt.Sprint(code)), UserMsg: classify("branch head", se)}
}
