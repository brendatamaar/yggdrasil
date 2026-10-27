package git

import (
	"bytes"
	"context"
	"crypto/sha256"
	"encoding/hex"
	"errors"
	"fmt"
	"io"
	"os"
	"path/filepath"
	"sort"
	"strings"

	"github.com/brendatamaar/yggdrasil/internal/platform"
)

// Errors of the write operations. Each one reaches the user through
// UserMessage on the *Error that wraps it.
var (
	// ErrStaleHead: the branch no longer points where the proposal said.
	ErrStaleHead = errors.New("head moved since the proposal was made")
	// ErrNothingToCommit: none of the paths has a change to commit.
	ErrNothingToCommit = errors.New("nothing to commit")
	// ErrNoAuthor: git has no user name or email.
	ErrNoAuthor = errors.New("git author is not set")
	// ErrNoHooksDir: the empty hooks folder could not be made or is not empty.
	ErrNoHooksDir = errors.New("hooks folder for proposals is not empty")
)

// CommitInfo is the commit HEAD points to.
type CommitInfo struct {
	ID      string
	Parent  string // "" for a root commit
	Message string // trimmed
}

// Writer is the part of git that executes confirmed proposals (RV-02). It has
// no force, reset, clean, delete or remove operation: none exists to call
// (invariant 1). *Git implements it; gittest.Fake is the in-memory fake.
type Writer interface {
	// Head returns the full commit ID of HEAD.
	Head(ctx context.Context, dir string) (string, error)
	// HeadInfo returns HEAD with its parent and message.
	HeadInfo(ctx context.Context, dir string) (CommitInfo, error)
	// DiffHash fingerprints the changes of paths (tracked diff against HEAD and
	// the content of untracked files). Equal changes, equal hash.
	DiffHash(ctx context.Context, dir string, paths []string) (string, error)
	// Add stages exactly paths, as literal pathspecs. There is no "all".
	Add(ctx context.Context, dir string, paths []string) error
	// Commit commits exactly paths with message, with hooks off, and returns
	// the new commit ID. expectHead, when set, must still be HEAD
	// (ErrStaleHead otherwise).
	Commit(ctx context.Context, dir string, paths []string, message, expectHead string) (string, error)
	// Push pushes req.Branch to req.Remote without force. It refuses the
	// default branch, a remote URL that carries credentials, and a branch tip
	// that is not req.ExpectHead.
	Push(ctx context.Context, dir string, req PushRequest) (PushResult, error)
	// LsRemote returns the commit ID the remote has for the branch, or "" when
	// the branch is not there.
	LsRemote(ctx context.Context, dir, remote, branch string) (string, error)
	// RemoteURL returns the fetch URL of a remote (may carry credentials: never
	// log it).
	RemoteURL(ctx context.Context, dir, name string) (string, error)
}

var _ Writer = (*Git)(nil)

// noHooks returns the empty folder core.hooksPath points at, so no hook of the
// repo (which an agent may have written) runs on a confirmed proposal. The
// folder is made once per Git; it must stay empty, or the operation is refused.
func (g *Git) noHooks() (string, error) {
	g.hooksOnce.Do(func() {
		g.hooksDir, g.hooksErr = os.MkdirTemp("", "ygg-nohooks-")
	})
	if g.hooksErr != nil {
		return "", &Error{Op: "hooks", Err: g.hooksErr, UserMsg: "Yggdrasil could not prepare a safe folder to run git without hooks."}
	}
	entries, err := os.ReadDir(g.hooksDir)
	if err != nil || len(entries) != 0 {
		return "", &Error{Op: "hooks", Err: ErrNoHooksDir, UserMsg: "The folder Yggdrasil uses to switch git hooks off is not empty. Restart Yggdrasil and try again."}
	}
	return g.hooksDir, nil
}

// exec runs git and returns stdout, stderr (both redacted) and the exit code.
// err is set only when git could not run or the context ended. Arguments never
// hold a token.
func (g *Git) exec(ctx context.Context, dir, op string, stdin io.Reader, args ...string) (stdout, stderr string, code int, err error) {
	argv := append([]string{g.exe}, baseConfig...)
	argv = append(argv, args...)
	var so, se bytes.Buffer
	p, err := platform.Start(ctx, platform.Spec{
		Argv: argv, Dir: dir, Env: g.env, TaskID: g.taskID, Stdin: stdin, Stdout: &so, Stderr: &se,
	})
	if err != nil {
		return "", "", 0, &Error{Op: op, Err: err, UserMsg: startMessage(err)}
	}
	code, err = p.Wait()
	if err != nil {
		return "", "", 0, &Error{Op: op, Err: err, UserMsg: fmt.Sprintf("git %s did not finish. Try again.", op)}
	}
	if ctx.Err() != nil {
		return "", "", 0, &Error{Op: op, Err: ctx.Err(), UserMsg: fmt.Sprintf("git %s was cancelled.", op)}
	}
	return g.redact.Redact(so.String()), strings.TrimSpace(g.redact.Redact(se.String())), code, nil
}

// execOK is exec that turns a non-zero exit into an *Error.
func (g *Git) execOK(ctx context.Context, dir, op string, stdin io.Reader, args ...string) (string, error) {
	so, se, code, err := g.exec(ctx, dir, op, stdin, args...)
	if err != nil {
		return "", err
	}
	if code != 0 {
		return "", &Error{Op: op, Code: code, Stderr: se, Err: errors.New("exit status " + fmt.Sprint(code)), UserMsg: classify(op, se)}
	}
	return so, nil
}

// checkPathspecs refuses what would mean "everything" or leave the repo. The
// proposals boundary parser is stricter; this is the last line.
func checkPathspecs(paths []string) error {
	if len(paths) == 0 {
		return &Error{Op: "check paths", Err: ErrInvalidArg, UserMsg: "No files were named. Name the files to include."}
	}
	for _, p := range paths {
		bad := p == "" || p == "." || strings.ContainsRune(p, 0) || strings.ContainsAny(p, "\r\n") ||
			filepath.IsAbs(p) || strings.HasPrefix(p, "/") || strings.HasPrefix(p, `\`) || strings.HasPrefix(p, ":") ||
			strings.HasPrefix(p, "-")
		if !bad {
			for _, seg := range strings.FieldsFunc(p, func(r rune) bool { return r == '/' || r == '\\' }) {
				if seg == ".." || strings.EqualFold(seg, ".git") {
					bad = true
				}
			}
		}
		if bad {
			return &Error{Op: "check paths", Err: fmt.Errorf("%w: path", ErrInvalidArg), UserMsg: "A file path is not valid for this repo. Use paths inside the repo."}
		}
	}
	return nil
}

// literal is the global option that makes every pathspec literal: no glob, no
// magic, so an agent-chosen name never expands.
const literal = "--literal-pathspecs"

// AddArgv is the argument list of Add (after "git").
func AddArgv(paths []string) []string {
	return append([]string{"-c", "core.fsmonitor=false", literal, "add", "--"}, paths...)
}

// CommitArgv is the argument list of the commit itself. The message goes on
// stdin (-F -), never in the argument list.
func CommitArgv(paths []string, hooksDir string) []string {
	a := []string{"-c", "core.hooksPath=" + hooksDir, "-c", "core.fsmonitor=false", literal,
		"commit", "--no-verify", "--only", "-q", "-F", "-", "--"}
	return append(a, paths...)
}

// Head returns the full commit ID of HEAD.
func (g *Git) Head(ctx context.Context, dir string) (string, error) {
	return g.ResolveCommit(ctx, dir, "HEAD")
}

// HeadInfo returns HEAD, its first parent and its message.
func (g *Git) HeadInfo(ctx context.Context, dir string) (CommitInfo, error) {
	out, err := g.run(ctx, dir, "head info", true, "-c", "core.fsmonitor=false", "log", "-1", "--no-color", "--format=%H%x00%P%x00%B", "HEAD", "--")
	if err != nil {
		return CommitInfo{}, err
	}
	parts := strings.SplitN(out, "\x00", 3)
	if len(parts) != 3 {
		return CommitInfo{}, &Error{Op: "head info", Err: errors.New("unexpected log output"), UserMsg: "Yggdrasil could not read the latest commit."}
	}
	parent, _, _ := strings.Cut(strings.TrimSpace(parts[1]), " ")
	return CommitInfo{ID: strings.TrimSpace(parts[0]), Parent: parent, Message: strings.TrimSpace(parts[2])}, nil
}

// DiffHash fingerprints the changes under paths: `git diff --binary HEAD` for
// tracked files and the path and content of every untracked file. It only reads.
func (g *Git) DiffHash(ctx context.Context, dir string, paths []string) (string, error) {
	if err := checkPathspecs(paths); err != nil {
		return "", err
	}
	ro := func(args ...string) (string, error) {
		return g.run(ctx, dir, "diff hash", true, append([]string{"-c", "core.fsmonitor=false", literal}, args...)...)
	}
	diff, err := ro(append([]string{"diff", "--binary", "--no-color", "--no-ext-diff", "--no-textconv", "--no-renames", "HEAD", "--"}, paths...)...)
	if err != nil {
		return "", err
	}
	others, err := ro(append([]string{"ls-files", "--others", "--exclude-standard", "-z", "--"}, paths...)...)
	if err != nil {
		return "", err
	}
	untracked := splitNUL(others)
	sort.Strings(untracked)
	h := sha256.New()
	fmt.Fprintf(h, "diff:%d\x00%s\x00", len(diff), diff)
	budget := &untrackedBudget{ctx: ctx, maxFiles: snapshotMaxFiles, maxBytes: snapshotMaxBytes}
	for _, rel := range untracked {
		if err := hashUntracked(h, dir, rel, budget); err != nil {
			if ctx.Err() != nil {
				return "", ctx.Err()
			}
			return "", &Error{Op: "diff hash", Err: err, UserMsg: "Yggdrasil could not read every new file to check that nothing changed."}
		}
	}
	return hex.EncodeToString(h.Sum(nil)), nil
}

// Add stages exactly paths (literal pathspecs; never -A).
func (g *Git) Add(ctx context.Context, dir string, paths []string) error {
	if err := checkPathspecs(paths); err != nil {
		return err
	}
	_, err := g.execOK(ctx, dir, "add", nil, AddArgv(paths)...)
	return err
}

// Commit commits exactly paths with message. Hooks are off (core.hooksPath is
// an empty folder, --no-verify), because a repo's hook can be a file an agent
// wrote. The author comes from the user's git config; when it is unset the
// error says how to set it. Call Add first for new files.
func (g *Git) Commit(ctx context.Context, dir string, paths []string, message, expectHead string) (string, error) {
	if err := checkPathspecs(paths); err != nil {
		return "", err
	}
	if strings.TrimSpace(message) == "" || strings.ContainsRune(message, 0) {
		return "", &Error{Op: "commit", Err: ErrInvalidArg, UserMsg: "The commit message is empty. Write a message."}
	}
	hooks, err := g.noHooks()
	if err != nil {
		return "", err
	}
	if expectHead != "" {
		head, err := g.Head(ctx, dir)
		if err != nil {
			return "", err
		}
		if head != expectHead {
			return "", &Error{Op: "commit", Err: ErrStaleHead, UserMsg: "The branch has changed since this commit was proposed. Reject it and ask for a new one."}
		}
	}
	for _, v := range []string{"GIT_AUTHOR_IDENT", "GIT_COMMITTER_IDENT"} {
		if _, _, code, err := g.exec(ctx, dir, "author", nil, "var", v); err != nil {
			return "", err
		} else if code != 0 {
			return "", &Error{Op: "commit", Err: ErrNoAuthor,
				UserMsg: `Git does not know who you are. Run: git config --global user.name "Your Name" and git config --global user.email "you@example.com", then retry.`}
		}
	}
	// Anything staged under paths? (1 = yes, 0 = nothing.)
	_, _, code, err := g.exec(ctx, dir, "commit", nil, append([]string{"-c", "core.fsmonitor=false", literal, "diff", "--cached", "--quiet", "--"}, paths...)...)
	if err != nil {
		return "", err
	}
	if code == 0 {
		return "", &Error{Op: "commit", Err: ErrNothingToCommit, UserMsg: "There is nothing to commit in these files. They may already be committed."}
	}
	if _, err := g.execOK(ctx, dir, "commit", strings.NewReader(strings.TrimSpace(message)+"\n"), CommitArgv(paths, hooks)...); err != nil {
		return "", err
	}
	return g.Head(ctx, dir)
}
