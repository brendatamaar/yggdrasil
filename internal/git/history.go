package git

import (
	"context"
	"errors"
	"fmt"
	"strconv"
	"strings"
	"time"
)

// Branch is a local or remote-tracking branch (WS-05).
type Branch struct {
	Name     string `json:"name"` // "main" or "origin/main"
	Remote   bool   `json:"remote"`
	Current  bool   `json:"current"`
	Upstream string `json:"upstream,omitempty"`
	OID      string `json:"oid"`
}

// Branches lists local and remote-tracking branches. When HEAD is detached no
// branch is Current.
func (g *Git) Branches(ctx context.Context, dir string) ([]Branch, error) {
	const format = "%(refname)%00%(objectname)%00%(upstream:short)%00%(HEAD)"
	out, err := g.run(ctx, dir, "branch list", true, "for-each-ref", "--format="+format, "refs/heads", "refs/remotes")
	if err != nil {
		return nil, err
	}
	var res []Branch
	for line := range strings.SplitSeq(out, "\n") {
		f := strings.Split(strings.TrimSuffix(line, "\r"), "\x00")
		if len(f) != 4 {
			continue
		}
		b := Branch{OID: f[1], Upstream: f[2], Current: f[3] == "*"}
		if n, ok := strings.CutPrefix(f[0], "refs/heads/"); ok {
			b.Name = n
		} else if n, ok := strings.CutPrefix(f[0], "refs/remotes/"); ok {
			if strings.HasSuffix(n, "/HEAD") {
				continue // origin/HEAD is an alias
			}
			b.Name, b.Remote = n, true
		} else {
			continue
		}
		res = append(res, b)
	}
	return res, nil
}

// Commit is one commit of the history.
type Commit struct {
	Hash    string    `json:"hash"`
	Short   string    `json:"short"`
	Author  string    `json:"author"`
	Email   string    `json:"email"`
	Date    time.Time `json:"date"`
	Parents []string  `json:"parents"`
	Subject string    `json:"subject"`
}

// commitFormat has fields split by US (0x1f). Records end with NUL (-z).
const commitFormat = "%H%x1f%h%x1f%an%x1f%ae%x1f%aI%x1f%P%x1f%s"

// parseCommit also returns the optional 8th field (the body, for Show).
func parseCommit(rec string) (c Commit, body string, err error) {
	f := strings.SplitN(rec, "\x1f", 8)
	if len(f) < 7 {
		return Commit{}, "", fmt.Errorf("bad commit record %q", rec)
	}
	d, err := time.Parse(time.RFC3339, f[4])
	if err != nil {
		return Commit{}, "", fmt.Errorf("bad commit date %q: %w", f[4], err)
	}
	if len(f) == 8 {
		body = strings.TrimSpace(f[7])
	}
	return Commit{Hash: f[0], Short: f[1], Author: f[2], Email: f[3], Date: d,
		Parents: strings.Fields(f[5]), Subject: f[6]}, body, nil
}

// Log returns up to n commits from HEAD, newest first. A repo without commits
// gives an empty list.
func (g *Git) Log(ctx context.Context, dir string, n int) ([]Commit, error) {
	if n < 1 {
		return nil, nil
	}
	out, err := g.run(ctx, dir, "log", true, "log", "-z", "-n", strconv.Itoa(n), "--format="+commitFormat)
	if err != nil {
		var ge *Error
		if errors.As(err, &ge) && strings.Contains(ge.Stderr, "does not have any commits") {
			return nil, nil
		}
		return nil, err
	}
	var res []Commit
	for rec := range strings.SplitSeq(out, "\x00") {
		if strings.TrimSpace(rec) == "" {
			continue
		}
		c, _, err := parseCommit(strings.TrimLeft(rec, "\n"))
		if err != nil {
			return nil, &Error{Op: "log", Err: err, UserMsg: "Yggdrasil could not read the output of git log. Update git and try again."}
		}
		res = append(res, c)
	}
	return res, nil
}

// CommitFile is one file touched by a commit. Status is git's letter (A, M,
// D, R, C, T).
type CommitFile struct {
	Path     string `json:"path"`
	OrigPath string `json:"origPath,omitempty"`
	Status   string `json:"status"`
}

// CommitDetail is a commit with its full message and changed files.
type CommitDetail struct {
	Commit
	Body  string       `json:"body"`
	Files []CommitFile `json:"files"`
}

// Show returns the detail of rev. A merge commit lists no files.
func (g *Git) Show(ctx context.Context, dir, rev string) (CommitDetail, error) {
	if err := checkName("revision", rev); err != nil {
		return CommitDetail{}, err
	}
	out, err := g.run(ctx, dir, "show", true, "log", "-1", "-z", "--format="+commitFormat+"%x1f%b", "--end-of-options", rev)
	if err != nil {
		return CommitDetail{}, err
	}
	rec := strings.TrimRight(out, "\x00")
	if rec == "" {
		return CommitDetail{}, &Error{Op: "show", Err: fmt.Errorf("%w: %q", ErrInvalidArg, rev),
			UserMsg: fmt.Sprintf("Commit %q was not found. Check the name.", rev)}
	}
	c, body, err := parseCommit(rec)
	if err != nil {
		return CommitDetail{}, &Error{Op: "show", Err: err, UserMsg: "Yggdrasil could not read the output of git show. Update git and try again."}
	}
	d := CommitDetail{Commit: c, Body: body}
	// The full hash is safe to pass as an argument.
	files, err := g.run(ctx, dir, "show", true, "diff-tree", "-r", "-z", "--root", "--no-commit-id", "--name-status", "-M", c.Hash)
	if err != nil {
		return CommitDetail{}, err
	}
	toks := strings.Split(strings.TrimRight(files, "\x00"), "\x00")
	for i := 0; i < len(toks) && toks[i] != ""; i++ {
		st := toks[i]
		cf := CommitFile{Status: st[:1]}
		if (st[0] == 'R' || st[0] == 'C') && i+2 < len(toks) {
			cf.OrigPath, cf.Path = toks[i+1], toks[i+2]
			i += 2
		} else if i+1 < len(toks) {
			cf.Path = toks[i+1]
			i++
		}
		d.Files = append(d.Files, cf)
	}
	return d, nil
}

// DiffMode selects what Diff compares.
type DiffMode int

const (
	// DiffWorktree compares the working tree with the index (unstaged changes).
	DiffWorktree DiffMode = iota
	// DiffStaged compares the index with HEAD.
	DiffStaged
	// DiffRefs compares From with To.
	DiffRefs
)

// DiffOptions describe a Diff call. Path limits it to one file or folder.
type DiffOptions struct {
	Mode     DiffMode
	From, To string
	Path     string
}

// Diff returns unified diff text. Untracked files are not part of it. Bytes
// are returned as git printed them, so CRLF stays CRLF.
func (g *Git) Diff(ctx context.Context, dir string, o DiffOptions) (string, error) {
	args := []string{"diff", "--no-color", "--no-ext-diff", "--no-textconv"}
	switch o.Mode {
	case DiffWorktree:
	case DiffStaged:
		args = append(args, "--cached")
	case DiffRefs:
		if err := checkName("revision", o.From); err != nil {
			return "", err
		}
		args = append(args, o.From)
		if o.To != "" {
			if err := checkName("revision", o.To); err != nil {
				return "", err
			}
			args = append(args, o.To)
		}
	default:
		return "", &Error{Op: "diff", Err: ErrInvalidArg, UserMsg: "Unknown diff type."}
	}
	args = append(args, "--")
	if o.Path != "" {
		args = append(args, o.Path)
	}
	return g.run(ctx, dir, "diff", true, args...)
}
