// Package hosts is the shell of git-host polling (GH-01, GH-02, GH-04,
// docs/design/git-hosts.md). It parses outside data at the boundary (remote
// URLs, .env config, host JSON in the client packages), feeds the pure poller
// in internal/core/hosts, and runs its commands: GET fetches through a
// HostClient, and publishes. Everything is read-only.
//
// Tokens (invariants 3 and 4) live in a map inside the Service. They are sent
// only in request headers, never put in an environment, URL, log, audit entry
// or API response.
package hosts

import (
	"context"
	"errors"
	"fmt"
	"log/slog"
	"slices"
	"sort"
	"strings"
	"sync"
	"time"

	"golang.org/x/sync/errgroup"

	"github.com/brendatamaar/yggdrasil/internal/api"
	"github.com/brendatamaar/yggdrasil/internal/apperr"
	"github.com/brendatamaar/yggdrasil/internal/core"
	corehosts "github.com/brendatamaar/yggdrasil/internal/core/hosts"
	"github.com/brendatamaar/yggdrasil/internal/git"
	"github.com/brendatamaar/yggdrasil/internal/safety"
)

// RemoteReader reads a repo's remote URL (internal/git: git.Git satisfies it).
type RemoteReader interface {
	RemoteURL(ctx context.Context, dir, name string) (string, error)
}

// SecretAdder adds secret values to the process-wide log redactor
// (*safety.SecretRedactor).
type SecretAdder interface {
	Add(secrets ...string)
}

// Publisher tells subscribers that host data changed (*api.Server).
type Publisher interface {
	PublishHostsChanged(msg api.HostsChangedMessage)
}

// RepoRef is one tracked repo and its current branch ("" when detached). The
// lead feeds it from repo status updates.
type RepoRef struct {
	ID     core.RepoID
	Path   string
	Branch string
}

// Deps are the service's collaborators.
type Deps struct {
	Config  Config
	EnvPath string
	// Reload re-reads the config (optional; used by Reload).
	Reload  func() (Config, error)
	Clients map[corehosts.Kind]HostClient
	Remotes RemoteReader
	Publish Publisher
	Log     *slog.Logger
	Clock   func() time.Time
	// TickEvery defaults to corehosts.TickEvery.
	TickEvery time.Duration
	// FetchTimeout defaults to 20 s.
	FetchTimeout time.Duration
	// RemoteTimeout bounds one read of a repo's remote URL; it defaults to
	// 10 s, so a hung git cannot hold up the poller.
	RemoteTimeout time.Duration
	// Secrets (optional) receives the tokens of a reloaded config, so the
	// process-wide redactor masks them too (invariant 4).
	Secrets SecretAdder
}

// link is what a repo's remote resolved to.
type link struct {
	linked      bool
	reason      string // why not linked
	instance    corehosts.InstanceID
	project     corehosts.ProjectPath
	credentials bool
}

type remoteCache struct {
	path string
	link link
}

// Service owns the poller state. It implements api.HostsService.
type Service struct {
	d   Deps
	log *slog.Logger

	mu       sync.Mutex
	started  bool
	closed   bool
	state    corehosts.Poller
	cfg      Config
	red      safety.Redactor
	repos    []RepoRef
	remotes  map[core.RepoID]remoteCache
	links    map[core.RepoID]link
	branches map[core.RepoID]string
	problems []string

	ctx    context.Context
	cancel context.CancelFunc
	group  *errgroup.Group
	wake   chan struct{}
}

// New returns a service. Call Start to begin polling.
func New(d Deps) *Service {
	if d.Clock == nil {
		d.Clock = func() time.Time { return time.Now().UTC() }
	}
	if d.TickEvery <= 0 {
		d.TickEvery = corehosts.TickEvery
	}
	if d.FetchTimeout <= 0 {
		d.FetchTimeout = 20 * time.Second
	}
	if d.RemoteTimeout <= 0 {
		d.RemoteTimeout = 10 * time.Second
	}
	s := &Service{
		d: d, remotes: map[core.RepoID]remoteCache{}, links: map[core.RepoID]link{},
		branches: map[core.RepoID]string{}, wake: make(chan struct{}, 1),
	}
	s.setConfig(d.Config)
	s.log = d.Log
	if s.log == nil {
		s.log = slog.New(slog.DiscardHandler)
	}
	return s
}

func (s *Service) setConfig(c Config) {
	s.cfg = c
	s.red = safety.NewRedactor(c.Secrets()...)
	s.problems = s.problems[:0:0]
	for _, p := range c.Problems {
		s.problems = append(s.problems, p.String())
	}
}

// SetRepos tells the service which repos are tracked and on which branch. The
// lead calls it after each status refresh and tracking change. It returns at
// once: the remotes are read by the service's own goroutine, once per repo
// until the repo set changes.
func (s *Service) SetRepos(repos []RepoRef) {
	s.mu.Lock()
	s.repos = slices.Clone(repos)
	s.mu.Unlock()
	s.signal()
}

func (s *Service) signal() {
	select {
	case s.wake <- struct{}{}:
	default:
	}
}

// Start begins polling. It returns at once; the goroutines stop when ctx is
// cancelled or Close is called.
func (s *Service) Start(ctx context.Context) error {
	s.mu.Lock()
	if s.started {
		s.mu.Unlock()
		return errors.New("the git hosts service is already started")
	}
	s.started = true
	s.ctx, s.cancel = context.WithCancel(ctx)
	s.group, _ = errgroup.WithContext(s.ctx)
	instances := s.cfg.CoreInstances()
	s.mu.Unlock()

	s.step(corehosts.ConfigChanged{Instances: instances, Now: s.d.Clock()})
	s.group.Go(func() error {
		tick := time.NewTicker(s.d.TickEvery)
		defer tick.Stop()
		for {
			select {
			case <-s.ctx.Done():
				return nil
			case <-tick.C:
				s.step(corehosts.Tick{Now: s.d.Clock()})
			case <-s.wake:
				s.resolve()
			}
		}
	})
	s.signal()
	return nil
}

// Close stops polling and waits for the goroutines.
func (s *Service) Close() {
	s.mu.Lock()
	cancel, group := s.cancel, s.group
	s.closed = true
	s.mu.Unlock()
	if cancel != nil {
		cancel()
		_ = group.Wait()
	}
}

// Reload re-reads the config from its source (the settings "reload" action).
// A new token is sent from then on, and is added to the process-wide log
// redactor (Deps.Secrets) before it is used.
func (s *Service) Reload() error {
	if s.d.Reload == nil {
		return errors.New("this service has no config source to reload")
	}
	cfg, err := s.d.Reload()
	if err != nil {
		return fmt.Errorf("reload the git host config: %w", err)
	}
	if s.d.Secrets != nil {
		s.d.Secrets.Add(cfg.Secrets()...)
	}
	s.mu.Lock()
	s.setConfig(cfg)
	s.mu.Unlock()
	s.step(corehosts.ConfigChanged{Instances: cfg.CoreInstances(), Now: s.d.Clock()})
	s.signal()
	return nil
}

// resolve reads the remote of every repo that is not cached, links repos to
// instances and feeds the poller the resulting targets.
func (s *Service) resolve() {
	s.mu.Lock()
	repos := slices.Clone(s.repos)
	known := map[core.RepoID]bool{}
	var todo []RepoRef
	for _, r := range repos {
		known[r.ID] = true
		if c, ok := s.remotes[r.ID]; !ok || c.path != r.Path {
			todo = append(todo, r)
		}
	}
	for id := range s.remotes {
		if !known[id] {
			delete(s.remotes, id)
		}
	}
	s.mu.Unlock()

	read := map[core.RepoID]remoteCache{}
	for _, r := range todo {
		if s.ctx.Err() != nil {
			return
		}
		read[r.ID] = remoteCache{path: r.Path, link: s.readLink(r)}
	}

	s.mu.Lock()
	for id, c := range read {
		s.remotes[id] = c
	}
	var targets []corehosts.RepoTarget
	links := map[core.RepoID]link{}
	branches := map[core.RepoID]string{}
	for _, r := range repos {
		c, ok := s.remotes[r.ID]
		if !ok {
			continue
		}
		l := c.link
		branches[r.ID] = r.Branch
		if l.linked {
			if _, ok := s.state.Instances[l.instance]; !ok {
				l.linked, l.reason = false, fmt.Sprintf("The host %s isn't configured. Add it to the .env file to see its merge requests.", l.instance)
			} else if r.Branch == "" {
				l.linked, l.reason = false, "This repo's HEAD is detached, so there is no branch to look up."
			} else {
				targets = append(targets, corehosts.RepoTarget{Repo: r.ID, Instance: l.instance, Project: l.project, Branch: r.Branch})
			}
		}
		links[r.ID] = l
	}
	s.links, s.branches = links, branches
	s.mu.Unlock()
	s.step(corehosts.TargetsChanged{Repos: targets, Now: s.d.Clock()})
}

// readLink reads and parses a repo's origin remote. The raw URL can hold
// credentials: it is parsed here and never logged or kept.
func (s *Service) readLink(r RepoRef) link {
	if s.d.Remotes == nil {
		return link{reason: "Git hosts can't be looked up here."}
	}
	ctx, cancel := context.WithTimeout(s.ctx, s.d.RemoteTimeout)
	defer cancel()
	raw, err := s.d.Remotes.RemoteURL(ctx, r.Path, "origin")
	if err != nil {
		if errors.Is(err, git.ErrNoRemote) {
			return link{reason: "This repo has no \"origin\" remote, so it can't be linked to a git host."}
		}
		s.log.Warn("could not read a repo's remote", "repo", string(r.ID), "err", s.red.Redact(err.Error()))
		return link{reason: "Yggdrasil couldn't read this repo's remote."}
	}
	ref, err := ParseRemote(raw)
	if err != nil {
		return link{reason: "This repo's remote isn't a web or ssh address Yggdrasil understands."}
	}
	s.mu.Lock()
	id := ref.InstanceID()
	if _, ok := s.state.Instances[id]; !ok {
		if alt := corehosts.InstanceID(ref.Host); alt != id {
			if _, ok := s.state.Instances[alt]; ok {
				id = alt
			}
		}
	}
	s.mu.Unlock()
	return link{linked: true, instance: id, project: ref.Project, credentials: ref.HadCredentials}
}

// step runs the core under the lock and interprets its commands afterwards.
func (s *Service) step(in corehosts.Input) {
	s.mu.Lock()
	next, cmds := safeStep(s.state, in, s.log)
	s.state = next
	s.mu.Unlock()
	s.run(cmds)
}

// safeStep recovers a panic in the core: the state is kept and the failure is
// logged (design: "core: invariant broken").
func safeStep(p corehosts.Poller, in corehosts.Input, log *slog.Logger) (next corehosts.Poller, cmds []corehosts.Command) {
	defer func() {
		if r := recover(); r != nil {
			log.Error("the host poller failed; keeping its previous state", "panic", fmt.Sprint(r))
			next, cmds = p, nil
		}
	}()
	return corehosts.Step(p, in)
}

func (s *Service) run(cmds []corehosts.Command) {
	for _, c := range cmds {
		switch v := c.(type) {
		case corehosts.Fetch:
			s.mu.Lock()
			if s.started && !s.closed {
				inst := s.state.Instances[v.Instance]
				token := s.cfg.Token(v.Instance)
				client := s.d.Clients[inst.Kind]
				// Go does not block, and closed is set before Close waits, so
				// no worker starts once the wait has begun.
				s.group.Go(func() error {
					s.fetch(v, inst, token, client)
					return nil
				})
			}
			s.mu.Unlock()
		case corehosts.PublishKey:
			s.publishKey(v.Key)
		case corehosts.PublishHosts:
			if s.d.Publish != nil {
				s.d.Publish.PublishHostsChanged(api.HostsChangedMessage{})
			}
		}
	}
}

func (s *Service) publishKey(k corehosts.Key) {
	if s.d.Publish == nil {
		return
	}
	switch v := k.(type) {
	case corehosts.RepoKey:
		id := v.Repo
		s.d.Publish.PublishHostsChanged(api.HostsChangedMessage{Repo: &id})
	case corehosts.InboxKey:
		s.d.Publish.PublishHostsChanged(api.HostsChangedMessage{Instance: string(v.Instance)})
	}
}

// fetch runs one Fetch command on a worker and feeds the result back.
func (s *Service) fetch(f corehosts.Fetch, inst corehosts.Instance, token string, client HostClient) {
	ctx, cancel := context.WithTimeout(s.ctx, s.d.FetchTimeout)
	defer cancel()
	var (
		pl   corehosts.Payload
		etag string
		err  error
	)
	func() {
		defer func() {
			if r := recover(); r != nil {
				s.log.Error("a host client panicked", "instance", string(f.Instance), "panic", truncate(s.redactText(fmt.Sprint(r)), 300))
				err = fmt.Errorf("host client panicked: %v", r)
			}
		}()
		if client == nil {
			err = fmt.Errorf("no client for host kind %q", inst.Kind)
			return
		}
		switch k := f.Key.(type) {
		case corehosts.RepoKey:
			var rs corehosts.RepoState
			rs, etag, err = client.RepoState(ctx, inst, token, f.Project, k.Branch, f.ETag)
			pl = rs
		case corehosts.InboxKey:
			var items []corehosts.InboxItem
			items, err = client.Inbox(ctx, inst, token)
			pl = corehosts.Inbox{Items: items}
		}
	}()
	if s.ctx.Err() != nil {
		return
	}
	now := s.d.Clock()
	var in corehosts.Input
	var fe *FetchError
	switch {
	case err == nil:
		in = corehosts.Fetched{Key: f.Key, Attempt: f.Attempt, Payload: pl, ETag: etag, Now: now}
	case errors.Is(err, ErrNotModified):
		in = corehosts.NotModified{Key: f.Key, Attempt: f.Attempt, Now: now}
	case errors.As(err, &fe):
		s.log.Debug("host fetch failed", "instance", string(f.Instance), "kind", string(fe.Kind), "err", s.redact(err))
		in = corehosts.FetchFailed{Key: f.Key, Attempt: f.Attempt, Kind: fe.Kind, RetryAt: fe.RetryAt, Now: now}
	default:
		// Unparseable or unexpected: transient. The host API probably changed.
		s.log.Warn("could not read a host response", "instance", string(f.Instance), "err", truncate(s.redact(err), 300))
		in = corehosts.FetchFailed{Key: f.Key, Attempt: f.Attempt, Kind: corehosts.FailTransient, Now: now}
	}
	s.step(in)
}

func (s *Service) redact(err error) string { return s.redactText(err.Error()) }

func (s *Service) redactText(text string) string {
	s.mu.Lock()
	red := s.red
	s.mu.Unlock()
	return red.Redact(text)
}

func truncate(s string, n int) string {
	if len(s) <= n {
		return s
	}
	return strings.ToValidUTF8(s[:n], "") + "..."
}

// Instances implements api.HostsService.
func (s *Service) Instances(context.Context) ([]corehosts.Instance, error) {
	s.mu.Lock()
	defer s.mu.Unlock()
	out := s.cfg.CoreInstances()
	sort.Slice(out, func(i, j int) bool { return out[i].ID < out[j].ID })
	return out, nil
}

// ConfigInfo implements api.HostsService.
func (s *Service) ConfigInfo(context.Context) api.HostsConfigInfo {
	s.mu.Lock()
	defer s.mu.Unlock()
	return api.HostsConfigInfo{EnvPath: s.d.EnvPath, Problems: slices.Clone(s.problems)}
}

// RepoHost implements api.HostsService.
func (s *Service) RepoHost(_ context.Context, repo core.RepoID) (api.RepoHostView, error) {
	s.mu.Lock()
	defer s.mu.Unlock()
	l, ok := s.links[repo]
	if !ok {
		return api.RepoHostView{NotLinked: "Yggdrasil hasn't looked at this repo's remote yet."}, nil
	}
	if !l.linked {
		return api.RepoHostView{NotLinked: l.reason, CredentialsInRemote: l.credentials}, nil
	}
	branch := s.branches[repo]
	inst := s.state.Instances[l.instance]
	return api.RepoHostView{
		Linked: true, Instance: inst, Project: l.project, Branch: branch,
		Entry:               s.state.Entries[corehosts.RepoKey{Repo: repo, Branch: branch}],
		Links:               corehosts.Links(inst, l.project, branch),
		CredentialsInRemote: l.credentials,
	}, nil
}

// Inbox implements api.HostsService. MRs found by more than one query or on
// more than one instance appear once, by URL; the newest first.
func (s *Service) Inbox(context.Context) (api.InboxView, error) {
	s.mu.Lock()
	defer s.mu.Unlock()
	var v api.InboxView
	seen := map[string]bool{}
	var ids []corehosts.InstanceID
	for k := range s.state.Targets {
		if ik, ok := k.(corehosts.InboxKey); ok {
			ids = append(ids, ik.Instance)
		}
	}
	slices.Sort(ids)
	for _, id := range ids {
		e := s.state.Entries[corehosts.InboxKey{Instance: id}]
		v.Instances = append(v.Instances, api.InboxInstance{Instance: id, Entry: e})
		var prev corehosts.Idle
		switch ph := e.Phase.(type) {
		case corehosts.Idle:
			prev = ph
		case corehosts.Fetching:
			prev = ph.Prev
		case corehosts.Waiting:
			prev = ph.Prev
		case corehosts.Blocked:
			prev = ph.Prev
		}
		if in, ok := prev.Data.(corehosts.Inbox); ok {
			for _, it := range in.Items {
				if it.MR.URL != "" && seen[it.MR.URL] {
					continue
				}
				seen[it.MR.URL] = true
				v.Items = append(v.Items, api.InboxEntry{Instance: id, Item: it})
			}
		}
	}
	sort.SliceStable(v.Items, func(i, j int) bool { return v.Items[i].Item.MR.UpdatedAt.After(v.Items[j].Item.MR.UpdatedAt) })
	return v, nil
}

// Refresh implements api.HostsService. It also forgets the cached remotes, so
// a fixed remote URL is picked up.
func (s *Service) Refresh(_ context.Context, keys []corehosts.Key) error {
	s.mu.Lock()
	var resolved []corehosts.Key
	for _, k := range keys {
		switch v := k.(type) {
		case corehosts.RepoKey:
			found := false
			for t := range s.state.Targets {
				if rk, ok := t.(corehosts.RepoKey); ok && rk.Repo == v.Repo && (v.Branch == "" || rk.Branch == v.Branch) {
					resolved = append(resolved, rk)
					found = true
				}
			}
			if !found {
				if _, known := s.links[v.Repo]; !known {
					s.mu.Unlock()
					return apperr.New(apperr.Invalid, "That repo isn't known to the git host poller. Refresh the page and try again.", nil)
				}
			}
		case corehosts.InboxKey:
			if _, ok := s.state.Instances[v.Instance]; !ok {
				s.mu.Unlock()
				return apperr.New(apperr.Invalid, "That git host isn't configured.", nil)
			}
			resolved = append(resolved, v)
		}
	}
	started := s.started
	if len(keys) == 0 || len(resolved) > 0 {
		s.remotes = map[core.RepoID]remoteCache{}
	}
	s.mu.Unlock()
	if !started {
		return apperr.New(apperr.Unavailable, "Git hosts aren't running yet.", nil)
	}
	if len(keys) > 0 && len(resolved) == 0 {
		s.signal()
		return nil
	}
	s.step(corehosts.RefreshRequested{Keys: resolved, Now: s.d.Clock()})
	s.signal()
	return nil
}

// FailedPipelines implements api.HostsService.
func (s *Service) FailedPipelines(context.Context) (map[core.RepoID]struct{}, error) {
	s.mu.Lock()
	defer s.mu.Unlock()
	return corehosts.FailedPipelines(s.state), nil
}
