package main

import (
	"context"
	"os"
	"path/filepath"
	"sync"
	"testing"
	"time"

	"github.com/brendatamaar/yggdrasil/internal/actions"
	"github.com/brendatamaar/yggdrasil/internal/actions/actionstest"
	"github.com/brendatamaar/yggdrasil/internal/core"
	corebuilds "github.com/brendatamaar/yggdrasil/internal/core/builds"
	corehosts "github.com/brendatamaar/yggdrasil/internal/core/hosts"
	"github.com/brendatamaar/yggdrasil/internal/core/newrepos"
	"github.com/brendatamaar/yggdrasil/internal/db"
	"github.com/brendatamaar/yggdrasil/internal/db/dbtest"
	"github.com/brendatamaar/yggdrasil/internal/search/searchtest"
	"github.com/brendatamaar/yggdrasil/internal/standup/standuptest"
)

// The real ScopeReader, ContextReader and Polisher of the composition root run
// the same contract suites as their fakes (internal/actions/actionstest and
// internal/standup/standuptest).

func (r *p6Rig) scopeReader() *scopeReader {
	return &scopeReader{store: r.a.store, status: r.a.status, hosts: fakeHostCache{}, git: r.a.git}
}

func (r *p6Rig) storedRepo(t *testing.T, id core.RepoID) core.Repo {
	t.Helper()
	repo, err := r.a.store.GetRepo(context.Background(), id)
	if err != nil {
		t.Fatal(err)
	}
	return repo
}

func TestScopeReaderContractRealAdapter(t *testing.T) {
	actionstest.ScopeReaderContract(t, func(t *testing.T) actionstest.ScopeRig {
		r := newP6Rig(t)
		ctx := context.Background()
		dirs := map[core.RepoID]*p6Repo{}
		n := 0
		return actionstest.ScopeRig{
			Reader: r.scopeReader(),
			Repo: func(t *testing.T, up bool) actionstest.Tracked {
				p := r.newRepo(t)
				if !up {
					searchtest.RunGit(t, r.env, p.dir, "branch", "--unset-upstream")
				}
				r.refresh(t, p)
				dirs[p.id] = p
				return actionstest.Tracked{ID: p.id, Project: p.project}
			},
			Sibling: func(t *testing.T, project core.ProjectID) core.RepoID {
				n++
				name := "shop-sibling-" + string(rune('a'+n))
				return dbtest.Repo(t, r.a.store, core.Repo{ProjectID: project, Name: name, Path: filepath.Join(t.TempDir(), name)}).ID
			},
			Reserved: func(t *testing.T, project core.ProjectID) core.RepoID {
				n++
				id := core.RepoID(db.NewID("rep"))
				leaf := "shop-reserved-" + string(rune('a'+n))
				spec := newrepos.InitSpec{Project: project, Repo: id, Leaf: leaf, Branch: newrepos.DefaultBranch,
					README: "# " + leaf + "\n", Ignore: "/bin/\n", CommitMessage: "Initial commit"}
				err := r.a.store.Commit(ctx, func(tx *db.Tx) error {
					return tx.ReserveRepo(ctx, db.ReserveRepo{Operation: core.OperationID(db.NewID("op")), Project: project, Repo: id,
						SetupTask: core.TaskID(db.NewID("tsk")), Path: filepath.Join(t.TempDir(), leaf), Spec: spec, Agent: "mock",
						Title: "Set up " + leaf, Now: time.Now().UTC()})
				})
				if err != nil {
					t.Fatal(err)
				}
				return id
			},
			Untrack: func(t *testing.T, id core.RepoID) { dbtest.UntrackRepo(t, r.a.store, r.storedRepo(t, id)) },
			Dirty: func(t *testing.T, id core.RepoID) {
				p := dirs[id]
				if err := os.WriteFile(filepath.Join(p.dir, "scratch.txt"), []byte("wip\n"), 0o600); err != nil {
					t.Fatal(err)
				}
				r.refresh(t, p)
			},
			Break: func(t *testing.T, id core.RepoID) {
				p := dirs[id]
				if err := os.RemoveAll(p.dir); err != nil {
					t.Fatal(err)
				}
				r.refresh(t, p)
			},
		}
	})
}

// logStub serves the stored step logs by build, in chunks, like builds.Service.
type logStub struct {
	mu   sync.Mutex
	logs map[core.BuildID][]byte
}

func (l *logStub) StepLog(_ context.Context, id core.BuildID, _ int, after int64) ([]byte, int64, error) {
	l.mu.Lock()
	defer l.mu.Unlock()
	b := l.logs[id]
	if after >= int64(len(b)) {
		return nil, after, nil
	}
	end := min(after+8192, int64(len(b)))
	return b[after:end], end, nil
}

func TestContextReaderContractRealAdapter(t *testing.T) {
	actionstest.ContextReaderContract(t, func(t *testing.T) actionstest.ContextRig {
		r := newP6Rig(t)
		ctx := context.Background()
		main, other := r.newRepo(t), r.newRepo(t)
		r.refresh(t, main, other)
		stub := &logStub{logs: map[core.BuildID][]byte{}}
		scope := r.scopeReader()
		reader := &contextReader{scope: scope, host: noHostReader{}, store: r.a.store, logs: stub, git: r.a.git, redact: redactorForTest()}
		return actionstest.ContextRig{
			Reader: reader, Repo: main.id, OtherRepo: other.id,
			Head: func(t *testing.T) string { return r.head(t, main) },
			Edit: func(t *testing.T, name string) []string {
				body := "changed " + name + " " + actionstest.SecretToken + "\n"
				for file, text := range map[string]string{"README.md": "# shop-api\n" + body, "notes.txt": "notes " + body} {
					if err := os.WriteFile(filepath.Join(main.dir, file), []byte(text), 0o600); err != nil {
						t.Fatal(err)
					}
				}
				return []string{"README.md", "notes.txt"}
			},
			Build: func(t *testing.T, repo core.RepoID, n int) core.BuildID {
				id := core.BuildID(db.NewID("bld"))
				started, now := time.Now().UTC().Add(-time.Minute), time.Now().UTC()
				err := r.a.store.Commit(ctx, func(tx *db.Tx) error {
					_, err := tx.SaveBuild(ctx, corebuilds.Finished{
						ID: id, Spec: corebuilds.Spec{Target: corebuilds.MainCheckout{}, Steps: []corebuilds.StepSpec{{RepoID: repo, Name: "verify", Argv: []string{"false"}}}},
						QueuedAt: started, Actor: "user", StartedAt: &started, FinishedAt: now,
						Steps:  []corebuilds.StepState{corebuilds.StepDone{Attempt: 1, StartedAt: started, FinishedAt: now, Outcome: corebuilds.Failed{ExitCode: 1}}},
						Result: corebuilds.ResultFailed{Step: 0},
					}, db.NewBuild)
					return err
				})
				if err != nil {
					t.Fatal(err)
				}
				body := "token=" + actionstest.SecretToken + "\n"
				for len(body) < n-10 {
					body += "xxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxx\n"
				}
				stub.mu.Lock()
				stub.logs[id] = []byte(body + "LAST-LINE\n")
				stub.mu.Unlock()
				return id
			},
			Untrack:    func(t *testing.T) { dbtest.UntrackRepo(t, r.a.store, r.storedRepo(t, main.id)) },
			NoTokenKey: corehosts.ContextKey{},
		}
	})
}

func TestPolisherContractRealAdapter(t *testing.T) {
	standuptest.PolisherContract(t, func(t *testing.T) standuptest.PolishRig {
		r := newP6Rig(t)
		p := r.newRepo(t)
		r.refresh(t, p)
		pol := polisher{scope: r.scopeReader(), tasks: r.a.tasks}
		return standuptest.PolishRig{
			Polisher: pol, Project: p.project, Unknown: "prj_0000000000000000",
			Tasks: func() int {
				list, err := r.a.store.ListTasks(context.Background(), p.project)
				if err != nil {
					t.Fatal(err)
				}
				return len(list)
			},
			Mode: func(ctx context.Context, id core.TaskID) (core.TaskMode, error) {
				task, err := r.a.store.GetTask(ctx, id)
				return task.Mode, err
			},
		}
	})
}

var _ actions.ScopeReader = (*scopeReader)(nil)
