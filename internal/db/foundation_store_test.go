package db

import (
	"context"
	"encoding/json"
	"errors"
	"strings"
	"testing"
	"time"

	"github.com/brendatamaar/yggdrasil/internal/core"
	"github.com/brendatamaar/yggdrasil/internal/core/actions"
	"github.com/brendatamaar/yggdrasil/internal/core/newrepos"
)

func TestFoundationOutboxSameWorkerFence(t *testing.T) {
	for _, operation := range []string{"complete", "release"} {
		t.Run(operation, func(t *testing.T) {
			ctx := context.Background()
			s := NewStore(openTest(t), nil)
			if err := s.Commit(ctx, func(tx *Tx) error {
				_, err := tx.EnqueueOutbox(ctx, OutboxRow{Kind: OutboxActionRequest, Subject: "req_shop", Attempt: 1, Payload: json.RawMessage(`{}`)}, at0)
				return err
			}); err != nil {
				t.Fatal(err)
			}
			claim := func(now time.Time) OutboxRow {
				t.Helper()
				var rows []OutboxRow
				if err := s.Commit(ctx, func(tx *Tx) (err error) {
					rows, err = tx.ClaimOutbox(ctx, "worker-shop", now, time.Minute, 1)
					return err
				}); err != nil {
					t.Fatal(err)
				}
				if len(rows) != 1 {
					t.Fatalf("claims: %+v", rows)
				}
				return rows[0]
			}
			a := claim(at0)
			b := claim(at0.Add(2 * time.Minute))
			if b.Version <= a.Version || b.ClaimCount != 2 {
				t.Fatalf("reclaim not fenced: A=%+v B=%+v", a, b)
			}
			err := s.Commit(ctx, func(tx *Tx) error {
				if operation == "complete" {
					return tx.CompleteOutbox(ctx, a.ID, "worker-shop", a.Version, at0.Add(3*time.Minute))
				}
				return tx.ReleaseOutbox(ctx, a.ID, "worker-shop", a.Version, at0.Add(4*time.Minute))
			})
			if !errors.Is(err, ErrNotFound) {
				t.Fatalf("stale %s: %v, want ErrNotFound", operation, err)
			}
			if err := s.Commit(ctx, func(tx *Tx) error {
				return tx.CompleteOutbox(ctx, b.ID, "worker-shop", b.Version, at0.Add(3*time.Minute))
			}); err != nil {
				t.Fatal(err)
			}
			if err := s.Commit(ctx, func(tx *Tx) error {
				return tx.CompleteOutbox(ctx, b.ID, "worker-shop", b.Version, at0.Add(4*time.Minute))
			}); !errors.Is(err, ErrNotFound) {
				t.Fatalf("duplicate completion: %v", err)
			}
		})
	}
}

func TestFoundationActionRejectsInvalidMatchingDigest(t *testing.T) {
	mutations := map[string]func(*actions.Request){
		"repo ID":           func(r *actions.Request) { r.RepoID = "" },
		"request ID length": func(r *actions.Request) { r.RequestID = core.RequestID(strings.Repeat("x", 101)) },
		"agent length":      func(r *actions.Request) { r.Agent = strings.Repeat("x", actions.MaxAgentLen+1) },
		"source length": func(r *actions.Request) {
			r.Kind = actions.DiagnoseBuild
			r.SourceID = strings.Repeat("x", actions.MaxSourceIDLen+1)
		},

		"request ID":     func(r *actions.Request) { r.RequestID = "" },
		"kind":           func(r *actions.Request) { r.Kind = "unknown" },
		"source missing": func(r *actions.Request) { r.Kind = actions.DiagnoseBuild },
		"source foreign": func(r *actions.Request) { r.SourceID = "build_1" },
		"source URL": func(r *actions.Request) {
			r.Kind = actions.DiagnoseBuild
			r.SourceID = "https://git.acme.example/build"
		},
		"agent missing":   func(r *actions.Request) { r.Agent = "" },
		"agent malformed": func(r *actions.Request) { r.Agent = "mock/escape" },
		"model control":   func(r *actions.Request) { r.Model = "mock\nmodel" },
		"model oversized": func(r *actions.Request) { r.Model = strings.Repeat("x", actions.MaxModelLen+1) },
	}
	for name, mutate := range mutations {
		t.Run(name, func(t *testing.T) {
			ctx := context.Background()
			s := NewStore(openTest(t), nil)
			_, repo := seed(t, s)
			req := actions.Request{RequestID: "req_shop", RepoID: repo.ID, Kind: actions.DiagnoseRepo, Agent: "mock"}
			mutate(&req)
			rec := ActionRecord{Request: req, Digest: req.Digest(), State: ActionSubmitting, Attempt: 1, Key: "action:shop:1", CreatedAt: at0}
			err := s.Commit(ctx, func(tx *Tx) error { _, _, err := tx.BeginActionRequest(ctx, rec); return err })
			if !errors.Is(err, actions.ErrInvalidRequest) {
				t.Fatalf("matching digest invalid request: %v", err)
			}
			if n := count(t, s.db, `SELECT count(*) FROM workspace_action_requests`); n != 0 {
				t.Fatal("invalid request persisted")
			}
		})
	}
}

func foundationReservation(t *testing.T) (*Store, newrepos.InitSpec) {
	t.Helper()
	ctx := context.Background()
	s := NewStore(openTest(t), nil)
	p, _ := seed(t, s)
	spec := newReposSpec(p.ID, "rep_shopnew", "shop-new")
	if err := s.Commit(ctx, func(tx *Tx) error {
		return tx.ReserveRepo(ctx, ReserveRepo{Operation: "op_shop", Project: p.ID, Repo: spec.Repo, SetupTask: "tsk_setup", Path: "/work/acme/shop-new", Spec: spec, Agent: "mock", Title: "Set up shop-new", Now: at0})
	}); err != nil {
		t.Fatal(err)
	}
	return s, spec
}

func foundationPublish(spec newrepos.InitSpec) newrepos.PublishSpec {
	return newrepos.PublishSpec{Repo: spec.Repo, HostInstance: "git.acme.example", Owner: "acme", OwnerKind: newrepos.OwnerOrg, Name: "shop-new", Visibility: newrepos.VisibilityPrivate, Branch: "main", ExpectedHead: strings.Repeat("a", 40), Marker: strings.Repeat("b", 16)}
}

func TestFoundationReservationImmutableScope(t *testing.T) {
	changes := map[string]func(*newrepos.InitSpec){"project": func(s *newrepos.InitSpec) { s.Project = "prj_other" }, "repo": func(s *newrepos.InitSpec) { s.Repo = "rep_other" }, "leaf": func(s *newrepos.InitSpec) { s.Leaf = "shop-other" }, "content": func(s *newrepos.InitSpec) { s.README = "changed" }}
	for name, change := range changes {
		t.Run("save/"+name, func(t *testing.T) {
			s, spec := foundationReservation(t)
			other := spec
			change(&other)
			err := s.Commit(context.Background(), func(tx *Tx) error {
				_, err := tx.SaveReservation(context.Background(), "op_shop", newrepos.Reserved{Spec: other, Hash: other.Hash()}, "", 0, at0)
				return err
			})
			if err == nil {
				t.Fatal("changed immutable spec saved")
			}
			rec, err := s.GetReservation(context.Background(), "op_shop")
			if err != nil || rec.SpecHash != spec.Hash() || rec.Operation.Version != 0 {
				t.Fatalf("failed save changed row: %+v %v", rec, err)
			}
		})
	}
	for _, kind := range []string{"publishing", "resume publish", "resume init", "hash", "ready"} {
		t.Run(kind, func(t *testing.T) {
			s, spec := foundationReservation(t)
			pub := foundationPublish(spec)
			pub.Repo = "rep_other"
			other := spec
			other.Repo = "rep_other"
			states := map[string]newrepos.State{"publishing": newrepos.Publishing{Spec: pub, Attempt: 1, Stage: newrepos.StageCreateRemote}, "resume publish": newrepos.Uncertain{Attempt: 1, Stage: newrepos.StageCreateRemote, Resume: newrepos.ResumePublish{Spec: pub}}, "resume init": newrepos.Stopped{Stage: newrepos.StageCreateDirectory, Reason: newrepos.StopStageFailed, Resume: newrepos.ResumeInit{Spec: other}}, "hash": newrepos.Reserved{Spec: spec, Hash: strings.Repeat("0", 64)}, "ready": newrepos.Ready{Repo: "rep_other", InitialHead: strings.Repeat("a", 40)}}
			if err := s.Commit(context.Background(), func(tx *Tx) error {
				_, err := tx.SaveReservation(context.Background(), "op_shop", states[kind], "", 0, at0)
				return err
			}); err == nil {
				t.Fatal("foreign binding saved")
			}
		})
	}
	// Raw spec tampering retains the original row headers. Recompute the hash so
	// the scope check, rather than a hash mismatch, must reject the row.
	for name, change := range changes {
		t.Run("raw/"+name, func(t *testing.T) {
			s, spec := foundationReservation(t)
			change(&spec)
			raw, err := json.Marshal(spec)
			if err != nil {
				t.Fatal(err)
			}
			hash := spec.Hash()
			if name == "content" {
				hash = strings.Repeat("0", 64)
			}
			if _, err := s.db.write.ExecContext(context.Background(), `UPDATE repo_reservations SET spec=?, spec_hash=? WHERE operation_id='op_shop'`, string(raw), hash); err != nil {
				t.Fatal(err)
			}
			if _, err := s.GetReservation(context.Background(), "op_shop"); err == nil {
				t.Fatal("tampered row loaded")
			}
			if err := s.Commit(context.Background(), func(tx *Tx) error {
				_, err := tx.SaveReservation(context.Background(), "op_shop", newrepos.Reserved{Spec: spec, Hash: spec.Hash()}, "", 0, at0)
				return err
			}); err == nil {
				t.Fatal("tampered row saved")
			}
		})
	}
	s, spec := foundationReservation(t)
	ctx := context.Background()
	ready := newrepos.Ready{Repo: spec.Repo, InitialHead: strings.Repeat("a", 40)}
	if err := s.Commit(ctx, func(tx *Tx) error {
		v, err := tx.SaveReservation(ctx, "op_shop", ready, "", 0, at0)
		if err == nil && v != 1 {
			t.Errorf("version=%d", v)
		}
		return err
	}); err != nil {
		t.Fatal(err)
	}
	if err := s.Commit(ctx, func(tx *Tx) error { _, err := tx.SaveReservation(ctx, "op_shop", ready, "", 0, at0); return err }); !errors.Is(err, ErrConflict) {
		t.Fatalf("stale save=%v", err)
	}
	rec, err := s.GetReservation(ctx, "op_shop")
	if err != nil || rec.Operation.Project != spec.Project || rec.Operation.Repo != spec.Repo || rec.Leaf != spec.Leaf || rec.SpecHash != spec.Hash() || rec.Operation.Version != 1 {
		t.Fatalf("valid save lost binding: %+v %v", rec, err)
	}
}

func TestFoundationReservationRowHeaders(t *testing.T) {
	s, _ := foundationReservation(t)
	row, err := s.db.reader().GetReservation(context.Background(), "op_shop")
	if err != nil {
		t.Fatal(err)
	}
	for _, field := range []string{"project", "repo", "leaf", "hash"} {
		t.Run(field, func(t *testing.T) {
			changed := row
			switch field {
			case "project":
				changed.ProjectID = "prj_other"
			case "repo":
				changed.RepoID = "rep_other"
			case "leaf":
				changed.Leaf = "shop-other"
			case "hash":
				changed.SpecHash = strings.Repeat("0", 64)
			}
			if _, err := reservationFrom(changed); err == nil {
				t.Fatal("header inconsistent with immutable spec accepted")
			}
		})
	}
}
