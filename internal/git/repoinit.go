package git

// RepoInitializer and OriginManager for new repos (NR-01, NR-03, NR-04). They
// run through the same safe wrapper as every other git call: platform.Start,
// the clean environment, no credential helper, long paths on, and for commits
// an empty hooks folder. No token is ever an argument, URL or config value.

import (
	"context"
	"crypto/sha256"
	"encoding/hex"
	"errors"
	"fmt"
	"os"
	"regexp"
	"strings"
)

// Errors of the new-repo helpers.
var (
	// ErrNotEmptyDir: InitRepo found something in the folder. Existing folders
	// are never adopted (docs/design/phase-6-newrepos.md).
	ErrNotEmptyDir = errors.New("folder is not an empty directory")
	// ErrNotFirstCommit: the branch already has history, so this is not the
	// first commit of a new repo.
	ErrNotFirstCommit = errors.New("the repo already has commits")
	// ErrOriginExists: origin is already set to another URL.
	ErrOriginExists = errors.New("origin is already set to a different URL")
)

const maxFirstMessage = 64 << 10

var (
	_ RepoInitializer = (*Git)(nil)
	_ OriginManager   = (*Git)(nil)
)

var hexID = regexp.MustCompile(`^[0-9a-f]{40}$`)

// MessageHash is the hash FirstCommit.MessageHash holds for a commit message:
// SHA-256 of the trimmed message and one trailing newline, which is exactly
// what CommitFirst stores (cleanup=verbatim).
func MessageHash(message string) string {
	sum := sha256.Sum256([]byte(strings.TrimSpace(message) + "\n"))
	return hex.EncodeToString(sum[:])
}

// InitRepo runs `git init` with the initial branch in an existing, empty
// directory. No template is copied, so no hook or other file the user's git
// template folder holds lands in the repo.
func (g *Git) InitRepo(ctx context.Context, dir, branch string) error {
	if err := checkName("branch", branch); err != nil {
		return err
	}
	if _, err := g.run(ctx, dir, "check branch", true, "check-ref-format", "refs/heads/"+branch); err != nil {
		return &Error{Op: "init", Err: fmt.Errorf("%w: branch name", ErrInvalidArg),
			UserMsg: fmt.Sprintf("The branch name %q is not valid. Choose another name.", branch)}
	}
	entries, err := os.ReadDir(dir)
	if err != nil {
		return &Error{Op: "init", Err: fmt.Errorf("%w: %w", ErrNotEmptyDir, err),
			UserMsg: "The new repo folder does not exist. Check the folder and try again."}
	}
	if len(entries) != 0 {
		return &Error{Op: "init", Err: ErrNotEmptyDir,
			UserMsg: "The new repo folder is not empty, so Yggdrasil will not touch it. Choose a folder that does not exist yet."}
	}
	hooks, err := g.noHooks()
	if err != nil {
		return err
	}
	_, err = g.execOK(ctx, dir, "init", nil,
		"-c", "core.hooksPath="+hooks, "-c", "init.defaultBranch="+branch,
		"init", "-q", "--template=", "--initial-branch="+branch)
	return err
}

// HasAuthor reports whether git knows both the author and the committer, so a
// commit will not fail for a missing identity.
func (g *Git) HasAuthor(ctx context.Context, dir string) (bool, error) {
	for _, v := range []string{"GIT_AUTHOR_IDENT", "GIT_COMMITTER_IDENT"} {
		_, _, code, err := g.exec(ctx, dir, "author", nil, "-c", "user.useConfigOnly=true", "var", v)
		if err != nil {
			return false, err
		}
		if code != 0 {
			return false, nil
		}
	}
	return true, nil
}

// CommitFirst commits exactly files (literal pathspecs, never "add all") as the
// first commit of the repo. With no author configured it fails with ErrNoAuthor
// before it stages or writes anything. Hooks are off. The message is stored
// verbatim (trimmed, one trailing newline).
func (g *Git) CommitFirst(ctx context.Context, dir string, files []string, message string) (FirstCommit, error) {
	if err := checkPathspecs(files); err != nil {
		return FirstCommit{}, err
	}
	if strings.TrimSpace(message) == "" || strings.ContainsRune(message, 0) || len(message) > maxFirstMessage {
		return FirstCommit{}, &Error{Op: "first commit", Err: ErrInvalidArg, UserMsg: "The first commit message is empty or too long. Write a short message."}
	}
	ok, err := g.HasAuthor(ctx, dir)
	if err != nil {
		return FirstCommit{}, err
	}
	if !ok {
		return FirstCommit{}, &Error{Op: "first commit", Err: ErrNoAuthor,
			UserMsg: `Git does not know who you are. Run: git config --global user.name "Your Name" and git config --global user.email "you@example.com", then retry.`}
	}
	hooks, err := g.noHooks()
	if err != nil {
		return FirstCommit{}, err
	}
	if _, _, code, err := g.exec(ctx, dir, "first commit", nil, "rev-parse", "--verify", "-q", "HEAD"); err != nil {
		return FirstCommit{}, err
	} else if code == 0 {
		return FirstCommit{}, &Error{Op: "first commit", Err: ErrNotFirstCommit,
			UserMsg: "This repo already has a commit, so Yggdrasil will not create a first one."}
	}
	if err := g.Add(ctx, dir, files); err != nil {
		return FirstCommit{}, err
	}
	argv := []string{"-c", "core.hooksPath=" + hooks, "-c", "core.fsmonitor=false", "-c", "user.useConfigOnly=true", "-c", "commit.gpgsign=false", literal,
		"commit", "--no-verify", "--only", "-q", "--cleanup=verbatim", "-F", "-", "--"}
	argv = append(argv, files...)
	if _, err := g.execOK(ctx, dir, "first commit", strings.NewReader(strings.TrimSpace(message)+"\n"), argv...); err != nil {
		return FirstCommit{}, err
	}
	fc, found, err := g.readCommit(ctx, dir, "HEAD")
	if err != nil {
		return FirstCommit{}, err
	}
	if !found {
		return FirstCommit{}, &Error{Op: "first commit", Err: errors.New("no commit after commit"), UserMsg: "Yggdrasil could not read the commit it just made."}
	}
	return fc, nil
}

// ReadFirstCommit reads the tip of branch without writing. found is false when
// the branch has no commit yet. A tip that has a parent is not a first commit:
// it returns ErrNotFirstCommit. Callers compare the returned Tree, MessageHash
// and Author with what they expect (MessageHash(message), TreeHash).
func (g *Git) ReadFirstCommit(ctx context.Context, dir, branch string) (FirstCommit, bool, error) {
	if err := checkName("branch", branch); err != nil {
		return FirstCommit{}, false, err
	}
	return g.readCommit(ctx, dir, "refs/heads/"+branch)
}

func (g *Git) readCommit(ctx context.Context, dir, ref string) (FirstCommit, bool, error) {
	_, _, code, err := g.exec(ctx, dir, "read commit", nil, "-c", "core.fsmonitor=false", "rev-parse", "--verify", "-q", "--end-of-options", ref+"^{commit}")
	if err != nil {
		return FirstCommit{}, false, err
	}
	if code != 0 {
		return FirstCommit{}, false, nil
	}
	id, err := g.run(ctx, dir, "read commit", true, "rev-parse", "--verify", "--end-of-options", ref+"^{commit}")
	if err != nil {
		return FirstCommit{}, false, err
	}
	id = strings.TrimSpace(id)
	if !hexID.MatchString(id) {
		return FirstCommit{}, false, &Error{Op: "read commit", Err: errors.New("unexpected commit id"), UserMsg: "Yggdrasil could not read the commit."}
	}
	raw, _, code, err := g.execWith(ctx, dir, "read commit", nil, false, "cat-file", "commit", id)
	if err != nil {
		return FirstCommit{}, false, err
	}
	if code != 0 {
		return FirstCommit{}, false, &Error{Op: "read commit", Code: code, Err: errors.New("cat-file failed"), UserMsg: "Yggdrasil could not read the commit."}
	}
	head, msg, _ := strings.Cut(raw, "\n\n")
	fc := FirstCommit{Commit: id}
	for line := range strings.SplitSeq(head, "\n") {
		key, val, _ := strings.Cut(line, " ")
		switch key {
		case "tree":
			fc.Tree = val
		case "parent":
			return FirstCommit{}, false, &Error{Op: "read commit", Err: ErrNotFirstCommit,
				UserMsg: "This repo has more than its first commit, so it can't be checked as new."}
		case "author":
			// "Name <email> 1700000000 +0000": keep identity, drop the time.
			if i := strings.LastIndex(val, ">"); i >= 0 {
				fc.Author = val[:i+1]
			}
		}
	}
	if !hexID.MatchString(fc.Tree) || fc.Author == "" {
		return FirstCommit{}, false, &Error{Op: "read commit", Err: errors.New("unexpected commit format"), UserMsg: "Yggdrasil could not read the commit."}
	}
	sum := sha256.Sum256([]byte(msg))
	fc.MessageHash = hex.EncodeToString(sum[:])
	return fc, true, nil
}

// Origin returns the URL of the origin remote. exists is false when there is
// none. A stored URL that carries credentials is never returned: it is an error,
// so the secret is not echoed.
func (g *Git) Origin(ctx context.Context, dir string) (string, bool, error) {
	raw, err := g.RemoteURL(ctx, dir, "origin")
	if errors.Is(err, ErrNoRemote) {
		return "", false, nil
	}
	if err != nil {
		return "", false, err
	}
	canon, err := ValidateRemoteURL(raw)
	if err != nil {
		return "", true, &Error{Op: "origin", Err: err,
			UserMsg: "The origin of this repo is not a plain https address or carries credentials. Yggdrasil will not use it."}
	}
	return canon, true, nil
}

// AddOrigin adds origin only when it is absent, or does nothing when it is
// already exactly url. It never replaces an origin. The URL must be a
// credential-free https URL.
func (g *Git) AddOrigin(ctx context.Context, dir, rawURL string) error {
	canon, err := ValidateRemoteURL(rawURL)
	if err != nil {
		return &Error{Op: "add origin", Err: err,
			UserMsg: "The remote address is not valid. Use a plain https address without a user name, password or token."}
	}
	cur, exists, err := g.Origin(ctx, dir)
	if err != nil {
		return err
	}
	if exists {
		if cur == canon {
			return nil
		}
		return &Error{Op: "add origin", Err: ErrOriginExists,
			UserMsg: "This repo already has an origin with a different address. Yggdrasil will not replace it."}
	}
	_, err = g.execOK(ctx, dir, "add origin", nil, "remote", "add", "origin", canon)
	return err
}
