package main

import (
	"context"
	"os"
	"path/filepath"
	"sync"
	"testing"
	"time"

	"github.com/brendatamaar/yggdrasil/internal/builds/toolchains"
	"github.com/brendatamaar/yggdrasil/internal/core"
	corebuilds "github.com/brendatamaar/yggdrasil/internal/core/builds"
	corehosts "github.com/brendatamaar/yggdrasil/internal/core/hosts"
	"github.com/brendatamaar/yggdrasil/internal/db"
	"github.com/brendatamaar/yggdrasil/internal/db/dbtest"
	"github.com/brendatamaar/yggdrasil/internal/proposals/proposalstest"
)

// The real host classifier and install-build runner pass the same contract
// as their fakes.
func TestProposalHostsContract(t *testing.T) {
	proposalstest.HostsContract(t, func(instances map[string]string) func(string) string {
		var list fakeInstances
		for host, kind := range instances {
			k := map[string]corehosts.Kind{
				proposalstest.GitLab: corehosts.KindGitLab, proposalstest.GitHub: corehosts.KindGitHub, proposalstest.Gitea: corehosts.KindGitea,
			}[kind]
			list = append(list, corehosts.Instance{ID: corehosts.InstanceID(host), Kind: k, Base: "https://" + host})
		}
		h := proposalHosts{svc: list}
		return func(u string) string { return string(h.Kind(u)) }
	})
}

// contractQueue is a build queue whose builds all end the way the test says.
type contractQueue struct {
	mu     sync.Mutex
	owners []string
	result string
}

func (q *contractQueue) Enqueue(_ context.Context, _ corebuilds.Spec, a corebuilds.Actor) (core.BuildID, error) {
	q.mu.Lock()
	defer q.mu.Unlock()
	q.owners = append(q.owners, string(a))
	return core.BuildID("bld_1"), nil
}

func (q *contractQueue) Get(context.Context, core.BuildID) (corebuilds.State, error) {
	q.mu.Lock()
	defer q.mu.Unlock()
	switch q.result {
	case "succeeded":
		return corebuilds.Finished{Result: corebuilds.ResultSucceeded{}}, nil
	case "failed":
		return corebuilds.Finished{Result: corebuilds.ResultFailed{Step: 0}}, nil
	case "cancelled":
		return corebuilds.Finished{Result: corebuilds.ResultCancelled{}}, nil
	}
	return corebuilds.Queued{}, nil
}

func TestProposalBuildsContract(t *testing.T) {
	proposalstest.BuildsContract(t, func(t *testing.T) proposalstest.BuildsRig {
		d, err := db.Open(context.Background(), filepath.Join(t.TempDir(), "ygg.db"))
		if err != nil {
			t.Fatal(err)
		}
		t.Cleanup(func() { _ = d.Close() })
		store := db.NewStore(d, nil)
		p := dbtest.Project(t, store, core.Project{Name: "acme-shop", Path: t.TempDir()})
		repoDir := filepath.Join(p.Path, "shop-web")
		writeTree(t, repoDir, map[string]string{
			"package.json": `{"name":"shop-web","scripts":{"test":"x"}}`, "package-lock.json": "{}",
		})
		r := dbtest.Repo(t, store, core.Repo{ProjectID: p.ID, Name: "shop-web", Path: repoDir})
		q := &contractQueue{result: "running"}
		b := proposalBuilds{queue: q, store: store, toolchains: toolchains.All(), poll: time.Millisecond}
		return proposalstest.BuildsRig{
			Step: firstInstallStepName(t, b, string(r.ID)),
			Enqueue: func(ctx context.Context, step, owner string) (string, error) {
				id, err := b.Enqueue(ctx, r.ID, step, owner)
				return string(id), err
			},
			Wait: func(ctx context.Context, id string) (bool, string, error) {
				o, err := b.Wait(ctx, core.BuildID(id))
				return o.Succeeded, o.Reason, err
			},
			SetResult: func(res string) { q.mu.Lock(); q.result = res; q.mu.Unlock() },
			Queued: func() []string {
				q.mu.Lock()
				defer q.mu.Unlock()
				return append([]string(nil), q.owners...)
			},
		}
	})
}

func writeTree(t *testing.T, dir string, files map[string]string) {
	t.Helper()
	for name, content := range files {
		p := filepath.Join(dir, filepath.FromSlash(name))
		if err := os.MkdirAll(filepath.Dir(p), 0o750); err != nil {
			t.Fatal(err)
		}
		if err := os.WriteFile(p, []byte(content), 0o600); err != nil {
			t.Fatal(err)
		}
	}
}
