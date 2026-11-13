package hosts

import (
	"context"
	"errors"
	"fmt"
	"reflect"
	"testing"
	"time"

	"github.com/brendatamaar/yggdrasil/internal/core"
	corehosts "github.com/brendatamaar/yggdrasil/internal/core/hosts"
	"github.com/brendatamaar/yggdrasil/internal/git"
)

type resolutionRemote struct {
	raw string
	err error
}

func (r resolutionRemote) RemoteURL(context.Context, string, string) (string, error) {
	return r.raw, r.err
}

func TestCharacterizeReadLink(t *testing.T) {
	for _, tc := range []struct {
		name, raw  string
		err        error
		configured []corehosts.InstanceID
		want       link
	}{
		{name: "exact port wins", raw: "https://git.acme.dev:8443/group/sub/shop.git", configured: []corehosts.InstanceID{"git.acme.dev", "git.acme.dev:8443"}, want: link{linked: true, instance: "git.acme.dev:8443", project: "group/sub/shop"}},
		{name: "bare host fallback", raw: "https://git.acme.dev:8443/group/shop.git", configured: []corehosts.InstanceID{"git.acme.dev"}, want: link{linked: true, instance: "git.acme.dev", project: "group/shop"}},
		{name: "unknown port retained", raw: "https://git.acme.dev:8443/group/shop.git", want: link{linked: true, instance: "git.acme.dev:8443", project: "group/shop"}},
		{name: "ssh port ignored", raw: "ssh://git@git.acme.dev:2222/group/shop.git", want: link{linked: true, instance: "git.acme.dev", project: "group/shop"}},
		{name: "default web port", raw: "https://git.acme.dev:443/group/shop.git", want: link{linked: true, instance: "git.acme.dev", project: "group/shop"}},
		{name: "credentials flag", raw: "https://user:secret@git.acme.dev/group/shop.git", want: link{linked: true, instance: "git.acme.dev", project: "group/shop", credentials: true}},
		{name: "no origin", err: fmt.Errorf("wrapped: %w", git.ErrNoRemote), want: link{reason: "This repo has no \"origin\" remote, so it can't be linked to a git host."}},
		{name: "read failure", err: errors.New("broken"), want: link{reason: "Yggdrasil couldn't read this repo's remote."}},
		{name: "invalid URL", raw: "/local/shop", want: link{reason: "This repo's remote isn't a web or ssh address Yggdrasil understands."}},
	} {
		t.Run(tc.name, func(t *testing.T) {
			s := New(Deps{Remotes: resolutionRemote{tc.raw, tc.err}})
			s.ctx = context.Background()
			s.state.Instances = map[corehosts.InstanceID]corehosts.Instance{}
			for _, id := range tc.configured {
				s.state.Instances[id] = corehosts.Instance{ID: id}
			}
			if got := s.readLink(RepoRef{ID: "shop"}); got != tc.want {
				t.Fatalf("got %+v want %+v", got, tc.want)
			}
		})
	}
	s := New(Deps{})
	if got := s.readLink(RepoRef{}); got.reason != "Git hosts can't be looked up here." {
		t.Fatal(got)
	}
}

type resultClient struct {
	err        error
	panicValue bool
}

func (c resultClient) RepoState(context.Context, corehosts.Instance, string, corehosts.ProjectPath, string, string) (corehosts.RepoState, string, error) {
	if c.panicValue {
		panic("scripted client defect")
	}
	return corehosts.RepoState{}, "new-etag", c.err
}
func (c resultClient) Inbox(context.Context, corehosts.Instance, string) ([]corehosts.InboxItem, error) {
	return nil, c.err
}

func TestCharacterizeFetchMapping(t *testing.T) {
	now := time.Date(2026, 11, 1, 0, 0, 0, 0, time.UTC)
	retry := now.Add(time.Minute)
	for _, tc := range []struct {
		name   string
		client HostClient
		want   func(corehosts.Fetch) corehosts.Input
	}{
		{"success", resultClient{}, func(f corehosts.Fetch) corehosts.Input {
			return corehosts.Fetched{Key: f.Key, Attempt: f.Attempt, Payload: corehosts.RepoState{}, ETag: "new-etag", Now: now}
		}},
		{"wrapped 304", resultClient{err: fmt.Errorf("wrapped: %w", ErrNotModified)}, func(f corehosts.Fetch) corehosts.Input {
			return corehosts.NotModified{Key: f.Key, Attempt: f.Attempt, Now: now}
		}},
		{"304 precedes typed error", resultClient{err: &FetchError{Kind: corehosts.FailUnauthorized, Err: ErrNotModified}}, func(f corehosts.Fetch) corehosts.Input {
			return corehosts.NotModified{Key: f.Key, Attempt: f.Attempt, Now: now}
		}},
		{"rate limit", resultClient{err: fmt.Errorf("wrapped: %w", &FetchError{Kind: corehosts.FailRateLimited, RetryAt: &retry})}, func(f corehosts.Fetch) corehosts.Input {
			return corehosts.FetchFailed{Key: f.Key, Attempt: f.Attempt, Kind: corehosts.FailRateLimited, RetryAt: &retry, Now: now}
		}},
		{"unauthorized", resultClient{err: &FetchError{Kind: corehosts.FailUnauthorized}}, func(f corehosts.Fetch) corehosts.Input {
			return corehosts.FetchFailed{Key: f.Key, Attempt: f.Attempt, Kind: corehosts.FailUnauthorized, Now: now}
		}},
		{"not found", resultClient{err: &FetchError{Kind: corehosts.FailNotFound}}, func(f corehosts.Fetch) corehosts.Input {
			return corehosts.FetchFailed{Key: f.Key, Attempt: f.Attempt, Kind: corehosts.FailNotFound, Now: now}
		}},
		{"unexpected", resultClient{err: errors.New("bad JSON")}, func(f corehosts.Fetch) corehosts.Input {
			return corehosts.FetchFailed{Key: f.Key, Attempt: f.Attempt, Kind: corehosts.FailTransient, Now: now}
		}},
		{"panic", resultClient{panicValue: true}, func(f corehosts.Fetch) corehosts.Input {
			return corehosts.FetchFailed{Key: f.Key, Attempt: f.Attempt, Kind: corehosts.FailTransient, Now: now}
		}},
		{"missing client", nil, func(f corehosts.Fetch) corehosts.Input {
			return corehosts.FetchFailed{Key: f.Key, Attempt: f.Attempt, Kind: corehosts.FailTransient, Now: now}
		}},
	} {
		t.Run(tc.name, func(t *testing.T) {
			for _, cancelled := range []bool{false, true} {
				s := New(Deps{Clock: func() time.Time { return now }})
				ctx, cancel := context.WithCancel(context.Background())
				defer cancel()
				s.ctx = ctx
				inst := corehosts.Instance{ID: "git.acme.dev", Kind: corehosts.KindGitLab}
				s.state, _ = corehosts.Step(s.state, corehosts.ConfigChanged{Instances: []corehosts.Instance{inst}, Now: now})
				var cmds []corehosts.Command
				s.state, cmds = corehosts.Step(s.state, corehosts.TargetsChanged{Repos: []corehosts.RepoTarget{{Repo: core.RepoID("shop"), Instance: inst.ID, Project: "group/shop", Branch: "main"}}, Now: now})
				var f corehosts.Fetch
				for _, c := range cmds {
					if v, ok := c.(corehosts.Fetch); ok {
						f = v
					}
				}
				if f.Key == nil {
					t.Fatal("no fetch")
				}
				e := s.state.Entries[f.Key]
				ph := e.Phase.(corehosts.Fetching)
				ph.Prev = corehosts.Idle{Data: corehosts.RepoState{MR: &corehosts.MergeRequest{Number: 17}}, ETag: "old-etag", FetchedAt: now.Add(-time.Minute)}
				e.Phase = ph
				s.state.Entries[f.Key] = e
				want, _ := corehosts.Step(s.state, tc.want(f))
				if cancelled {
					cancel()
					want = s.state
				}
				s.fetch(f, inst, "", tc.client)
				if !reflect.DeepEqual(s.state, want) {
					t.Fatalf("cancelled=%v: got %+v want %+v", cancelled, s.state, want)
				}
			}
		})
	}
}

func TestCharacterizeTargetEligibility(t *testing.T) {
	for _, tc := range []struct {
		name, branch string
		configured   bool
		reason       string
	}{
		{"configured branch", "main", true, ""},
		{"detached", "", true, "This repo's HEAD is detached, so there is no branch to look up."},
		{"missing instance before detached", "", false, "The host git.acme.dev isn't configured. Add it to the .env file to see its merge requests."},
	} {
		t.Run(tc.name, func(t *testing.T) {
			s := New(Deps{})
			s.ctx = context.Background()
			inst := corehosts.Instance{ID: "git.acme.dev", Kind: corehosts.KindGitLab}
			if tc.configured {
				s.state, _ = corehosts.Step(s.state, corehosts.ConfigChanged{Instances: []corehosts.Instance{inst}})
			}
			s.repos = []RepoRef{{ID: "shop", Path: "cached", Branch: tc.branch}}
			s.remotes["shop"] = remoteCache{path: "cached", link: link{linked: true, instance: inst.ID, project: "group/shop", credentials: true}}
			s.resolve()
			got := s.links["shop"]
			if got.reason != tc.reason || got.linked != (tc.reason == "") || !got.credentials || got.project != "group/shop" {
				t.Fatalf("link %+v", got)
			}
			if len(s.state.Targets) != map[bool]int{true: 1, false: 0}[tc.reason == ""] {
				t.Fatalf("targets %+v", s.state.Targets)
			}
		})
	}
}
