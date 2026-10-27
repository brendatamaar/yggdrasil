package git

import (
	"context"
	"errors"
	"fmt"
	"path/filepath"
	"strings"
)

// Push errors.
var (
	// ErrUnsafeRemote: the remote URL carries credentials or has a form
	// Yggdrasil does not push to. Nothing is sent (invariant 4).
	ErrUnsafeRemote = errors.New("unsafe remote url")
	// ErrDefaultBranch: a proposal never pushes to the repo's default branch.
	ErrDefaultBranch = errors.New("push to the default branch")
	// ErrPushRejected: the remote refused the push. There is no force path.
	ErrPushRejected = errors.New("push rejected by the remote")
)

// MROptions are the GitLab merge-request push options.
type MROptions struct {
	Target      string
	Title       string
	Description string // one line; "" for none
}

// PushRequest is one push. There is no force, delete or refspec field.
type PushRequest struct {
	Remote string
	Branch string
	// ExpectHead is the commit the local branch must point to.
	ExpectHead string
	// MR, when set, adds the merge-request push options.
	MR *MROptions
}

// PushResult is what the remote said (redacted), for finding the MR link.
type PushResult struct {
	Output string
}

// PushFail says why a push failed.
type PushFail string

const (
	// PushNonFastForward: the remote has commits the local branch lacks.
	PushNonFastForward PushFail = "non_fast_forward"
	// PushRefused: a rule on the remote refused it (protected branch, hook).
	PushRefused PushFail = "refused"
	// PushNetwork: the remote could not be reached or needs sign-in.
	PushNetwork PushFail = "network"
)

// PushError is a failed push with the remote's reason (redacted).
type PushError struct {
	Kind   PushFail
	Reason string
	Msg    string
}

func (e *PushError) Error() string {
	return "git push: " + string(e.Kind) + ": " + e.Reason
}

// Unwrap lets errors.Is(err, ErrPushRejected) match a refusal by the remote.
func (e *PushError) Unwrap() error {
	if e.Kind == PushNetwork {
		return nil
	}
	return ErrPushRejected
}

// UserMessage is the plain-language message.
func (e *PushError) UserMessage() string { return e.Msg }

// PushArgv is the argument list of a push (after "git"). It holds the refspec
// "refs/heads/<b>:refs/heads/<b>" and, for a merge request, -o options. It
// never holds a force flag, a "+" refspec, a delete or an empty source.
func PushArgv(req PushRequest, hooksDir string) []string {
	a := []string{"-c", "core.hooksPath=" + hooksDir, "-c", "core.fsmonitor=false", "-c", "protocol.ext.allow=never",
		"push", "--porcelain", "--no-verify"}
	if m := req.MR; m != nil {
		a = append(a, "-o", "merge_request.create", "-o", "merge_request.target="+m.Target,
			"-o", "merge_request.title="+m.Title)
		if m.Description != "" {
			a = append(a, "-o", "merge_request.description="+m.Description)
		}
	}
	ref := "refs/heads/" + req.Branch
	return append(a, "--", req.Remote, ref+":"+ref)
}

// checkBranchName is the last line of defence for a branch name that goes into
// a refspec: nothing that makes a "+", a ":" or a pattern.
func checkBranchName(b string) error {
	bad := b == "" || strings.HasPrefix(b, "-") || strings.HasPrefix(b, "+") || strings.HasPrefix(b, "/") ||
		strings.HasSuffix(b, "/") || strings.HasSuffix(b, ".") || strings.HasSuffix(b, ".lock") ||
		strings.Contains(b, "..") || strings.Contains(b, "//") || strings.Contains(b, "@{") || b == "@" ||
		strings.ContainsAny(b, " \t\r\n\x00:?*[\\^~") || strings.ContainsFunc(b, func(r rune) bool { return r < 0x20 || r == 0x7f })
	if bad {
		return &Error{Op: "check branch", Err: fmt.Errorf("%w: branch", ErrInvalidArg), UserMsg: "The branch name is not valid. Choose another name."}
	}
	return nil
}

func checkOptionText(s string) bool {
	return !strings.ContainsAny(s, "\r\n\x00")
}

// IsLocalPath reports whether remote url is a plain absolute folder path (a
// bare repo on this machine). It holds no credentials and runs no program, but
// the proposals URL check does not know it, so the service asks here.
func IsLocalPath(u string) bool {
	return u != "" && !strings.Contains(u, "::") && !strings.Contains(u, "://") &&
		!strings.ContainsAny(u, "\r\n\x00") && (filepath.IsAbs(u) || strings.HasPrefix(u, `\\`)) &&
		!strings.HasPrefix(u, "-")
}

// checkRemoteURL refuses a URL that carries credentials or has a form that
// runs a program (ext::), as the last line before git gets it. The service
// applies the stricter proposals.CheckRemote first.
func checkRemoteURL(u string) error {
	unsafe := &Error{Op: "push", Err: ErrUnsafeRemote,
		UserMsg: "The remote address has a password or token in it, or is not a normal address. Remove the credentials from the remote URL (use SSH keys or a credential manager), then retry."}
	if u == "" || strings.ContainsAny(u, "\r\n\x00") || strings.Contains(u, "::") || strings.HasPrefix(u, "-") {
		return unsafe
	}
	if IsLocalPath(u) {
		return nil
	}
	if scheme, rest, ok := strings.Cut(u, "://"); ok {
		auth, path, _ := strings.Cut(rest, "/")
		if strings.ContainsAny(path, "?#") || strings.ContainsAny(auth, "?#") {
			return unsafe
		}
		switch strings.ToLower(scheme) {
		case "http", "https":
			if strings.Contains(auth, "@") {
				return unsafe
			}
		case "ssh", "git+ssh":
			if i := strings.LastIndex(auth, "@"); i >= 0 && strings.Contains(auth[:i], ":") {
				return unsafe
			}
		default:
			return unsafe
		}
		return nil
	}
	// scp-like user@host:path
	at, colon := strings.Index(u, "@"), strings.Index(u, ":")
	if at <= 0 || colon <= at+1 || strings.ContainsAny(u[:colon], "/\\") || strings.ContainsAny(u, "?#") {
		return unsafe
	}
	return nil
}

// checkRemoteName accepts [A-Za-z0-9._-]+ not starting with "-", the names the
// proposals parser accepts, so a remote name never forms a refspec or option.
func checkRemoteName(n string) error {
	if err := checkName("remote", n); err != nil {
		return err
	}
	for _, c := range n {
		ok := c >= 'a' && c <= 'z' || c >= 'A' && c <= 'Z' || c >= '0' && c <= '9' || c == '.' || c == '_' || c == '-'
		if !ok {
			return &Error{Op: "check remote", Err: fmt.Errorf("%w: remote", ErrInvalidArg), UserMsg: fmt.Sprintf("The remote name %q is not valid.", n)}
		}
	}
	return nil
}

// CheckRemoteURL applies the URL rules Push applies, so a fake can share them.
func CheckRemoteURL(u string) error { return checkRemoteURL(u) }

// pushURL returns the URL a push would use (pushurl if set, else url).
func (g *Git) pushURL(ctx context.Context, dir, name string) (string, error) {
	out, err := g.run(ctx, dir, "remote url", true, "remote", "get-url", "--push", "--end-of-options", name)
	if err != nil {
		return "", err
	}
	return strings.TrimSpace(out), nil
}

// DefaultBranch returns the repo's default branch as the clone knows it
// (refs/remotes/<remote>/HEAD), or "" when unknown.
func (g *Git) DefaultBranch(ctx context.Context, dir, remote string) (string, error) {
	if err := checkRemoteName(remote); err != nil {
		return "", err
	}
	so, _, code, err := g.exec(ctx, dir, "default branch", nil, "symbolic-ref", "--short", "refs/remotes/"+remote+"/HEAD")
	if err != nil {
		return "", err
	}
	if code != 0 {
		return "", nil
	}
	return strings.TrimPrefix(strings.TrimSpace(so), remote+"/"), nil
}

// IsDefaultBranch reports whether pushing branch is refused as a push to the
// default branch. When the default is unknown, "main" and "master" count.
func (g *Git) IsDefaultBranch(ctx context.Context, dir, remote, branch string) (bool, error) {
	def, err := g.DefaultBranch(ctx, dir, remote)
	if err != nil {
		return false, err
	}
	if def != "" {
		return branch == def, nil
	}
	return branch == "main" || branch == "master", nil
}

// LsRemote returns the commit ID the remote has for branch ("" when none).
func (g *Git) LsRemote(ctx context.Context, dir, remote, branch string) (string, error) {
	if err := checkRemoteName(remote); err != nil {
		return "", err
	}
	if err := checkBranchName(branch); err != nil {
		return "", err
	}
	u, err := g.pushURL(ctx, dir, remote)
	if err != nil {
		return "", err
	}
	if err := checkRemoteURL(u); err != nil {
		return "", err
	}
	out, err := g.run(ctx, dir, "ls-remote", true, "-c", "protocol.ext.allow=never", "ls-remote", "--heads", "--", remote, "refs/heads/"+branch)
	if err != nil {
		return "", err
	}
	for line := range strings.SplitSeq(out, "\n") {
		id, ref, ok := strings.Cut(strings.TrimSpace(line), "\t")
		if ok && ref == "refs/heads/"+branch {
			return id, nil
		}
	}
	return "", nil
}

// Push sends the local branch to the remote as a fast-forward. It refuses a
// remote URL with credentials (nothing is sent), the default branch, and a
// branch whose tip is not req.ExpectHead. Hooks are off. A remote refusal
// comes back as *PushError.
func (g *Git) Push(ctx context.Context, dir string, req PushRequest) (PushResult, error) {
	if err := checkRemoteName(req.Remote); err != nil {
		return PushResult{}, err
	}
	if err := checkBranchName(req.Branch); err != nil {
		return PushResult{}, err
	}
	if m := req.MR; m != nil {
		if err := checkName("target branch", m.Target); err != nil {
			return PushResult{}, err
		}
		if !checkOptionText(m.Target) || !checkOptionText(m.Title) || !checkOptionText(m.Description) || m.Title == "" {
			return PushResult{}, &Error{Op: "push", Err: ErrInvalidArg, UserMsg: "The merge request title or description is not valid. Use one line of text."}
		}
	}
	// Every URL the push could use: the fetch URL and a separate push URL.
	for _, get := range []func(context.Context, string, string) (string, error){g.RemoteURL, g.pushURL} {
		u, err := get(ctx, dir, req.Remote)
		if err != nil {
			return PushResult{}, err
		}
		if err := checkRemoteURL(u); err != nil {
			return PushResult{}, err
		}
	}
	isDef, err := g.IsDefaultBranch(ctx, dir, req.Remote, req.Branch)
	if err != nil {
		return PushResult{}, err
	}
	if isDef {
		return PushResult{}, &Error{Op: "push", Err: ErrDefaultBranch,
			UserMsg: "Yggdrasil does not push to the default branch. Push a task branch and open a merge request instead."}
	}
	if req.ExpectHead != "" {
		tip, err := g.ResolveCommit(ctx, dir, "refs/heads/"+req.Branch)
		if err != nil {
			return PushResult{}, err
		}
		if tip != req.ExpectHead {
			return PushResult{}, &Error{Op: "push", Err: ErrStaleHead, UserMsg: "The branch has changed since this push was proposed. Reject it and ask for a new one."}
		}
	}
	hooks, err := g.noHooks()
	if err != nil {
		return PushResult{}, err
	}
	so, se, code, err := g.exec(ctx, dir, "push", nil, PushArgv(req, hooks)...)
	if err != nil {
		return PushResult{}, err
	}
	out := strings.TrimSpace(se + "\n" + so)
	if code == 0 {
		return PushResult{Output: out}, nil
	}
	return PushResult{}, classifyPush(out, so, se)
}

func classifyPush(all, stdout, stderr string) error {
	low := strings.ToLower(all)
	reason := ""
	for line := range strings.SplitSeq(stdout, "\n") {
		if strings.HasPrefix(line, "!") {
			if _, r, ok := strings.Cut(strings.TrimPrefix(line, "!"), "\t["); ok {
				reason = "[" + strings.TrimSpace(strings.TrimPrefix(r, "\t"))
			}
			break
		}
	}
	if reason == "" {
		reason = lastLines(stderr, 3)
	}
	switch {
	case strings.Contains(low, "non-fast-forward") || strings.Contains(low, "fetch first") || strings.Contains(low, "stale info"):
		return &PushError{Kind: PushNonFastForward, Reason: reason,
			Msg: "The remote has newer commits on this branch, so the push was refused. Yggdrasil never forces a push. Update the branch and propose the push again."}
	case strings.Contains(low, "[remote rejected]") || strings.Contains(low, "[rejected]") || strings.Contains(low, "declined"):
		return &PushError{Kind: PushRefused, Reason: reason,
			Msg: "The remote refused the push (a rule such as a protected branch). Check the remote's message, fix the cause and propose again."}
	}
	return &PushError{Kind: PushNetwork, Reason: reason,
		Msg: classify("push", strings.ToLower(all))}
}

func lastLines(s string, n int) string {
	lines := strings.Split(strings.TrimSpace(s), "\n")
	if len(lines) > n {
		lines = lines[len(lines)-n:]
	}
	return strings.TrimSpace(strings.Join(lines, " | "))
}
