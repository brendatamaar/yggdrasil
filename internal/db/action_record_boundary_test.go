package db

import (
	"context"
	"crypto/sha256"
	"database/sql"
	"encoding/hex"
	"errors"
	"fmt"
	"reflect"
	"strings"
	"testing"
	"unicode/utf8"

	"github.com/brendatamaar/yggdrasil/internal/core"
	"github.com/brendatamaar/yggdrasil/internal/core/actions"
	"github.com/brendatamaar/yggdrasil/internal/db/gen"
)

// These predicates deliberately use no production validators or parsers. Keep
// the documented wire rules here so relaxing a production check is detectable.
func independentRequestValid(r actions.Request) bool {
	id := func(s string) bool { return s != "" && !strings.ContainsAny(s, " \t\r\n/\\") }
	plain := func(s string) bool {
		for _, c := range s {
			switch {
			case c >= 'a' && c <= 'z', c >= 'A' && c <= 'Z', c >= '0' && c <= '9', c == '.', c == '_', c == '-':
			default:
				return false
			}
		}
		return true
	}
	if !id(string(r.RequestID)) || len(r.RequestID) > 100 || !id(string(r.RepoID)) {
		return false
	}
	source, agent := false, true
	switch string(r.Kind) {
	case "diagnose_pipeline", "fix_pipeline", "review_mr", "import_issue", "diagnose_build":
		source = true
	case "propose_commits", "diagnose_repo", "propose_ignore":
	case "pull_and_check":
		agent = false
	default:
		return false
	}
	if len(r.SourceID) > 64 || !plain(r.SourceID) || (r.SourceID != "") != source {
		return false
	}
	if agent && r.Agent == "" || len(r.Agent) > 64 || !plain(r.Agent) {
		return false
	}
	return len(r.Model) <= 100 && utf8.ValidString(r.Model) && !strings.ContainsFunc(r.Model, func(c rune) bool { return c < 32 || c == 127 })
}

func independentStatusValid(state, refusal, key string) bool {
	switch state {
	case "preparing", "ready", "completed", "cancelled":
		return refusal == ""
	case "submitting":
		return refusal == "" && key != ""
	case "refused":
		switch refusal {
		case "invalid_request", "untracked", "no_offering", "scope_changed", "capability_missing", "stale_head", "digest_mismatch", "admission_refused", "prepare_failed", "absent_after_lost_reply":
			return true
		}
	}
	return false
}

func independentDigest(r actions.Request) string {
	var b strings.Builder
	for _, v := range []string{"ygg-action-request-v1", string(r.RepoID), string(r.Kind), r.SourceID, r.Agent, r.Model} {
		fmt.Fprintf(&b, "%d:%s;", len(v), v)
	}
	h := sha256.Sum256([]byte(b.String()))
	return hex.EncodeToString(h[:])
}

func independentRecordValid(r ActionRecord) bool {
	return independentRequestValid(r.Request) && independentStatusValid(string(r.State), string(r.Refusal), r.Key) && r.Digest == independentDigest(r.Request)
}

func TestActionStatusBeginSaveContract(t *testing.T) {
	states := []string{"preparing", "ready", "submitting", "completed", "refused", "cancelled", "unknown", ""}
	reasons := []string{"", "invalid_request", "untracked", "no_offering", "scope_changed", "capability_missing", "stale_head", "digest_mismatch", "admission_refused", "prepare_failed", "absent_after_lost_reply", "unknown"}
	for _, state := range states {
		for _, refusal := range reasons {
			for _, key := range []string{"", "action:req:1"} {
				t.Run(fmt.Sprintf("%s/%s/key=%t", state, refusal, key != ""), func(t *testing.T) {
					ctx := context.Background()
					d := openTest(t)
					s := NewStore(d, nil)
					_, repo := seed(t, s)
					survivor := replayFixture(t, s, repo.ID)
					req := actions.Request{RequestID: "req_matrix", RepoID: repo.ID, Kind: actions.DiagnoseBuild, SourceID: "build_1", Agent: "mock", Model: "model-a"}
					rec := ActionRecord{Request: req, Digest: independentDigest(req), State: ActionState(state), Refusal: actions.RefuseReason(refusal), Key: key, Attempt: 1, CreatedAt: at0}
					want := independentRecordValid(rec)
					var begun ActionRecord
					var created bool
					err := s.Commit(ctx, func(tx *Tx) error {
						if err := tx.AppendAudit(ctx, core.AuditEntry{ID: "audit_matrix", At: at0, Actor: "user", Action: "submit", Subject: "acme-shop"}); err != nil {
							return err
						}
						var err error
						begun, created, err = tx.BeginActionRequest(ctx, rec)
						return err
					})
					if (err == nil) != want {
						t.Fatalf("begin accepted=%v want=%v: %v", err == nil, want, err)
					}
					if !want {
						if n := count(t, d, "SELECT count(*) FROM audit_log"); n != 0 {
							t.Fatalf("collateral audit survived: %d", n)
						}
						if _, err := s.GetActionRequest(ctx, req.RequestID); !errors.Is(err, ErrNotFound) {
							t.Fatalf("rejected request survived: %v", err)
						}
					} else {
						if !created || !independentRecordValid(begun) || begun.Request != req {
							t.Fatalf("bad begin: %+v", begun)
						}
						assertActionReplay(t, s, rec, 0)
					}
					// Save must apply the identical status contract to a known valid row.
					candidate := survivor
					candidate.State = rec.State
					candidate.Refusal = rec.Refusal
					candidate.Key = rec.Key
					err = s.Commit(ctx, func(tx *Tx) error {
						if err := tx.AppendAudit(ctx, core.AuditEntry{ID: "audit_save", At: at0, Actor: "user", Action: "update", Subject: "acme-shop"}); err != nil {
							return err
						}
						_, err := tx.SaveActionRequest(ctx, candidate, 0, at0)
						return err
					})
					if (err == nil) != want {
						t.Fatalf("save accepted=%v want=%v: %v", err == nil, want, err)
					}
					if want {
						assertActionReplay(t, s, candidate, 1)
					} else {
						assertActionReplay(t, s, survivor, 0)
					}
					n := count(t, d, "SELECT count(*) FROM audit_log")
					if !want && n != 0 || want && n != 2 {
						t.Fatalf("audit rollback: %d", n)
					}
				})
			}
		}
	}
}

func assertActionReplay(t *testing.T, s *Store, want ActionRecord, version int64) {
	t.Helper()
	ctx := context.Background()
	check := func(got ActionRecord) {
		t.Helper()
		if !independentRecordValid(got) || got.Request != want.Request || got.Digest != want.Digest || got.State != want.State || got.Refusal != want.Refusal || got.Key != want.Key || got.Attempt != want.Attempt || got.Version != version {
			t.Fatalf("replay mismatch: %+v want %+v version %d", got, want, version)
		}
	}
	got, err := s.GetActionRequest(ctx, want.Request.RequestID)
	if err != nil {
		t.Fatal(err)
	}
	check(got)
	err = s.Commit(ctx, func(tx *Tx) error {
		got, created, err := tx.BeginActionRequest(ctx, want)
		if err == nil {
			if created {
				t.Fatal("duplicate created")
			}
			check(got)
		}
		return err
	})
	if err != nil {
		t.Fatal(err)
	}
	rows, err := s.ListUnsettledActionRequests(ctx)
	if err != nil {
		t.Fatal(err)
	}
	found := false
	for _, r := range rows {
		if r.Request.RequestID == want.Request.RequestID {
			found = true
			check(r)
		}
	}
	unsettled := want.State == ActionSubmitting
	if found != unsettled {
		t.Fatalf("unsettled membership %v want %v", found, unsettled)
	}
}

func actionBoundaryRow() gen.WorkspaceActionRequest {
	r := gen.WorkspaceActionRequest{RequestID: "req_decode", RepoID: "repo_decode", Kind: "diagnose_repo", Agent: sql.NullString{String: "mock", Valid: true}, Model: sql.NullString{String: "model-a", Valid: true}, State: "submitting", IdempotencyKey: "key", CreatedAt: fmtTime(at0), UpdatedAt: fmtTime(at0)}
	setBoundaryDigest(&r)
	return r
}
func boundaryRequest(r gen.WorkspaceActionRequest) actions.Request {
	return actions.Request{RequestID: core.RequestID(r.RequestID), RepoID: core.RepoID(r.RepoID), Kind: actions.Kind(r.Kind), SourceID: r.SourceID, Agent: r.Agent.String, Model: r.Model.String}
}
func setBoundaryDigest(r *gen.WorkspaceActionRequest) {
	r.Digest = independentDigest(boundaryRequest(*r))
}
func checkActionBoundary(t *testing.T, row gen.WorkspaceActionRequest) {
	t.Helper()
	want := row.Agent.Valid && row.Model.Valid && independentRequestValid(boundaryRequest(row)) && independentStatusValid(row.State, row.Refusal, row.IdempotencyKey) && row.Digest == independentDigest(boundaryRequest(row))
	got, err := actionFrom(row, nil)
	if (!row.Agent.Valid || !row.Model.Valid) && !errors.Is(err, ErrLegacyActionRequest) {
		t.Fatalf("nullable identity did not fail closed: %v", err)
	}
	if (err == nil) != want {
		t.Fatalf("decoder accepted=%v want=%v row=%+v err=%v", err == nil, want, row, err)
	}
	if err == nil && (!independentRecordValid(got) || got.Request != boundaryRequest(row) || got.Digest != row.Digest || string(got.State) != row.State || string(got.Refusal) != row.Refusal || got.Key != row.IdempotencyKey) {
		t.Fatalf("changed or invalid record: %+v", got)
	}
}

func TestActionDecoderRejectsEachRule(t *testing.T) {
	cases := map[string]func(*gen.WorkspaceActionRequest){
		"null-agent": func(r *gen.WorkspaceActionRequest) { r.Agent.Valid = false }, "null-model": func(r *gen.WorkspaceActionRequest) { r.Model.Valid = false },
		"empty-request": func(r *gen.WorkspaceActionRequest) { r.RequestID = "" }, "long-request": func(r *gen.WorkspaceActionRequest) { r.RequestID = strings.Repeat("a", 101) },
		"empty-repo": func(r *gen.WorkspaceActionRequest) { r.RepoID = "" }, "unknown-kind": func(r *gen.WorkspaceActionRequest) { r.Kind = "unknown" },
		"source-forbidden": func(r *gen.WorkspaceActionRequest) { r.SourceID = "build_1" }, "source-required": func(r *gen.WorkspaceActionRequest) { r.Kind = "diagnose_build" },
		"source-long":    func(r *gen.WorkspaceActionRequest) { r.Kind = "diagnose_build"; r.SourceID = strings.Repeat("a", 65) },
		"source-path":    func(r *gen.WorkspaceActionRequest) { r.Kind = "diagnose_build"; r.SourceID = "a/b" },
		"agent-required": func(r *gen.WorkspaceActionRequest) { r.Agent.String = "" }, "agent-long": func(r *gen.WorkspaceActionRequest) { r.Agent.String = strings.Repeat("a", 65) },
		"agent-path": func(r *gen.WorkspaceActionRequest) { r.Agent.String = "bad/agent" }, "optional-agent-invalid": func(r *gen.WorkspaceActionRequest) { r.Kind = "pull_and_check"; r.Agent.String = "bad/agent" },
		"model-long": func(r *gen.WorkspaceActionRequest) { r.Model.String = strings.Repeat("a", 101) }, "model-utf8": func(r *gen.WorkspaceActionRequest) { r.Model.String = string([]byte{255}) },
		"model-control": func(r *gen.WorkspaceActionRequest) { r.Model.String = "\n" }, "model-del": func(r *gen.WorkspaceActionRequest) { r.Model.String = "\x7f" },
	}
	for _, c := range []string{" ", "\t", "\r", "\n", "/", "\\"} {
		cases["request-character-"+fmt.Sprintf("%x", c)] = func(r *gen.WorkspaceActionRequest) { r.RequestID = "req" + c }
		cases["repo-character-"+fmt.Sprintf("%x", c)] = func(r *gen.WorkspaceActionRequest) { r.RepoID = "repo" + c }
	}
	for name, change := range cases {
		t.Run(name, func(t *testing.T) {
			row := actionBoundaryRow()
			change(&row)
			setBoundaryDigest(&row)
			if row.Agent.Valid && row.Model.Valid && independentRequestValid(boundaryRequest(row)) {
				t.Fatal("negative control is valid")
			}
			checkActionBoundary(t, row)
		})
	}
	t.Run("digest", func(t *testing.T) {
		row := actionBoundaryRow()
		row.Digest = strings.Repeat("0", 64)
		checkActionBoundary(t, row)
	})
	for _, state := range []string{"preparing", "ready", "submitting", "completed", "refused", "cancelled", "unknown", ""} {
		for _, reason := range []string{"", "invalid_request", "unknown"} {
			for _, key := range []string{"", "key"} {
				row := actionBoundaryRow()
				row.State = state
				row.Refusal = reason
				row.IdempotencyKey = key
				checkActionBoundary(t, row)
			}
		}
	}
}

func TestActionDecoderAllKindsRoundTrip(t *testing.T) {
	for _, kind := range []string{"diagnose_pipeline", "fix_pipeline", "review_mr", "import_issue", "diagnose_build", "propose_commits", "diagnose_repo", "propose_ignore", "pull_and_check"} {
		t.Run(kind, func(t *testing.T) {
			row := actionBoundaryRow()
			row.Kind = kind
			switch kind {
			case "diagnose_pipeline", "fix_pipeline", "review_mr", "import_issue", "diagnose_build":
				row.SourceID = "source_1"
			}
			if kind == "pull_and_check" {
				row.Agent.String = ""
			}
			row.Model.String = ""
			setBoundaryDigest(&row)
			if !independentRequestValid(boundaryRequest(row)) {
				t.Fatal("invalid fixture")
			}
			checkActionBoundary(t, row)
			got, err := actionFrom(row, nil)
			if err != nil || !reflect.DeepEqual(got.Request, boundaryRequest(row)) {
				t.Fatalf("roundtrip %+v %v", got, err)
			}
		})
	}
}

func FuzzActionReplayDecoder(f *testing.F) {
	f.Add("req_fuzz", "repo_fuzz", "diagnose_repo", "", "mock", "model-a", "submitting", "", "key", true, true, true, "")
	f.Add("bad id", "repo", "unknown", "a/b", "bad/agent", "\n", "refused", "unknown", "", false, false, false, "bad")
	for _, seed := range []struct {
		id, agent, model, state, refusal, key string
		agentValid, modelValid                bool
	}{
		{"bad id", "mock", "model-a", "submitting", "", "key", true, true},
		{"req_fuzz", "mock", "model-a", "submitting", "", "key", false, true},
		{"req_fuzz", "mock", "model-a", "submitting", "", "key", true, false},
		{"req_fuzz", "mock", "model-a", "completed", "untracked", "", true, true},
		{"req_fuzz", "mock", "model-a", "refused", "unknown", "", true, true},
		{"req_fuzz", "mock", "model-a", "submitting", "", "", true, true},
	} {
		f.Add(seed.id, "repo_fuzz", "diagnose_repo", "", seed.agent, seed.model, seed.state, seed.refusal, seed.key, seed.agentValid, seed.modelValid, true, "")
	}
	f.Fuzz(func(t *testing.T, id, repo, kind, source, agent, model, state, refusal, key string, agentValid, modelValid, matchingDigest bool, digest string) {
		row := actionBoundaryRow()
		row.RequestID = id
		row.RepoID = repo
		row.Kind = kind
		row.SourceID = source
		row.Agent = sql.NullString{String: agent, Valid: agentValid}
		row.Model = sql.NullString{String: model, Valid: modelValid}
		row.State = state
		row.Refusal = refusal
		row.IdempotencyKey = key
		row.Digest = digest
		if matchingDigest {
			setBoundaryDigest(&row)
		}
		checkActionBoundary(t, row)
		// Also perturb one field of a valid row, so the fuzzer explores close
		// neighbors instead of spending all its time on multiply invalid records.
		near := actionBoundaryRow()
		switch (len(id) + len(repo) + len(kind) + len(source) + len(agent) + len(model) + len(state)) % 10 {
		case 0:
			near.RequestID = id
		case 1:
			near.RepoID = repo
		case 2:
			near.Kind = kind
			near.SourceID = source
		case 3:
			near.SourceID = source
		case 4:
			near.Agent = sql.NullString{String: agent, Valid: agentValid}
		case 5:
			near.Model = sql.NullString{String: model, Valid: modelValid}
		case 6:
			near.State = state
		case 7:
			near.Refusal = refusal
		case 8:
			near.IdempotencyKey = key
		case 9:
			near.Kind = "pull_and_check"
			near.Agent.String = agent
		}
		setBoundaryDigest(&near)
		checkActionBoundary(t, near)
	})
}
