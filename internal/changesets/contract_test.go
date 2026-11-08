package changesets_test

import (
	"context"
	"os"
	"path/filepath"
	"testing"
	"time"

	"github.com/brendatamaar/yggdrasil/internal/changesets"
	"github.com/brendatamaar/yggdrasil/internal/changesets/changesetstest"
	"github.com/brendatamaar/yggdrasil/internal/core"
	coreprop "github.com/brendatamaar/yggdrasil/internal/core/proposals"
	"github.com/brendatamaar/yggdrasil/internal/db"
	"github.com/brendatamaar/yggdrasil/internal/db/dbtest"
	"github.com/brendatamaar/yggdrasil/internal/safety"
)

func TestFakeProposalsAPIContract(t *testing.T) {
	changesetstest.ProposalsAPIContract(t, func(t *testing.T) changesetstest.APIHarness {
		w := changesetstest.NewWorld()
		w.AddTask("tsk_1", "rep_1")
		w.AddSet("cs_1", "tsk_1")
		return changesetstest.APIHarness{
			API: changesetstest.NewAPI(w),
			NewPending: func(t *testing.T) (core.ProposalID, string) {
				return w.AddProposal("cs_1", "rep_1", core.ProposalInstall)
			},
			Status: func(t *testing.T, id core.ProposalID) core.ProposalStatus { return w.Status(id) },
			Settle: func(t *testing.T, id core.ProposalID) core.ProposalStatus {
				w.SetStatus(id, core.ProposalExecuted, "")
				return w.Status(id)
			},
		}
	})
}

func fakeStoreHarness(t *testing.T) changesetstest.StoreHarness {
	w := changesetstest.NewWorld()
	w.AddTask("tsk_1", "rep_1", "rep_2")
	w.AddTask("tsk_2", "rep_3")
	w.AddSet("cs_1", "tsk_1")
	w.AddSet("cs_2", "tsk_2")
	a, _ := w.AddProposal("cs_1", "rep_1", core.ProposalInstall)
	b, _ := w.AddProposal("cs_1", "rep_2", core.ProposalInstall)
	o, _ := w.AddProposal("cs_2", "rep_3", core.ProposalInstall)
	return changesetstest.StoreHarness{
		Store: changesetstest.NewStore(w), Task: "tsk_1", OtherTask: "tsk_2", Repos: [2]core.RepoID{"rep_1", "rep_2"},
		Set: "cs_1", OtherSet: "cs_2", Members: [2]core.ProposalID{a, b}, Other: o, Audit: w.Audit,
	}
}

func TestFakeChangeSetStoreContract(t *testing.T) {
	changesetstest.ChangeSetStoreContract(t, fakeStoreHarness)
}

func TestSQLiteChangeSetStoreContract(t *testing.T) {
	changesetstest.ChangeSetStoreContract(t, func(t *testing.T) changesetstest.StoreHarness {
		ctx := context.Background()
		d, err := db.Open(ctx, filepath.Join(t.TempDir(), "ygg.db"))
		if err != nil {
			t.Fatal(err)
		}
		t.Cleanup(func() { _ = d.Close() })
		store := db.NewStore(d, safety.NewRedactor().Redact)
		project := dbtest.Project(t, store, core.Project{Name: "acme-shop", Path: t.TempDir()})
		var repos [3]core.Repo
		for i, name := range []string{"shop-api", "shop-web", "shop-admin"} {
			dir := filepath.Join(project.Path, name)
			if err := os.MkdirAll(dir, 0o755); err != nil {
				t.Fatal(err)
			}
			repos[i] = dbtest.Repo(t, store, core.Repo{ProjectID: project.ID, Name: name, Path: dir})
		}
		mkTask := func(rs ...core.Repo) core.Task {
			ids := make([]core.RepoID, len(rs))
			for i, r := range rs {
				ids[i] = r.ID
			}
			return dbtest.Task(t, store, core.Task{ProjectID: project.ID, Scope: core.ScopeProject, Mode: core.ModeCode, RepoIDs: ids})
		}
		task1, task2 := mkTask(repos[0], repos[1]), mkTask(repos[2])
		now := time.Date(2026, 11, 1, 9, 0, 0, 0, time.UTC)
		n := 0
		create := func(task core.Task, repo core.Repo) (core.ChangeSetID, core.ProposalID) {
			n++
			id := core.ProposalID("prp_c" + string(rune('a'+n)))
			var cs core.ChangeSetID
			err := store.Commit(ctx, func(tx *db.Tx) error {
				var err error
				cs, err = tx.EnsureChangeSet(ctx, task.ID, core.ChangeSetID("cs_"+string(id)), now)
				if err != nil {
					return err
				}
				p, cmds := coreprop.Step(coreprop.Proposal{}, coreprop.Created{
					Proposal: coreprop.Proposal{ID: id, ChangeSet: cs, Task: task.ID, Repo: repo.ID, Source: coreprop.AgentSource{Task: task.ID},
						Action: coreprop.Install{Repo: repo.ID, Step: "install"}, Summary: "deps"},
					Facts: coreprop.Facts{RepoTracked: true, PathsOK: true, RemoteSupported: true, TaskChangeSet: cs}, Now: now,
				})
				if p.State == nil {
					t.Fatalf("Step refused: %v", cmds)
				}
				return tx.CreateProposalRecord(ctx, p)
			})
			if err != nil {
				t.Fatal(err)
			}
			return cs, id
		}
		cs1, a := create(task1, repos[0])
		_, b := create(task1, repos[1])
		cs2, o := create(task2, repos[2])
		return changesetstest.StoreHarness{
			Store: changesets.NewSQLiteStore(store, nil), Task: task1.ID, OtherTask: task2.ID, Repos: [2]core.RepoID{repos[0].ID, repos[1].ID},
			Set: cs1, OtherSet: cs2, Members: [2]core.ProposalID{a, b}, Other: o,
			Audit: func(subject string) []core.AuditEntry {
				out, err := store.ListAudit(ctx, subject, 100)
				if err != nil {
					t.Fatal(err)
				}
				return out
			},
		}
	})
}
