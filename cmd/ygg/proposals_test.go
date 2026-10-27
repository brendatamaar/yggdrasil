package main

import (
	"bytes"
	"context"
	"fmt"
	"net/http"
	"os"
	"regexp"
	"testing"
	"time"

	"github.com/brendatamaar/yggdrasil/internal/api"
	"github.com/brendatamaar/yggdrasil/internal/builds/toolchains"
	"github.com/brendatamaar/yggdrasil/internal/core"
	corebuilds "github.com/brendatamaar/yggdrasil/internal/core/builds"
	corehosts "github.com/brendatamaar/yggdrasil/internal/core/hosts"
	coreprop "github.com/brendatamaar/yggdrasil/internal/core/proposals"
	"github.com/brendatamaar/yggdrasil/internal/db"
	"github.com/brendatamaar/yggdrasil/internal/proposals"
	"github.com/brendatamaar/yggdrasil/internal/search/searchtest"
)

// A proposal that was Confirmed when the last run ended is reconciled at the
// next start. It is never executed again on its own: with nothing to prove
// that the install landed, it becomes a retryable Interrupted failure and no
// build is queued.
func TestConfirmedProposalIsReconciledNotExecutedAtStart(t *testing.T) {
	cfg := config{addr: "127.0.0.1:0", dataDir: t.TempDir()}
	base, cfg, stop := startAppWith(t, cfg)
	project, repo := trackRepo(t, cfg)
	code, task := createTask(t, base, project, repo, "ask")
	if code != http.StatusCreated {
		t.Fatalf("create task = %d", code)
	}
	if err := stop(); err != nil {
		t.Fatal(err)
	}

	// Leave a Confirmed install proposal behind, as a crash would.
	ctx := context.Background()
	d, err := db.Open(ctx, cfg.dataDir+"/ygg.db")
	if err != nil {
		t.Fatal(err)
	}
	store := db.NewStore(d, nil)
	now := time.Now().UTC()
	var pid core.ProposalID = "prp_crashed"
	err = store.Commit(ctx, func(tx *db.Tx) error {
		cs, err := tx.EnsureChangeSet(ctx, core.TaskID(task), "cs_crashed", now)
		if err != nil {
			return err
		}
		p, cmds := coreprop.Step(coreprop.Proposal{}, coreprop.Created{
			Proposal: coreprop.Proposal{
				ID: pid, ChangeSet: cs, Task: core.TaskID(task), Repo: core.RepoID(repo),
				Source: coreprop.UserSource{}, Action: coreprop.Install{Repo: core.RepoID(repo), Step: "install"}, Summary: "install",
			},
			Facts: coreprop.Facts{RepoTracked: true, PathsOK: true, RemoteSupported: true, TaskChangeSet: cs}, Now: now,
		})
		if p.State == nil {
			return fmt.Errorf("proposal refused: %v", cmds)
		}
		return tx.CreateProposalRecord(ctx, p)
	})
	if err != nil {
		t.Fatal(err)
	}
	rec, err := store.GetProposalRecord(ctx, pid)
	if err != nil {
		t.Fatal(err)
	}
	next, _ := coreprop.Step(rec.Proposal, coreprop.Confirm{Hash: rec.Proposal.ActionHash, By: "user", RepoTracked: true, Now: now})
	if next.State.Status() != core.ProposalConfirmed {
		t.Fatalf("setup: state = %v", next.State.Status())
	}
	if err := store.Commit(ctx, func(tx *db.Tx) error { _, err := tx.SaveProposalRecord(ctx, next, rec.Version); return err }); err != nil {
		t.Fatal(err)
	}
	_ = d.Close()

	base, _, _ = startAppWith(t, cfg)
	var out api.ProposalsResponse
	deadline := time.Now().Add(20 * time.Second)
	for {
		getJSON(t, base+"/api/proposals?task="+task, &out)
		if len(out.Proposals) == 1 && out.Proposals[0].Status == core.ProposalFailed {
			break
		}
		if time.Now().After(deadline) {
			t.Fatalf("proposal never left confirmed: %+v", out)
		}
		time.Sleep(50 * time.Millisecond)
	}
	if got := out.Proposals[0]; got.FailKind != string(coreprop.FailInterrupted) || !got.Retryable {
		t.Errorf("proposal = %+v, want a retryable interrupted failure", got)
	}
	var list struct{ Builds []any }
	getJSON(t, base+"/api/builds", &list)
	if len(list.Builds) != 0 {
		t.Errorf("a build was queued at start: %+v", list.Builds)
	}
}

type fakeQueue struct {
	spec  corebuilds.Spec
	actor corebuilds.Actor
	state corebuilds.State
}

func (q *fakeQueue) Enqueue(_ context.Context, s corebuilds.Spec, a corebuilds.Actor) (core.BuildID, error) {
	q.spec, q.actor = s, a
	return "bld_1", nil
}
func (q *fakeQueue) Get(context.Context, core.BuildID) (corebuilds.State, error) { return q.state, nil }

func TestProposalBuildsEnqueuesTheNamedInstallStepAndWaits(t *testing.T) {
	cfg := config{addr: "127.0.0.1:0", dataDir: t.TempDir()}
	d, err := db.Open(context.Background(), cfg.dataDir+"/ygg.db")
	if err != nil {
		t.Fatal(err)
	}
	defer d.Close()
	store := db.NewStore(d, nil)
	// Track a repo that looks like a Node project.
	_, repo := trackRepoWith(t, cfg, map[string]string{"package.json": `{"name":"shop-web","scripts":{"test":"x"}}`, "package-lock.json": "{}"})
	q := &fakeQueue{}
	b := proposalBuilds{queue: q, store: store, toolchains: toolchains.All(), poll: time.Millisecond}

	if _, err := b.Enqueue(context.Background(), core.RepoID(repo), "no-such-step", "proposal:prp_1"); err == nil {
		t.Fatal("an unknown install step was queued")
	}
	id, err := b.Enqueue(context.Background(), core.RepoID(repo), firstInstallStepName(t, b, repo), "proposal:prp_1")
	if err != nil || id != "bld_1" {
		t.Fatalf("enqueue = %q, %v", id, err)
	}
	if _, ok := q.spec.Target.(corebuilds.MainCheckout); !ok || len(q.spec.Steps) != 1 || q.actor != "proposal:prp_1" {
		t.Errorf("spec = %+v actor %q", q.spec, q.actor)
	}

	for _, c := range []struct {
		res  corebuilds.Result
		want bool
	}{{corebuilds.ResultSucceeded{}, true}, {corebuilds.ResultFailed{Step: 0}, false}, {corebuilds.ResultCancelled{}, false}} {
		q.state = corebuilds.Finished{Result: c.res}
		out, err := b.Wait(context.Background(), "bld_1")
		if err != nil || out.Succeeded != c.want || (!c.want && out.Reason == "") {
			t.Errorf("%T: outcome %+v, %v", c.res, out, err)
		}
	}
	// A build that never ends stops when the context does.
	q.state = corebuilds.Queued{}
	ctx, cancel := context.WithTimeout(context.Background(), 30*time.Millisecond)
	defer cancel()
	if _, err := b.Wait(ctx, "bld_1"); err == nil {
		t.Error("Wait returned without a finished build")
	}
}

func firstInstallStepName(t *testing.T, b proposalBuilds, repo string) string {
	t.Helper()
	r, err := b.store.GetRepo(context.Background(), core.RepoID(repo))
	if err != nil {
		t.Fatal(err)
	}
	for _, tc := range b.toolchains {
		if d, ok := tc.Detect(os.DirFS(r.Path)); ok && d.Dir == "." {
			if in := tc.Commands(d).Install; len(in) > 0 {
				return in[0].Name
			}
		}
	}
	t.Fatal("no install step detected")
	return ""
}

// trackRepoWith tracks a fresh temp git repo holding files.
func trackRepoWith(t *testing.T, cfg config, files map[string]string) (project, repo string) {
	t.Helper()
	env := searchtest.GitEnv(t)
	dir := searchtest.InitRepo(t, env, files)
	var out bytes.Buffer
	if err := seed(context.Background(), &out, cfg, dir, ""); err != nil {
		t.Fatal(err)
	}
	ids := regexp.MustCompile(`(prj|rep)_[0-9a-f]+`).FindAllString(out.String(), -1)
	if len(ids) < 2 {
		t.Fatalf("seed output has no IDs: %s", out.String())
	}
	return ids[0], ids[1]
}

type fakeInstances []corehosts.Instance

func (f fakeInstances) Instances(context.Context) ([]corehosts.Instance, error) { return f, nil }

func TestProposalHostsClassifiesRemotesByConfiguredInstance(t *testing.T) {
	h := proposalHosts{svc: fakeInstances{
		{ID: "git.acme.dev", Kind: corehosts.KindGitLab, Base: "https://git.acme.dev"},
		{ID: "github.com", Kind: corehosts.KindGitHub, Base: "https://github.com"},
		{ID: "code.example.org", Kind: corehosts.KindGitea, Base: "https://code.example.org:3000"},
	}}
	for url, want := range map[string]proposals.HostKind{
		"https://git.acme.dev/acme/shop-api.git":            proposals.HostGitLab,
		"git@git.acme.dev:acme/shop-api.git":                proposals.HostGitLab,
		"https://user:tok@GitHub.com/acme/shop-api.git":     proposals.HostGitHub,
		"ssh://git@code.example.org:2222/acme/shop-api.git": proposals.HostGitea,
		"https://elsewhere.example/acme/shop-api.git":       proposals.HostUnknown,
		"not a url":           proposals.HostUnknown,
		"":                    proposals.HostUnknown,
		"C:\\repos\\shop-api": proposals.HostUnknown,
	} {
		if got := h.Kind(url); got != want {
			t.Errorf("Kind(%q) = %q, want %q", url, got, want)
		}
	}
}
