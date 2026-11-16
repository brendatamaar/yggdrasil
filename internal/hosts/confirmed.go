package hosts

import (
	"bytes"
	"context"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"net/http"
	"strconv"
	"strings"

	"github.com/brendatamaar/yggdrasil/internal/core"
	corehosts "github.com/brendatamaar/yggdrasil/internal/core/hosts"
	corenewrepos "github.com/brendatamaar/yggdrasil/internal/core/newrepos"
	coreproposals "github.com/brendatamaar/yggdrasil/internal/core/proposals"
)

// ConfirmedHosts is an unwired capability for the confirmed proposal executor.
// It must never be injected into HTTP, MCP or agent dispatch. Durable intent
// and receipts belong to that executor; these adapters never retry a POST.
type ConfirmedHosts struct {
	service   *Service
	resolver  ContextResolver
	getter    *Getter
	transport http.RoundTripper
}

func NewConfirmedHosts(s *Service, resolver ContextResolver, base *http.Client) *ConfirmedHosts {
	rt := http.DefaultTransport
	if base != nil && base.Transport != nil {
		rt = base.Transport
	}
	return &ConfirmedHosts{service: s, resolver: resolver, getter: NewGetter(base, nil), transport: rt}
}

var _ CommentHost = (*ConfirmedHosts)(nil)
var _ RepoHost = (*ConfirmedHosts)(nil)

func (h *ConfirmedHosts) instance(id corehosts.InstanceID) (corehosts.Instance, string, error) {
	h.service.mu.Lock()
	defer h.service.mu.Unlock()
	for _, in := range h.service.cfg.CoreInstances() {
		if in.ID == id {
			t := h.service.cfg.Token(id)
			if t == "" {
				return in, "", NeedToken()
			}
			return in, t, nil
		}
	}
	return corehosts.Instance{}, "", ErrContextScope
}
func hostAPIRoot(in corehosts.Instance) string {
	if in.Kind == corehosts.KindGitLab {
		return in.Base + "/api/v4"
	}
	if in.ID == "github.com" {
		return "https://api.github.com"
	}
	return in.Base + "/api/v3"
}

// resolveFailure maps an error of the scope resolver. Only a real scope error
// (the repo is not tracked, linked or owned) is ErrContextScope; anything else,
// a database failure say, keeps its kind so it is not shown as "not tracked".
// Either way nothing is written: the caller stops before any POST.
func resolveFailure(repo core.RepoID, err error) error {
	if errors.Is(err, ErrContextScope) {
		return ErrContextScope
	}
	return fmt.Errorf("resolve host scope for %s: %w", repo, err)
}

func (h *ConfirmedHosts) commentTarget(ctx context.Context, repo core.RepoID, mr int, head string) (ContextTarget, corehosts.Instance, string, error) {
	key := corehosts.ContextKey{Repo: repo, Kind: corehosts.ContextMRDiff, ObjectID: strconv.Itoa(mr), Head: head}
	// MRHead resolves scope before the head is known: the head is what it learns
	// from the host. That lookup is scope-only and cached nowhere, so only the
	// repo and object are shape-checked; every call with a head validates it all.
	shape := key
	if head == "" {
		shape.Head = strings.Repeat("0", 40)
	}
	if shape.Validate() != nil || h.resolver == nil {
		return ContextTarget{}, corehosts.Instance{}, "", ErrContextScope
	}
	target, err := h.resolver.ResolveContext(ctx, key)
	if err != nil {
		return target, corehosts.Instance{}, "", resolveFailure(repo, err)
	}
	if !validContextProject(target.Project) {
		return target, corehosts.Instance{}, "", ErrContextScope
	}
	inst, token, err := h.instance(target.Instance)
	if err == nil && inst.Kind != corehosts.KindGitHub && inst.Kind != corehosts.KindGitLab {
		err = ErrContextUnsupported
	}
	return target, inst, token, err
}
func commentPath(inst corehosts.Instance, p corehosts.ProjectPath, mr int) string {
	a := ContextAPI{Root: hostAPIRoot(inst), GitLab: inst.Kind == corehosts.KindGitLab}
	path := a.Root + a.project(p)
	if a.GitLab {
		return path + "/merge_requests/" + strconv.Itoa(mr)
	}
	return path + "/issues/" + strconv.Itoa(mr)
}
func (h *ConfirmedHosts) MRHead(ctx context.Context, repo core.RepoID, mr int) (string, error) {
	target, inst, token, err := h.commentTarget(ctx, repo, mr, "")
	if err != nil {
		return "", err
	}
	a := ContextAPI{Getter: h.getter, Root: hostAPIRoot(inst), GitLab: inst.Kind == corehosts.KindGitLab}
	path := a.Root + a.project(target.Project) + "/pulls/" + strconv.Itoa(mr)
	if a.GitLab {
		path = a.Root + a.project(target.Project) + "/merge_requests/" + strconv.Itoa(mr)
	}
	ctx, cancel := context.WithTimeout(ctx, corehosts.ContextRequestTimeout)
	defer cancel()
	auth := AuthBearer
	if a.GitLab {
		auth = AuthPrivateToken
	}
	resp, err := h.getter.GetBounded(ctx, path, token, auth, corehosts.ContextMaxResponseBytes)
	if err != nil {
		return "", err
	}
	var w contextMRWire
	if json.Unmarshal(resp.Body, &w) != nil {
		return "", errors.New("invalid merge request")
	}
	head := w.Head.SHA
	n := w.Number
	if a.GitLab {
		head = w.DiffRefs.Head
		n = w.IID
	}
	key := corehosts.ContextKey{Repo: repo, Kind: corehosts.ContextMRDiff, ObjectID: strconv.Itoa(mr), Head: head}
	if n != mr || head == "" || key.Validate() != nil {
		return "", ErrContextScope
	}
	return head, nil
}
func commentMarker(marker, head string) string {
	return "<!-- ygg:" + marker + " head:" + head + " -->"
}

func (h *ConfirmedHosts) PostReviewComment(ctx context.Context, repo core.RepoID, mr int, head, body, marker string) (string, error) {
	ctx, cancel := context.WithTimeout(ctx, corehosts.ContextRequestTimeout)
	defer cancel()
	spec := coreproposals.ReviewComment{Repo: repo, MRNumber: mr, ExpectedHead: head, Body: body, Marker: marker}
	if err := spec.Validate(); err != nil {
		return "", err
	}
	target, inst, token, err := h.commentTarget(ctx, repo, mr, head)
	if err != nil {
		return "", err
	}
	actual, err := h.MRHead(ctx, repo, mr)
	if err != nil {
		return "", err
	}
	if actual != head {
		return "", ErrContextScope
	}
	fresh, err := h.resolver.ResolveContext(ctx, corehosts.ContextKey{Repo: repo, Kind: corehosts.ContextMRDiff, ObjectID: strconv.Itoa(mr), Head: head})
	if err != nil {
		return "", resolveFailure(repo, err)
	}
	if fresh != target {
		return "", ErrContextScope
	}
	path := commentPath(inst, target.Project, mr) + "/comments"
	if inst.Kind == corehosts.KindGitLab {
		path = commentPath(inst, target.Project, mr) + "/notes"
	}
	// Refuse bodies containing current credentials, rather than silently changing
	// the previewed text and making its durable hash irreconcilable.
	if h.service.redactText(body) != body {
		return "", errors.New("comment contains a credential")
	}
	raw, err := h.post(ctx, inst, token, path, map[string]string{"body": body + "\n\n" + commentMarker(marker, head)})
	if err != nil {
		return "", err
	}
	var w struct {
		ID int64 `json:"id"`
	}
	if json.Unmarshal(raw, &w) != nil || w.ID <= 0 {
		return "", ErrOutcomeUnknown
	}
	return strconv.FormatInt(w.ID, 10), nil
}
func (h *ConfirmedHosts) FindReviewComment(ctx context.Context, repo core.RepoID, mr int, marker, bodyHash, head string) (coreproposals.CommentLookup, error) {
	ctx, cancel := context.WithTimeout(ctx, corehosts.ContextRequestTimeout)
	defer cancel()
	unknown := coreproposals.CommentLookup{Outcome: coreproposals.CommentUnknown}
	receipt := coreproposals.CommentReceipt{CommentID: "1", Marker: marker, BodyHash: bodyHash, Head: head}
	if err := receipt.Validate(); err != nil {
		return unknown, err
	}
	target, inst, token, err := h.commentTarget(ctx, repo, mr, head)
	if err != nil {
		return unknown, err
	}
	actual, err := h.MRHead(ctx, repo, mr)
	if err != nil {
		return unknown, err
	}
	if actual != head {
		return coreproposals.CommentLookup{Outcome: coreproposals.CommentConflict}, nil
	}

	fresh, scopeErr := h.resolver.ResolveContext(ctx, corehosts.ContextKey{Repo: repo, Kind: corehosts.ContextMRDiff, ObjectID: strconv.Itoa(mr), Head: head})
	if scopeErr != nil {
		return unknown, resolveFailure(repo, scopeErr)
	}
	if fresh != target {
		return unknown, ErrContextScope
	}
	path := commentPath(inst, target.Project, mr) + "/comments"
	auth := AuthBearer
	if inst.Kind == corehosts.KindGitLab {
		path = commentPath(inst, target.Project, mr) + "/notes"
		auth = AuthPrivateToken
	}
	lookup, err := h.scanComments(ctx, path, token, auth, marker, bodyHash, head)
	if err != nil || (lookup.Outcome != coreproposals.CommentFound && lookup.Outcome != coreproposals.CommentAbsent) {
		return lookup, err
	}
	// Found and Absent are conclusive: the executor takes Found as the receipt
	// and Absent as leave to act. The repo may have been relinked or untracked
	// while the pages were read, so the scope is proven again before either.
	after, err := h.resolver.ResolveContext(ctx, corehosts.ContextKey{Repo: repo, Kind: corehosts.ContextMRDiff, ObjectID: strconv.Itoa(mr), Head: head})
	if err != nil {
		return unknown, resolveFailure(repo, err)
	}
	if after != target {
		return unknown, ErrContextScope
	}
	return lookup, nil
}

// scanComments reads the comment pages of one merge request, within the
// response budget, and decides what they prove. It does not check scope.
func (h *ConfirmedHosts) scanComments(ctx context.Context, path, token string, auth AuthStyle, marker, bodyHash, head string) (coreproposals.CommentLookup, error) {
	unknown := coreproposals.CommentLookup{Outcome: coreproposals.CommentUnknown}
	remaining := corehosts.ContextMaxResponseBytes
	found := ""
	for page := 1; page <= corehosts.ContextMaxPages; page++ {
		resp, e := h.getter.GetBounded(ctx, path+"?per_page=50&page="+strconv.Itoa(page), token, auth, remaining)
		if e != nil {
			return unknown, e
		}
		remaining -= len(resp.Body)
		var notes []struct {
			ID   int64  `json:"id"`
			Body string `json:"body"`
		}
		if json.Unmarshal(resp.Body, &notes) != nil || notes == nil || len(notes) > 50 {
			return unknown, nil //nolint:nilerr // an unreadable page proves nothing: Unknown is the outcome, to be reconciled, not an error
		}
		for _, n := range notes {
			if !strings.Contains(n.Body, "ygg:"+marker) {
				continue
			}
			suffix := "\n\n" + commentMarker(marker, head)
			body, ok := strings.CutSuffix(n.Body, suffix)
			digest := sha256.Sum256([]byte(body))
			if !ok || hex.EncodeToString(digest[:]) != bodyHash || n.ID <= 0 || found != "" {
				return coreproposals.CommentLookup{Outcome: coreproposals.CommentConflict}, nil
			}
			found = strconv.FormatInt(n.ID, 10)
		}
		if len(notes) < 50 && !hasNextContextPage(resp.Header) {
			if found != "" {
				return coreproposals.CommentLookup{Outcome: coreproposals.CommentFound, CommentID: found}, nil
			}
			return coreproposals.CommentLookup{Outcome: coreproposals.CommentAbsent}, nil
		}
	}
	return unknown, nil
}

// ErrWriteRejected means the host answered a write with 400 or 422: it looked
// at the request and refused it, so nothing was created or changed. Unlike
// ErrOutcomeUnknown there is nothing to reconcile.
var ErrWriteRejected = errContext("host write: the host rejected the request and nothing was created; check the name and settings, then try again")

// writeDenied is the definite answer to a 401 or 403: the host refused the
// token or its permissions before acting, so nothing was created or changed.
func writeDenied(status int) error {
	return fetchErr(corehosts.FailUnauthorized, "the host refused the token or its permissions (status %d) and nothing was created; check the token's access and try again", status)
}

// post performs exactly one RoundTrip. It follows no redirect and never gives
// a transport an idempotency header that could enable automatic replay.
//
// The reply decides what the caller may assume:
//   - 2xx with a readable body: the body is returned;
//   - 401 or 403: the host refused the token or permissions (a *FetchError of
//     kind unauthorized): nothing was created;
//   - 400 or 422: ErrWriteRejected, nothing was created;
//   - everything else (3xx, 5xx, 429, other statuses, a transport error, a lost
//     or oversized body): ErrOutcomeUnknown, which the caller must reconcile
//     read-only and never resend.
func (h *ConfirmedHosts) post(ctx context.Context, in corehosts.Instance, token, path string, value interface{}) ([]byte, error) {
	root := hostAPIRoot(in)
	if !strings.HasPrefix(path, root+"/") {
		return nil, ErrContextScope
	}
	raw, err := json.Marshal(value)
	if err != nil {
		return nil, err
	}
	ctx, cancel := context.WithTimeout(ctx, corehosts.ContextRequestTimeout)
	defer cancel()
	req, err := http.NewRequestWithContext(ctx, http.MethodPost, path, bytes.NewReader(raw))
	if err != nil {
		return nil, err
	}
	req.Header.Set("Content-Type", "application/json")
	req.Header.Set("User-Agent", "ygg")
	if in.Kind == corehosts.KindGitLab {
		req.Header.Set("PRIVATE-TOKEN", token)
	} else {
		req.Header.Set("Authorization", "Bearer "+token)
	}
	res, err := h.transport.RoundTrip(req)
	if err != nil {
		return nil, ErrOutcomeUnknown
	}
	defer res.Body.Close()
	switch res.StatusCode {
	case http.StatusUnauthorized, http.StatusForbidden:
		return nil, writeDenied(res.StatusCode)
	case http.StatusBadRequest, http.StatusUnprocessableEntity:
		return nil, ErrWriteRejected
	}
	if res.StatusCode < 200 || res.StatusCode >= 300 {
		return nil, ErrOutcomeUnknown
	}
	body, err := io.ReadAll(io.LimitReader(res.Body, corehosts.ContextMaxResponseBytes+1))
	if err != nil || len(body) > corehosts.ContextMaxResponseBytes {
		return nil, ErrOutcomeUnknown
	}
	return body, nil
}

func validateCreate(spec CreateRepoSpec) error {
	visibility := corenewrepos.VisibilityPublic
	if spec.Private {
		visibility = corenewrepos.VisibilityPrivate
	}
	// Reuse the approved publication boundary. The description's marker is
	// parsed and round-tripped rather than accepting an arbitrary description.
	prefix := "Created by Yggdrasil [ygg:"
	marker := strings.TrimSuffix(strings.TrimPrefix(spec.Description, prefix), "]")
	p := corenewrepos.PublishSpec{Repo: "rep_publish", HostInstance: string(spec.Instance), Owner: spec.Owner, OwnerKind: corenewrepos.OwnerKind(spec.OwnerKind), Name: spec.Name, Visibility: visibility, Branch: "main", ExpectedHead: strings.Repeat("a", 40), Marker: marker}
	if err := p.Validate(); err != nil {
		return err
	}
	if p.Description() != spec.Description {
		return errors.New("invalid publication marker")
	}
	return nil
}
func (h *ConfirmedHosts) ResolveOwner(ctx context.Context, id corehosts.InstanceID, owner string) (RepoOwnerInfo, error) {
	if err := validateCreate(CreateRepoSpec{Instance: id, Owner: owner, OwnerKind: "user", Name: "check", Description: "Created by Yggdrasil [ygg:" + strings.Repeat("a", 16) + "]", Private: true}); err != nil {
		return RepoOwnerInfo{}, err
	}
	in, token, err := h.instance(id)
	if err != nil {
		return RepoOwnerInfo{}, err
	}
	if in.Kind != corehosts.KindGitHub {
		return RepoOwnerInfo{}, ErrContextUnsupported
	}
	ctx, cancel := context.WithTimeout(ctx, corehosts.ContextRequestTimeout)
	defer cancel()
	root := hostAPIRoot(in)
	resp, err := h.getter.GetBounded(ctx, root+"/users/"+owner, token, AuthBearer, corehosts.ContextMaxResponseBytes)
	if err != nil {
		return RepoOwnerInfo{}, err
	}
	var w struct {
		Login string `json:"login"`
		Type  string `json:"type"`
	}
	if json.Unmarshal(resp.Body, &w) != nil || w.Login != owner {
		return RepoOwnerInfo{}, ErrContextScope
	}
	if w.Type == "Organization" {
		membership, e := h.getter.GetBounded(ctx, root+"/user/memberships/orgs/"+owner, token, AuthBearer, corehosts.ContextMaxResponseBytes)
		if e != nil {
			return RepoOwnerInfo{Kind: "org"}, e
		}
		var m struct {
			State string `json:"state"`
			Role  string `json:"role"`
		}
		if json.Unmarshal(membership.Body, &m) != nil {
			return RepoOwnerInfo{}, errors.New("invalid membership response")
		}
		return RepoOwnerInfo{Kind: "org", CanCreate: m.State == "active" && m.Role == "admin"}, nil
	}
	if w.Type != "User" {
		return RepoOwnerInfo{}, ErrContextUnsupported
	}
	me, err := h.getter.GetBounded(ctx, root+"/user", token, AuthBearer, corehosts.ContextMaxResponseBytes)
	if err != nil {
		return RepoOwnerInfo{}, err
	}
	var user struct {
		Login string `json:"login"`
	}
	if json.Unmarshal(me.Body, &user) != nil {
		return RepoOwnerInfo{}, errors.New("invalid account response")
	}
	return RepoOwnerInfo{Kind: "user", CanCreate: user.Login == owner}, nil
}
func (h *ConfirmedHosts) CreateRepo(ctx context.Context, spec CreateRepoSpec) (CreatedRepo, error) {
	ctx, cancel := context.WithTimeout(ctx, corehosts.ContextRequestTimeout)
	defer cancel()
	if err := validateCreate(spec); err != nil {
		return CreatedRepo{}, err
	}
	in, token, err := h.instance(spec.Instance)
	if err != nil {
		return CreatedRepo{}, err
	}
	if in.Kind != corehosts.KindGitHub {
		return CreatedRepo{}, ErrContextUnsupported
	}
	owner, err := h.ResolveOwner(ctx, spec.Instance, spec.Owner)
	if err != nil {
		return CreatedRepo{}, err
	}
	if owner.Kind != spec.OwnerKind || !owner.CanCreate {
		return CreatedRepo{}, errors.New("repository creation permission is unavailable")
	}
	path := hostAPIRoot(in) + "/user/repos"
	if spec.OwnerKind == "org" {
		path = hostAPIRoot(in) + "/orgs/" + spec.Owner + "/repos"
	}
	raw, err := h.post(ctx, in, token, path, map[string]interface{}{"name": spec.Name, "description": spec.Description, "private": spec.Private, "auto_init": false})
	if err != nil {
		return CreatedRepo{}, err
	}
	repo, err := parseCreated(raw, spec, in)
	if err != nil {
		return CreatedRepo{}, ErrOutcomeUnknown
	}
	return repo, nil
}

type remoteRepo struct {
	ID          int64  `json:"id"`
	Name        string `json:"name"`
	Private     *bool  `json:"private"`
	Description string `json:"description"`
	Clone       string `json:"clone_url"`
	Owner       struct {
		Login string `json:"login"`
	} `json:"owner"`
}

func parseCreated(raw []byte, spec CreateRepoSpec, in corehosts.Instance) (CreatedRepo, error) {
	var w remoteRepo
	if json.Unmarshal(raw, &w) != nil || w.ID <= 0 || w.Name != spec.Name || w.Owner.Login != spec.Owner || w.Private == nil || *w.Private != spec.Private || w.Description != spec.Description {
		return CreatedRepo{}, ErrContextScope
	}
	expected := in.Base + "/" + spec.Owner + "/" + spec.Name + ".git"
	if w.Clone != expected || SafeWebURL(w.Clone) == "" || !strings.HasPrefix(w.Clone, "https://") {
		return CreatedRepo{}, ErrContextScope
	}
	return CreatedRepo{RemoteID: strconv.FormatInt(w.ID, 10), URL: w.Clone}, nil
}
func (h *ConfirmedHosts) LookupRepo(ctx context.Context, spec CreateRepoSpec) (RepoLookup, error) {
	unknown := RepoLookup{Outcome: RepoUnknown}
	if err := validateCreate(spec); err != nil {
		return unknown, err
	}
	in, token, err := h.instance(spec.Instance)
	if err != nil {
		return unknown, err
	}
	if in.Kind != corehosts.KindGitHub {
		return unknown, ErrContextUnsupported
	}
	ctx, cancel := context.WithTimeout(ctx, corehosts.ContextRequestTimeout)
	defer cancel()
	path := hostAPIRoot(in) + "/repos/" + spec.Owner + "/" + spec.Name
	resp, err := h.getter.GetBounded(ctx, path, token, AuthBearer, corehosts.ContextMaxResponseBytes)
	// A 404 can hide a private repo; it is never proof of absence.
	if err != nil {
		return unknown, err
	}
	created, err := parseCreated(resp.Body, spec, in)
	if err != nil {
		return RepoLookup{Outcome: RepoConflict}, nil
	}
	remaining := corehosts.ContextMaxResponseBytes - len(resp.Body)
	empty, err := h.provenEmpty(ctx, path, token, remaining)
	switch {
	case err != nil:
		return unknown, err
	case !empty.known:
		return unknown, nil
	case !empty.empty:
		return RepoLookup{Outcome: RepoConflict}, nil
	}
	return RepoLookup{Outcome: RepoExactMatch, RemoteID: created.RemoteID, URL: created.URL}, nil
}

// refState is what the ref probes proved. known is false when they proved
// nothing; empty is meaningful only when known.
type refState struct{ known, empty bool }

// listCount GETs one JSON array and returns its length. ok is false when the
// body is not an array or the list continues on another page (so its length
// proves nothing about the rest).
func (h *ConfirmedHosts) listCount(ctx context.Context, url, token string, remaining *int) (n int, ok bool, err error) {
	resp, err := h.getter.GetBounded(ctx, url, token, AuthBearer, *remaining)
	if err != nil {
		return 0, false, err
	}
	*remaining -= len(resp.Body)
	var list []json.RawMessage
	decodeErr := json.Unmarshal(resp.Body, &list)
	proven := decodeErr == nil && list != nil && !hasNextContextPage(resp.Header)
	return len(list), proven, nil
}

// provenEmpty decides whether the repository has no refs. Only positive answers
// count: a 404 or any failure is never taken as proof of absence.
//   - matching-refs answers 200: [] is empty, a non-empty list is not;
//   - matching-refs answers 409 (GitHub's "Git Repository is empty"): the
//     repository is empty only if branches and tags both answer 200 with [].
//     A non-empty branches or tags list proves it is not empty;
//   - anything else is an error or unknown.
func (h *ConfirmedHosts) provenEmpty(ctx context.Context, repoPath, token string, remaining int) (refState, error) {
	n, ok, err := h.listCount(ctx, repoPath+"/git/matching-refs/", token, &remaining)
	if err == nil {
		return refState{known: ok, empty: ok && n == 0}, nil
	}
	if !errors.Is(err, errStatusConflict) {
		return refState{}, err
	}
	for _, kind := range []string{"/branches?per_page=1", "/tags?per_page=1"} {
		n, ok, err := h.listCount(ctx, repoPath+kind, token, &remaining)
		if err != nil {
			return refState{}, err
		}
		if !ok {
			return refState{}, nil
		}
		if n > 0 {
			return refState{known: true}, nil
		}
	}
	return refState{known: true, empty: true}, nil
}
