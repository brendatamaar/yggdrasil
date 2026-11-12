package db

// These regressions intentionally fail on f98a8c2. The lead owns the serial
// production repairs; do not skip these tests or relax their evidence checks.
import (
	"context"
	"crypto/sha256"
	"encoding/json"
	"fmt"
	"strings"
	"testing"

	"github.com/brendatamaar/yggdrasil/internal/core/newrepos"
)

func TestFoundationDefectUncertainNeedsStageAndResume(t *testing.T) {
	s, spec := foundationReservation(t)
	st := newrepos.Uncertain{Attempt: 1, Resume: newrepos.ResumeNone{}}
	checkFoundationState(t, spec, st, false)
	if err := s.Commit(context.Background(), func(tx *Tx) error {
		_, err := tx.SaveReservation(context.Background(), "op_shop", st, "", 0, at0)
		return err
	}); err == nil {
		t.Error("saved uncertain without a stage or original resume spec")
	}
}

func TestFoundationDefectContradictoryRecoveryEvidence(t *testing.T) {
	spec := newReposSpec("prj_shop", "rep_shop", "shop-new")
	pub := foundationPublish(spec)
	remote := newrepos.RemoteReceipt{RemoteID: "R_shop", URL: "https://git.acme.example/acme/shop-api.git"}
	origin := newrepos.OriginReceipt{URL: "https://git.acme.example/acme/shop-other.git"}
	checkFoundationState(t, spec, newrepos.Publishing{Spec: pub, Attempt: 1, Stage: newrepos.StagePushHead, Done: []newrepos.Receipt{remote, origin}}, false)
	rs := foundationReceipts()[:4]
	rs[2] = newrepos.FileReceipt{Name: "README.md", ContentHash: fmt.Sprintf("%x", sha256.Sum256([]byte(spec.README)))}
	rs[3] = newrepos.FileReceipt{Name: ".gitignore", ContentHash: fmt.Sprintf("%x", sha256.Sum256([]byte(spec.Ignore)))}
	for _, index := range []int{2, 3} {
		t.Run(fmt.Sprint(index), func(t *testing.T) {
			done := append([]newrepos.Receipt{}, rs...)
			file := done[index].(newrepos.FileReceipt)
			file.ContentHash = strings.Repeat("0", 64)
			done[index] = file
			checkFoundationState(t, spec, newrepos.Initializing{Spec: spec, Attempt: 1, Stage: newrepos.StageFirstCommit, Done: done}, false)
			raw, err := encodeReceipts(done)
			if err != nil {
				t.Fatal(err)
			}
			b, err := json.Marshal(detailJSON{Receipts: raw})
			if err != nil {
				t.Fatal(err)
			}
			if _, err := decodeNewRepoState(spec, "initializing", "first_commit", 1, string(b)); err == nil {
				t.Error("decoded file proof unrelated to immutable content")
			}
		})
	}
	raw, err := encodeReceipts([]newrepos.Receipt{remote, origin})
	if err != nil {
		t.Fatal(err)
	}
	b, err := json.Marshal(detailJSON{Publish: &pub, Receipts: raw})
	if err != nil {
		t.Fatal(err)
	}
	if _, err := decodeNewRepoState(spec, "publishing", "push_head", 1, string(b)); err == nil {
		t.Error("decoded contradictory remote/origin URLs")
	}
}

func TestFoundationDefectForeignStateAndResumeFields(t *testing.T) {
	spec := newReposSpec("prj_shop", "rep_shop", "shop-new")
	for _, detail := range []string{`{"head":""}`, `{"head":"unrelated"}`, `{"receipts":[]}`, `{"publish":null}`} {
		if _, err := decodeNewRepoState(spec, "reserved", "", 0, detail); err == nil {
			t.Errorf("reserved accepted foreign data: %s", detail)
		}
	}
	for _, detail := range []string{
		`{"reason":"rejected","resume":{"kind":"none","receipts":[]}}`,
		`{"reason":"rejected","resume":{"kind":"none","receipts":[{"kind":"tracked"}]}}`,
		`{"reason":"rejected","resume":{"kind":"none","publish":null}}`,
		`{"reason":"rejected","resume":{"kind":"none"},"head":""}`,
	} {
		if _, err := decodeNewRepoState(spec, "stopped", "", 0, detail); err == nil {
			t.Errorf("stopped accepted foreign data: %s", detail)
		}
	}
	pub := foundationPublish(spec)
	data, err := json.Marshal(pub)
	if err != nil {
		t.Fatal(err)
	}
	detail := `{"reason":"rejected","resume":{"kind":"none","publish":` + string(data) + `}}`
	if _, err := decodeNewRepoState(spec, "stopped", "", 0, detail); err == nil {
		t.Error("ResumeNone accepted foreign publish spec")
	}
}

func foundationEffectStore(t *testing.T) *Store {
	t.Helper()
	s := NewStore(openTest(t), nil)
	if _, err := s.db.write.ExecContext(context.Background(), `INSERT INTO proposals (id,kind,status,summary,created_at) VALUES ('prp_shop','publish','pending','Publish shop',`+ts+`)`); err != nil {
		t.Fatal(err)
	}
	return s
}

func TestFoundationDefectEffectProofBoundary(t *testing.T) {
	_, remote, err := EncodeReceipt(foundationReceipts()[6])
	if err != nil {
		t.Fatal(err)
	}
	_, origin, err := EncodeReceipt(foundationReceipts()[7])
	if err != nil {
		t.Fatal(err)
	}
	cases := []struct{ name, stage, kind, proof string }{
		{"empty remote", "create_remote", "remote", `{}`},
		{"malformed remote", "create_remote", "remote", `{"kind":"remote"}`},
		{"metadata mismatch", "create_remote", "origin", remote},
		{"stage mismatch", "create_remote", "origin", origin},
	}
	for _, c := range cases {
		for _, path := range []string{"insert", "settle", "load"} {
			t.Run(c.name+"/"+path, func(t *testing.T) {
				s := foundationEffectStore(t)
				ctx := context.Background()
				e := EffectRecord{Proposal: "prp_shop", Stage: c.stage, Attempt: 1, CreatedAt: at0, State: EffectIntent}
				if path == "insert" {
					e.State = EffectSucceeded
					e.ReceiptKind = c.kind
					e.Receipt = json.RawMessage(c.proof)
					if err := s.Commit(ctx, func(tx *Tx) error { return tx.InsertEffect(ctx, e) }); err == nil {
						t.Error("insert accepted invalid effect proof")
					}
					return
				}
				if err := s.Commit(ctx, func(tx *Tx) error { return tx.InsertEffect(ctx, e) }); err != nil {
					t.Fatal(err)
				}
				id := EffectID(e.Proposal, e.Stage, e.Attempt)
				if path == "settle" {
					if err := s.Commit(ctx, func(tx *Tx) error {
						_, err := tx.SettleEffect(ctx, id, EffectSucceeded, c.kind, json.RawMessage(c.proof), "R_shop", 0, at0)
						return err
					}); err == nil {
						t.Error("settlement accepted invalid effect proof")
					}
					return
				}
				if _, err := s.db.write.ExecContext(ctx, `UPDATE proposal_effects SET state='succeeded',receipt_kind=?,receipt=? WHERE id=?`, c.kind, c.proof, id); err != nil {
					t.Fatal(err)
				}
				if _, err := s.GetEffect(ctx, id); err == nil {
					t.Error("load accepted invalid effect proof")
				}
			})
		}
	}
	t.Run("valid roundtrip", func(t *testing.T) {
		s := foundationEffectStore(t)
		ctx := context.Background()
		e := EffectRecord{Proposal: "prp_shop", Stage: "create_remote", Attempt: 1, CreatedAt: at0}
		if err := s.Commit(ctx, func(tx *Tx) error { return tx.InsertEffect(ctx, e) }); err != nil {
			t.Fatal(err)
		}
		id := EffectID(e.Proposal, e.Stage, e.Attempt)
		if err := s.Commit(ctx, func(tx *Tx) error {
			_, err := tx.SettleEffect(ctx, id, EffectSucceeded, "remote", json.RawMessage(remote), "R_shop", 0, at0)
			return err
		}); err != nil {
			t.Fatal(err)
		}
		got, err := s.GetEffect(ctx, id)
		if err != nil || got.ReceiptKind != "remote" || string(got.Receipt) != remote || got.Version != 1 {
			t.Fatalf("valid roundtrip: %+v %v", got, err)
		}
	})
}
