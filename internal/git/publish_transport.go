package git

// The in-process smart-HTTP publish transport (NR-04; docs/design/
// phase-6-newrepos.md "Publish transport decision"). It uses go-git's pure-Go
// transport pieces and never starts a process, so a token never reaches a child's
// environment, argv or config (invariants 3 and 4). The credential exists only
// inside the Authorizer, which sets the Authorization header of each request.
//
// The client is built per call from go-git's HTTP transport object, never
// through go-git's global protocol registry, and only https URLs without userinfo
// are accepted. Redirects to another authority are refused before they are sent.

import (
	"bytes"
	"context"
	"errors"
	"fmt"
	"io"
	"net/http"
	"net/url"
	"strings"
	"time"

	gogit "github.com/go-git/go-git/v5"
	"github.com/go-git/go-git/v5/plumbing"
	"github.com/go-git/go-git/v5/plumbing/filemode"
	"github.com/go-git/go-git/v5/plumbing/format/packfile"
	"github.com/go-git/go-git/v5/plumbing/object"
	"github.com/go-git/go-git/v5/plumbing/protocol/packp"
	"github.com/go-git/go-git/v5/plumbing/revlist"
	"github.com/go-git/go-git/v5/plumbing/transport"
	gohttp "github.com/go-git/go-git/v5/plumbing/transport/http"

	"github.com/brendatamaar/yggdrasil/internal/safety"
)

// DefaultPublishTimeout bounds one transport call made with the default client.
const DefaultPublishTimeout = 5 * time.Minute

// ClientFactory returns the HTTP client the transport uses for one call. Tests
// pass the client of an httptest TLS server. The transport copies the client
// and replaces its redirect policy, cookie jar and round tripper wrapper.
type ClientFactory func() *http.Client

// DefaultClientFactory is a client with the standard library transport and a
// timeout. It follows no cookies.
func DefaultClientFactory() *http.Client {
	tr, ok := http.DefaultTransport.(*http.Transport)
	if !ok {
		return &http.Client{Timeout: DefaultPublishTimeout}
	}
	return &http.Client{Transport: tr.Clone(), Timeout: DefaultPublishTimeout}
}

// GoGitTransport is the PublishTransport backed by go-git. It is safe for
// concurrent use; it holds no per-call state.
type GoGitTransport struct {
	authz   Authorizer
	factory ClientFactory
	redact  safety.Redactor
}

var _ PublishTransport = (*GoGitTransport)(nil)

// NewGoGitTransport returns the in-process publish transport. authz is
// required; factory may be nil (DefaultClientFactory).
func NewGoGitTransport(authz Authorizer, factory ClientFactory) (*GoGitTransport, error) {
	if authz == nil {
		return nil, errors.New("publish transport needs an authorizer")
	}
	if factory == nil {
		factory = DefaultClientFactory
	}
	return &GoGitTransport{authz: authz, factory: factory, redact: safety.NewRedactor()}, nil
}

// authority is scheme, lower-case host and effective port of a URL.
func authority(u *url.URL) string {
	port := u.Port()
	if port == "" {
		port = "443"
	}
	return strings.ToLower(u.Scheme) + "://" + strings.ToLower(u.Hostname()) + ":" + port
}

// authRT adds credentials through the Authorizer to every request it sends, and
// only to requests for the one authority it was built for. A failure to
// authorize means the request is not sent.
type authRT struct {
	inner http.RoundTripper
	authz Authorizer
	only  string
	// prefix is the validated repository path (no trailing slash). The credential
	// is for that one repository: a same-host request for another path is refused.
	prefix string
}

// inRepoPath reports whether path is the repository's own path or below it.
func inRepoPath(prefix, path string) bool {
	if path == prefix || strings.HasPrefix(path, prefix+"/") {
		return true
	}
	// A URL given without ".git" is served below "<path>.git/" by some hosts.
	return !strings.HasSuffix(prefix, ".git") && strings.HasPrefix(path, prefix+".git/")
}

func (a *authRT) RoundTrip(req *http.Request) (*http.Response, error) {
	if authority(req.URL) != a.only {
		return nil, errors.New("refusing to send a request to another host")
	}
	if !inRepoPath(a.prefix, req.URL.Path) {
		return nil, errors.New("refusing to send a request for another repository")
	}
	out := req.Clone(req.Context())
	// Anything the credential holder sets other than Authorization is refused.
	before := out.Header.Clone()
	if err := a.authz.Authorize(req.Context(), out); err != nil {
		return nil, fmt.Errorf("authorize request: %w", err)
	}
	for k, v := range out.Header {
		if http.CanonicalHeaderKey(k) == "Authorization" {
			continue
		}
		if ov, ok := before[k]; !ok || strings.Join(ov, "\x00") != strings.Join(v, "\x00") {
			return nil, errors.New("the authorizer may only set the Authorization header")
		}
	}
	for k := range before {
		if _, ok := out.Header[k]; !ok {
			return nil, errors.New("the authorizer may only set the Authorization header")
		}
	}
	return a.inner.RoundTrip(out)
}

// session is a per-call client bound to one validated URL.
type pubCall struct {
	t    *GoGitTransport
	ep   *transport.Endpoint
	cli  transport.Transport
	only string
}

func (t *GoGitTransport) newCall(rawURL string) (*pubCall, error) {
	canon, err := ValidateRemoteURL(rawURL)
	if err != nil {
		return nil, &Error{Op: "publish", Err: err,
			UserMsg: "The remote address is not valid. Use a plain https address without a user name, password or token."}
	}
	u, err := url.Parse(canon)
	if err != nil {
		return nil, &Error{Op: "publish", Err: ErrBadRemoteURL, UserMsg: "The remote address is not valid."}
	}
	ep, err := transport.NewEndpoint(canon)
	if err != nil || ep.User != "" || ep.Password != "" {
		return nil, &Error{Op: "publish", Err: ErrBadRemoteURL, UserMsg: "The remote address is not valid."}
	}
	only := authority(u)
	prefix := strings.TrimRight(u.Path, "/")
	base := t.factory()
	if base == nil {
		base = DefaultClientFactory()
	}
	inner := base.Transport
	if inner == nil {
		inner = http.DefaultTransport
	}
	hc := &http.Client{
		Transport: &authRT{inner: inner, authz: t.authz, only: only, prefix: prefix},
		Timeout:   base.Timeout,
		// No cookie jar: nothing but the Authorizer's header carries identity.
		CheckRedirect: func(req *http.Request, via []*http.Request) error {
			if authority(req.URL) != only {
				return errors.New("the remote redirected to another host; Yggdrasil does not follow it")
			}
			if !inRepoPath(prefix, req.URL.Path) {
				return errors.New("the remote redirected to another repository; Yggdrasil does not follow it")
			}
			return nil
		},
	}
	return &pubCall{t: t, ep: ep, cli: gohttp.NewClientWithOptions(hc, &gohttp.ClientOptions{RedirectPolicy: gohttp.FollowInitialRedirects}), only: only}, nil
}

// failure turns a transport error into an *Error with a plain message. The
// text is redacted; request headers are never part of it.
func (t *GoGitTransport) failure(op string, err error) error {
	msg := "Yggdrasil could not reach the git host. Check the network and try again."
	switch {
	case errors.Is(err, transport.ErrAuthenticationRequired):
		msg = "The git host did not accept the sign-in. Reconnect the host account and try again."
	case errors.Is(err, transport.ErrAuthorizationFailed):
		msg = "The git host account is not allowed to publish to this repository."
	case errors.Is(err, transport.ErrRepositoryNotFound):
		msg = "The git host has no such repository, or the account cannot see it."
	case errors.Is(err, context.Canceled), errors.Is(err, context.DeadlineExceeded):
		msg = "The operation was cancelled or timed out."
	}
	return &Error{Op: op, Err: errors.New(t.redact.Redact(err.Error())), UserMsg: msg}
}

func definiteHTTP(err error) bool {
	return errors.Is(err, transport.ErrAuthenticationRequired) ||
		errors.Is(err, transport.ErrAuthorizationFailed) ||
		errors.Is(err, transport.ErrRepositoryNotFound)
}

func checkBranch(branch string) (plumbing.ReferenceName, error) {
	if err := checkName("branch", branch); err != nil {
		return "", err
	}
	name := plumbing.NewBranchReferenceName(branch)
	if err := name.Validate(); err != nil || strings.ContainsAny(branch, " ~^:?*[\\\x7f") || strings.Contains(branch, "..") || strings.Contains(branch, "@{") {
		return "", &Error{Op: "publish", Err: fmt.Errorf("%w: branch", ErrInvalidArg),
			UserMsg: fmt.Sprintf("The branch name %q is not valid.", branch)}
	}
	return name, nil
}

// RemoteBranch lists the remote refs and reports the branch. It sends no object
// and writes nothing. A remote with no commits at all reports the branch absent.
func (t *GoGitTransport) RemoteBranch(ctx context.Context, req PublishRequest) (RemoteRef, error) {
	name, err := checkBranch(req.Branch)
	if err != nil {
		return RemoteRef{}, err
	}
	call, err := t.newCall(req.URL)
	if err != nil {
		return RemoteRef{}, err
	}
	sess, err := call.cli.NewUploadPackSession(call.ep, nil)
	if err != nil {
		return RemoteRef{}, t.failure("remote branch", err)
	}
	defer func() { _ = sess.Close() }()
	ar, err := sess.AdvertisedReferencesContext(ctx)
	if errors.Is(err, transport.ErrEmptyRemoteRepository) {
		return RemoteRef{}, nil
	}
	if err != nil {
		return RemoteRef{}, t.failure("remote branch", err)
	}
	if h, ok := ar.References[name.String()]; ok {
		return RemoteRef{Exists: true, Head: h.String()}, nil
	}
	return RemoteRef{}, nil
}

// PublishBranch pushes refs/heads/<branch>:refs/heads/<branch> without force
// and never deletes. See PublishTransport for the outcomes.
func (t *GoGitTransport) PublishBranch(ctx context.Context, req PublishRequest) (PublishResult, error) {
	name, err := checkBranch(req.Branch)
	if err != nil {
		return PublishResult{}, err
	}
	if !hexID.MatchString(req.ExpectedHead) {
		return PublishResult{}, &Error{Op: "publish", Err: fmt.Errorf("%w: expected head", ErrInvalidArg),
			UserMsg: "The commit to publish is missing. Start the publish again."}
	}
	call, err := t.newCall(req.URL)
	if err != nil {
		return PublishResult{}, err
	}
	repo, err := gogit.PlainOpen(req.Dir)
	if errors.Is(err, gogit.ErrUnknownExtension) || errors.Is(err, gogit.ErrUnsupportedExtensionRepositoryFormatVersion) ||
		errors.Is(err, gogit.ErrUnsupportedRepositoryFormatVersion) {
		return PublishResult{}, &Error{Op: "publish", Err: fmt.Errorf("%w: %s", ErrUnsupportedFormat, err.Error()),
			UserMsg: "This repo uses a format Yggdrasil cannot publish from here. You can publish it yourself with git."}
	}
	if err != nil {
		return PublishResult{}, &Error{Op: "publish", Err: fmt.Errorf("open repo: %w", err),
			UserMsg: "Yggdrasil could not open the repo to publish it."}
	}
	if err := checkPublishable(repo); err != nil {
		return PublishResult{}, err
	}
	local, err := repo.Reference(name, true)
	if err != nil {
		return PublishResult{}, &Error{Op: "publish", Err: fmt.Errorf("local branch: %w", err),
			UserMsg: fmt.Sprintf("The branch %q does not exist in the repo.", req.Branch)}
	}
	if local.Hash().String() != req.ExpectedHead {
		return PublishResult{}, &Error{Op: "publish", Err: ErrStaleHead,
			UserMsg: "The branch has changed since this publish was confirmed. Start the publish again."}
	}
	if err := checkTreeFormat(repo, local.Hash()); err != nil {
		return PublishResult{}, err
	}

	sess, err := call.cli.NewReceivePackSession(call.ep, nil)
	if err != nil {
		return PublishResult{}, t.failure("publish", err)
	}
	defer func() { _ = sess.Close() }()
	ar, err := sess.AdvertisedReferencesContext(ctx)
	if errors.Is(err, transport.ErrEmptyRemoteRepository) {
		return PublishResult{}, t.failure("publish", err)
	}
	if err != nil {
		// Discovery only reads: nothing was sent, so this is a definite failure.
		return PublishResult{}, t.failure("publish", err)
	}
	if remote, ok := ar.References[name.String()]; ok {
		if remote == local.Hash() {
			return PublishResult{Head: remote.String()}, nil
		}
		return PublishResult{}, &Error{Op: "publish", Err: fmt.Errorf("%w: %s", ErrRemoteDiverged, name.Short()),
			UserMsg: "The remote branch already has different commits. Yggdrasil will not overwrite them."}
	}

	// Objects the remote does not have: everything reachable from the head
	// except what its advertised refs already reach (and we also hold).
	var haves []plumbing.Hash
	for _, h := range ar.References {
		if repo.Storer.HasEncodedObject(h) == nil {
			haves = append(haves, h)
		}
	}
	hashes, err := revlist.Objects(repo.Storer, []plumbing.Hash{local.Hash()}, haves)
	if err != nil {
		return PublishResult{}, &Error{Op: "publish", Err: fmt.Errorf("list objects: %w", err),
			UserMsg: "Yggdrasil could not read the repo's commits to publish them."}
	}
	cfg, err := repo.Storer.Config()
	if err != nil {
		return PublishResult{}, &Error{Op: "publish", Err: fmt.Errorf("read config: %w", err), UserMsg: "Yggdrasil could not read the repo's settings."}
	}
	var pack bytes.Buffer
	if _, err := packfile.NewEncoder(&pack, repo.Storer, !ar.Capabilities.Supports("ofs-delta")).Encode(hashes, cfg.Pack.Window); err != nil {
		return PublishResult{}, &Error{Op: "publish", Err: fmt.Errorf("pack objects: %w", err), UserMsg: "Yggdrasil could not pack the repo's commits."}
	}

	upd := packp.NewReferenceUpdateRequestFromCapabilities(ar.Capabilities)
	// Exactly one create command: the single explicit refspec, no force, no delete.
	upd.Commands = []*packp.Command{{Name: name, Old: plumbing.ZeroHash, New: local.Hash()}}
	upd.Packfile = io.NopCloser(&pack)

	report, err := sess.ReceivePack(ctx, upd)
	if err != nil {
		if report != nil {
			// The remote answered and refused the update: definite, nothing changed.
			return PublishResult{}, &Error{Op: "publish", Err: errors.New(t.redact.Redact(err.Error())),
				UserMsg: "The git host refused the publish. Check the branch rules and permissions, then try again."}
		}
		if definiteHTTP(err) {
			return PublishResult{}, t.failure("publish", err)
		}
		// The request may have been applied: never retry blindly.
		return PublishResult{}, &Error{Op: "publish", Err: fmt.Errorf("%w: %s", ErrPublishUnknown, t.redact.Redact(err.Error())),
			UserMsg: "Yggdrasil lost contact with the git host while publishing. It will check what the host has before doing anything else."}
	}
	if report == nil {
		return PublishResult{}, &Error{Op: "publish", Err: fmt.Errorf("%w: no status from the host", ErrPublishUnknown),
			UserMsg: "The git host did not say whether the publish worked. Yggdrasil will check what the host has."}
	}
	return PublishResult{Head: local.Hash().String()}, nil
}

// checkPublishable refuses repositories the in-process transport cannot publish
// safely: another object format, other storage extensions, shallow history.
func checkPublishable(repo *gogit.Repository) error {
	refuse := func(why string) error {
		return &Error{Op: "publish", Err: fmt.Errorf("%w: %s", ErrUnsupportedFormat, why),
			UserMsg: "This repo uses a format Yggdrasil cannot publish from here. You can publish it yourself with git."}
	}
	cfg, err := repo.Storer.Config()
	if err != nil {
		return refuse("unreadable config")
	}
	if ext := cfg.Raw.Section("extensions"); ext != nil {
		for _, o := range ext.Options {
			if strings.EqualFold(o.Key, "objectformat") && strings.EqualFold(o.Value, "sha1") {
				continue
			}
			return refuse("extension " + o.Key)
		}
	}
	if v := cfg.Raw.Section("core").Option("repositoryformatversion"); v != "" && v != "0" && v != "1" {
		return refuse("repository format version")
	}
	shallow, err := repo.Storer.Shallow()
	if err != nil || len(shallow) != 0 {
		return refuse("shallow history")
	}
	if cfg.Raw.HasSection("filter") {
		return refuse("content filters")
	}
	return nil
}

// checkTreeFormat refuses a head whose tree holds a submodule link or asks for
// a content filter in .gitattributes, which the transport cannot publish.
func checkTreeFormat(repo *gogit.Repository, head plumbing.Hash) error {
	refuse := func(why string) error {
		return &Error{Op: "publish", Err: fmt.Errorf("%w: %s", ErrUnsupportedFormat, why),
			UserMsg: "This repo uses submodules or content filters that Yggdrasil cannot publish. You can publish it yourself with git."}
	}
	commit, err := repo.CommitObject(head)
	if err != nil {
		return &Error{Op: "publish", Err: fmt.Errorf("read head: %w", err), UserMsg: "Yggdrasil could not read the commit to publish."}
	}
	tree, err := commit.Tree()
	if err != nil {
		return &Error{Op: "publish", Err: fmt.Errorf("read tree: %w", err), UserMsg: "Yggdrasil could not read the commit to publish."}
	}
	w := object.NewTreeWalker(tree, true, nil)
	defer w.Close()
	for {
		name, entry, err := w.Next()
		if errors.Is(err, io.EOF) {
			return nil
		}
		if err != nil {
			return &Error{Op: "publish", Err: fmt.Errorf("walk tree: %w", err), UserMsg: "Yggdrasil could not read the commit to publish."}
		}
		if entry.Mode == filemode.Submodule {
			return refuse("submodule " + name)
		}
		if strings.HasSuffix(name, ".gitattributes") && entry.Mode.IsFile() {
			f, err := tree.File(name)
			if err != nil {
				return refuse("unreadable " + name)
			}
			text, err := f.Contents()
			if err != nil {
				return refuse("unreadable " + name)
			}
			if strings.Contains(text, "filter=") {
				return refuse("filter in " + name)
			}
		}
	}
}
