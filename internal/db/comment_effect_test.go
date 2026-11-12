package db

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"reflect"
	"strings"
	"testing"
)

func commentEffectFixture(t *testing.T) EffectRecord {
	t.Helper()
	r := commentReceiptFixture()
	kind, raw, err := EncodeCommentReceipt(r)
	if err != nil {
		t.Fatal(err)
	}
	return EffectRecord{ID: EffectID("prp_shop", "", 1), Proposal: "prp_shop", Attempt: 1, State: EffectSucceeded, ReceiptKind: kind, Receipt: json.RawMessage(raw), Marker: r.Marker, RemoteID: r.CommentID, CreatedAt: at0}
}

func insertCommentIntent(t *testing.T, s *Store, e EffectRecord) {
	t.Helper()
	e.State = EffectIntent
	e.ReceiptKind = ""
	e.Receipt = nil
	e.RemoteID = ""
	if err := s.Commit(context.Background(), func(tx *Tx) error { return tx.InsertEffect(context.Background(), e) }); err != nil {
		t.Fatal(err)
	}
}

func settleCommentEffect(s *Store, e EffectRecord, version int64) error {
	return s.Commit(context.Background(), func(tx *Tx) error {
		_, err := tx.SettleEffect(context.Background(), e.ID, e.State, e.ReceiptKind, e.Receipt, e.RemoteID, version, at0)
		return err
	})
}

func assertEffectLoadRejected(t *testing.T, s *Store, e EffectRecord) {
	t.Helper()
	ctx := context.Background()
	if _, err := s.GetEffect(ctx, e.ID); err == nil {
		t.Error("GetEffect accepted invalid proof")
	}
	if _, err := s.GetEffectByKey(ctx, e.Proposal, e.Stage, e.Attempt); err == nil {
		t.Error("GetEffectByKey accepted invalid proof")
	}
	if _, err := s.ListEffectsByProposal(ctx, e.Proposal); err == nil {
		t.Error("ListEffectsByProposal accepted invalid proof")
	}
}

// AC-02, NR-01/04, SF-05: exercise the real SQLite boundary, including
// deliberately corrupted rows simulating persisted evidence from outside it.
func TestCommentEffectProofBoundaries(t *testing.T) {
	valid := commentEffectFixture(t)
	_, remote, err := EncodeReceipt(foundationReceipts()[6])
	if err != nil {
		t.Fatal(err)
	}
	cases := map[string]func(*EffectRecord){
		"marker mismatch":              func(e *EffectRecord) { e.Marker = strings.Repeat("d", 16) },
		"host id mismatch":             func(e *EffectRecord) { e.RemoteID = "C_other" },
		"new repo stage comment proof": func(e *EffectRecord) { e.Stage = "create_remote" },
		"comment stage new repo proof": func(e *EffectRecord) { e.ReceiptKind = "remote"; e.Receipt = json.RawMessage(remote) },
		"metadata absent":              func(e *EffectRecord) { e.ReceiptKind = "" },
		"metadata alien":               func(e *EffectRecord) { e.ReceiptKind = "alien" },
		"metadata remote":              func(e *EffectRecord) { e.ReceiptKind = "remote" },
		"body kind mismatch": func(e *EffectRecord) {
			e.Receipt = json.RawMessage(strings.Replace(string(e.Receipt), `"kind":"comment"`, `"kind":"remote"`, 1))
		},
	}
	for _, n := range []int{7, 39, 41, 63} {
		cases[fmt.Sprintf("short or partial head %d", n)] = func(e *EffectRecord) {
			e.Receipt = json.RawMessage(strings.Replace(string(e.Receipt), strings.Repeat("c", 40), strings.Repeat("c", n), 1))
		}
	}
	for _, state := range []EffectState{EffectIntent, EffectFailed, EffectUnknown} {
		cases[string(state)+" completed proof"] = func(e *EffectRecord) { e.State = state }
		cases[string(state)+" untyped proof"] = func(e *EffectRecord) { e.State = state; e.ReceiptKind = "" }
		cases[string(state)+" metadata only"] = func(e *EffectRecord) { e.State = state; e.Receipt = nil }
	}
	for name, change := range cases {
		for _, path := range []string{"insert", "settle", "load"} {
			t.Run(name+"/"+path, func(t *testing.T) {
				s := foundationEffectStore(t)
				ctx := context.Background()
				e := valid
				change(&e)
				e.ID = EffectID(e.Proposal, e.Stage, e.Attempt)
				if path == "insert" {
					if err := s.Commit(ctx, func(tx *Tx) error { return tx.InsertEffect(ctx, e) }); err == nil {
						t.Error("insert accepted invalid proof")
					}
					return
				}
				insertCommentIntent(t, s, e)
				before, err := s.GetEffect(ctx, e.ID)
				if err != nil {
					t.Fatal(err)
				}
				if path == "settle" {
					if err := settleCommentEffect(s, e, 0); err == nil {
						t.Error("settle accepted invalid proof")
					}
					after, err := s.GetEffect(ctx, e.ID)
					if err != nil {
						t.Fatal(err)
					}
					if !reflect.DeepEqual(before, after) {
						t.Error("rejected settlement changed persisted intent")
					}
					return
				}
				if _, err := s.db.write.ExecContext(ctx, `UPDATE proposal_effects SET state=?,receipt_kind=?,receipt=?,remote_id=?,marker=? WHERE id=?`, string(e.State), e.ReceiptKind, jsonText(e.Receipt, "{}"), e.RemoteID, e.Marker, e.ID); err != nil {
					// SQLite itself refuses absent metadata on completed rows. Other
					// invalid kinds reach the load parser and must be rejected there.
					if e.State == EffectSucceeded && e.ReceiptKind == "" && strings.Contains(err.Error(), "CHECK constraint failed") {
						return
					}
					t.Fatal(err)
				}
				assertEffectLoadRejected(t, s, e)
			})
		}
	}
}

func TestCommentEffectIntentSuccessRoundTrip(t *testing.T) {
	for _, headLen := range []int{40, 64} {
		t.Run(fmt.Sprint(headLen), func(t *testing.T) {
			s := foundationEffectStore(t)
			e := commentEffectFixture(t)
			e.Receipt = json.RawMessage(strings.Replace(string(e.Receipt), strings.Repeat("c", 40), strings.Repeat("c", headLen), 1))
			insertCommentIntent(t, s, e)
			if err := settleCommentEffect(s, e, 0); err != nil {
				t.Fatal(err)
			}
			got, err := s.GetEffect(context.Background(), e.ID)
			if err != nil || got.State != EffectSucceeded || got.Version != 1 || got.Marker != e.Marker || got.RemoteID != e.RemoteID || got.ReceiptKind != e.ReceiptKind || string(got.Receipt) != string(e.Receipt) {
				t.Fatalf("roundtrip: %+v %v", got, err)
			}
			proof, err := DecodeCommentReceipt(string(got.Receipt))
			if err != nil || !commentEvidenceValid(proof) {
				t.Fatalf("loaded invalid proof: %#v %v", proof, err)
			}
			byKey, err := s.GetEffectByKey(context.Background(), e.Proposal, "", 1)
			if err != nil || !reflect.DeepEqual(byKey, got) {
				t.Fatalf("key lookup: %+v %v", byKey, err)
			}
		})
	}
}

func TestCommentEffectSettlementTransitions(t *testing.T) {
	states := []EffectState{EffectIntent, EffectUnknown, EffectSucceeded, EffectFailed}
	for _, from := range states {
		for _, to := range states {
			t.Run(string(from)+" to "+string(to), func(t *testing.T) {
				s := foundationEffectStore(t)
				e := commentEffectFixture(t)
				insertCommentIntent(t, s, e)
				version := int64(0)
				if from != EffectIntent {
					first := e
					first.State = from
					if from != EffectSucceeded {
						first.ReceiptKind = ""
						first.Receipt = nil
						first.RemoteID = ""
					}
					if err := settleCommentEffect(s, first, 0); err != nil {
						t.Fatal(err)
					}
					version = 1
				}
				before, err := s.GetEffect(context.Background(), e.ID)
				if err != nil {
					t.Fatal(err)
				}
				next := e
				next.State = to
				if to != EffectSucceeded {
					next.ReceiptKind = ""
					next.Receipt = nil
					next.RemoteID = ""
				}
				err = settleCommentEffect(s, next, version)
				allowed := (from == EffectIntent || from == EffectUnknown) && to != EffectIntent
				if allowed && err != nil {
					t.Fatalf("legal settlement: %v", err)
				}
				if !allowed && err == nil {
					t.Error("illegal settlement accepted")
				}
				if (from == EffectSucceeded || from == EffectFailed) && !errors.Is(err, ErrConflict) {
					t.Errorf("terminal settlement must conflict: %v", err)
				}
				after, loadErr := s.GetEffect(context.Background(), e.ID)
				if loadErr != nil {
					t.Fatal(loadErr)
				}
				if !allowed {
					if !reflect.DeepEqual(before, after) {
						t.Error("illegal settlement changed row")
					}
					return
				}
				if after.State != to || after.Version != version+1 {
					t.Fatalf("wrong settlement: %+v", after)
				}
				if to == EffectSucceeded {
					proof, err := DecodeCommentReceipt(string(after.Receipt))
					if err != nil || !commentEvidenceValid(proof) {
						t.Fatalf("invalid completed proof: %v", err)
					}
				} else if after.ReceiptKind != "" || string(after.Receipt) != "{}" {
					t.Errorf("unfinished proof: %+v", after)
				}
				if err := settleCommentEffect(s, next, version); !errors.Is(err, ErrConflict) {
					t.Errorf("stale settlement must conflict: %v", err)
				}
			})
		}
	}
}
