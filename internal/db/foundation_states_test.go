package db

import (
	"crypto/sha256"
	"encoding/json"
	"fmt"
	"reflect"
	"testing"

	"github.com/brendatamaar/yggdrasil/internal/core/newrepos"
)

// Exercise the same cases at both pure validators and the persistence boundary.
func checkFoundationState(t *testing.T, spec newrepos.InitSpec, st newrepos.State, valid bool) {
	t.Helper()
	for name, err := range map[string]error{"shape": newrepos.ValidateStateShape(st), "binding": newrepos.ValidateState(spec, st)} {
		if (err == nil) != valid {
			t.Errorf("%s valid=%v: %T %+v: %v", name, valid, st, st, err)
		}
	}
	code, stage, attempt, detail, err := encodeNewRepoState(st)
	if (err == nil) != valid {
		t.Errorf("encode valid=%v: %T: %v", valid, st, err)
	}
	if valid && err == nil {
		back, err := decodeNewRepoState(spec, code, stage, attempt, detail)
		if err != nil {
			t.Fatal(err)
		}
		c, s, a, d, err := encodeNewRepoState(back)
		if err != nil || c != code || s != stage || a != attempt || d != detail {
			t.Fatalf("state roundtrip changed: %T %v", st, err)
		}
	}
}

func TestFoundationRecoveryStagePrefixes(t *testing.T) {
	spec := newReposSpec("prj_shop", "rep_shop", "shop-new")
	pub := foundationPublish(spec)
	receipts := foundationReceipts()
	receipts[2] = newrepos.FileReceipt{Name: "README.md", ContentHash: fmt.Sprintf("%x", sha256.Sum256([]byte(spec.README)))}
	receipts[3] = newrepos.FileReceipt{Name: ".gitignore", ContentHash: fmt.Sprintf("%x", sha256.Sum256([]byte(spec.Ignore)))}
	stages := []newrepos.Stage{}
	for _, s := range newrepos.InitStages() {
		stages = append(stages, s)
	}
	for _, s := range newrepos.PublishStages() {
		stages = append(stages, s)
	}
	for _, stage := range stages {
		t.Run(stage.Name(), func(t *testing.T) {
			var prefix []newrepos.Receipt
			var next newrepos.Receipt
			switch v := stage.(type) {
			case newrepos.InitStage:
				for i, s := range newrepos.InitStages() {
					if s == v {
						prefix = receipts[:i]
						next = receipts[i]
					}
				}
			case newrepos.PublishStage:
				for i, s := range newrepos.PublishStages() {
					if s == v {
						prefix = receipts[6 : 6+i]
						next = receipts[6+i]
					}
				}
			}
			variants := map[string][]newrepos.Receipt{"legal": prefix, "extra": append(append([]newrepos.Receipt{}, prefix...), next)}
			if len(prefix) > 0 {
				variants["missing"] = prefix[:len(prefix)-1]
				bad := append([]newrepos.Receipt{}, prefix...)
				bad[0] = newrepos.TrackedReceipt{}
				variants["wrong variant"] = bad
			}
			if len(prefix) > 1 {
				bad := append([]newrepos.Receipt{}, prefix...)
				bad[0], bad[1] = bad[1], bad[0]
				variants["reordered"] = bad
			}
			if len(prefix) > 2 {
				bad := append([]newrepos.Receipt{}, prefix...)
				bad[2] = receipts[3]
				variants["wrong file"] = bad
			}
			for name, done := range variants {
				t.Run(name, func(t *testing.T) {
					var active newrepos.State
					var resume newrepos.Resumable
					switch v := stage.(type) {
					case newrepos.InitStage:
						active = newrepos.Initializing{Spec: spec, Attempt: 1, Stage: v, Done: done}
						resume = newrepos.ResumeInit{Spec: spec, Done: done}
					case newrepos.PublishStage:
						active = newrepos.Publishing{Spec: pub, Attempt: 1, Stage: v, Done: done}
						resume = newrepos.ResumePublish{Spec: pub, Done: done}
					}
					valid := name == "legal"
					for _, st := range []newrepos.State{active, newrepos.Stopped{Stage: stage, Reason: newrepos.StopStageFailed, Resume: resume}, newrepos.Uncertain{Stage: stage, Attempt: 1, Resume: resume}} {
						checkFoundationState(t, spec, st, valid)
					}
					// Build wire data without the encoder, so removed decoder validation is
					// independently detectable even while the encoder still rejects bad states.
					raw, err := encodeReceipts(done)
					if err != nil {
						t.Fatal(err)
					}
					d := detailJSON{Receipts: raw}
					code := "initializing"
					if _, ok := stage.(newrepos.PublishStage); ok {
						code = "publishing"
						d.Publish = &pub
					}
					b, err := json.Marshal(d)
					if err != nil {
						t.Fatal(err)
					}
					if _, err := decodeNewRepoState(spec, code, stage.Name(), 1, string(b)); (err == nil) != valid {
						t.Errorf("decode valid=%v: %v", valid, err)
					}
					for _, kind := range []string{"stopped", "uncertain"} {
						r := &resumeJSON{Kind: "init", Receipts: raw}
						if code == "publishing" {
							r.Kind = "publish"
							r.Publish = &pub
						}
						b, err := json.Marshal(detailJSON{Reason: string(newrepos.StopStageFailed), Resume: r})
						if err != nil {
							t.Fatal(err)
						}
						attempt := 0
						if kind == "uncertain" {
							attempt = 1
						}
						if _, err := decodeNewRepoState(spec, kind, stage.Name(), attempt, string(b)); (err == nil) != valid {
							t.Errorf("decode %s valid=%v: %v", kind, valid, err)
						}
					}
				})
			}
		})
	}
}

func TestFoundationRecoveryRejectsMissingResumeAndAttempts(t *testing.T) {
	spec := newReposSpec("prj_shop", "rep_shop", "shop-new")
	pub := foundationPublish(spec)
	bad := []newrepos.State{
		newrepos.Initializing{Spec: spec, Attempt: 1, Stage: newrepos.InitStage(newrepos.StageCreateRemote)},
		newrepos.Publishing{Spec: pub, Attempt: 1, Stage: newrepos.PublishStage(newrepos.StageCreateDirectory)},
		newrepos.Initializing{Spec: spec, Attempt: -1, Stage: newrepos.StageCreateDirectory},
		newrepos.Publishing{Spec: pub, Attempt: -1, Stage: newrepos.StageCreateRemote},
		newrepos.Uncertain{Attempt: 1, Stage: newrepos.StageCreateDirectory, Resume: newrepos.ResumeInit{}},
		newrepos.Uncertain{Attempt: 1, Stage: newrepos.StageCreateRemote, Resume: newrepos.ResumePublish{}},

		newrepos.Initializing{Spec: spec, Stage: newrepos.StageCreateDirectory},
		newrepos.Publishing{Spec: pub, Stage: newrepos.StageCreateRemote},
		newrepos.Uncertain{Stage: newrepos.StageCreateDirectory, Resume: newrepos.ResumeInit{Spec: spec}},
		newrepos.Uncertain{Attempt: -1, Stage: newrepos.StageCreateRemote, Resume: newrepos.ResumePublish{Spec: pub}},
		newrepos.Uncertain{Attempt: 1, Stage: newrepos.StageCreateRemote, Resume: newrepos.ResumeInit{Spec: spec}},
		newrepos.Uncertain{Attempt: 1, Stage: newrepos.StageCreateDirectory, Resume: newrepos.ResumePublish{Spec: pub}},
		newrepos.Uncertain{Attempt: 1, Stage: newrepos.StageCreateRemote, Resume: newrepos.ResumeNone{}},
		newrepos.Uncertain{Attempt: 1, Stage: newrepos.StageCreateRemote},
		newrepos.Stopped{Stage: newrepos.StageCreateDirectory, Reason: newrepos.StopStageFailed},
		newrepos.Stopped{Stage: newrepos.StageCreateRemote, Reason: newrepos.StopStageFailed, Resume: newrepos.ResumeInit{Spec: spec}},
		newrepos.Stopped{Stage: newrepos.StageCreateDirectory, Reason: newrepos.StopStageFailed, Resume: newrepos.ResumePublish{Spec: pub}},
		newrepos.Stopped{Stage: newrepos.StageCreateDirectory, Reason: newrepos.StopStageFailed, Resume: newrepos.ResumeInit{}},
		newrepos.Stopped{Stage: newrepos.StageCreateRemote, Reason: newrepos.StopStageFailed, Resume: newrepos.ResumePublish{}},
		newrepos.Initializing{Spec: spec, Attempt: 1, Stage: newrepos.StageFinalizeTracking},
	}
	for _, st := range bad {
		checkFoundationState(t, spec, st, false)
	}
}

func FuzzFoundationStateAcceptedImpliesValid(f *testing.F) {
	spec := newReposSpec("prj_shop", "rep_shop", "shop-new")
	f.Add("initializing", "finalize_tracking", 1, `{}`)
	f.Add("initializing", "create_directory", 1, `{}`)
	f.Add("reserved", "", 0, `{}`)
	f.Add("uncertain", "create_remote", 1, `{"resume":{"kind":"none"}}`)
	f.Fuzz(func(t *testing.T, code, stage string, attempt int, detail string) {
		st, err := decodeNewRepoState(spec, code, stage, attempt, detail)
		if err != nil {
			return
		}
		// Independently require an exact stage evidence prefix on accepted recovery.
		var done []newrepos.Receipt
		var active newrepos.Stage
		switch v := st.(type) {
		case newrepos.Initializing:
			if v.Attempt < 1 {
				t.Fatal("nonpositive attempt")
			}
			active = v.Stage
			done = v.Done
		case newrepos.Publishing:
			if v.Attempt < 1 || v.Spec.Repo != spec.Repo {
				t.Fatal("bad publishing scope/attempt")
			}
			active = v.Stage
			done = v.Done
		case newrepos.Uncertain:
			if v.Attempt < 1 {
				t.Fatal("nonpositive attempt")
			}
			active = v.Stage
			switch r := v.Resume.(type) {
			case newrepos.ResumeInit:
				done = r.Done
			case newrepos.ResumePublish:
				done = r.Done
			case newrepos.ResumeNone:
				t.Fatal("uncertain without resume")
			}
		case newrepos.Stopped:
			active = v.Stage
			switch r := v.Resume.(type) {
			case newrepos.ResumeInit:
				done = r.Done
			case newrepos.ResumePublish:
				done = r.Done
			case newrepos.ResumeNone:
				if active != nil {
					t.Fatal("stopped without resume")
				}
			}
		case newrepos.Reserved, newrepos.AwaitingConfirm, newrepos.Ready, newrepos.Published:
			return
		}
		if active == nil {
			return
		}
		names := map[string]int{"create_directory": 0, "init_git": 1, "write_readme": 2, "write_ignore": 3, "first_commit": 4, "finalize_tracking": 5, "create_remote": 0, "add_origin": 1, "push_head": 2}
		n, ok := names[active.Name()]
		if !ok || len(done) != n {
			t.Fatalf("illegal evidence count: %s %#v", stage, done)
		}
		receipts := foundationReceipts()
		offset := 0
		if code == "publishing" {
			offset = 6
		} else if _, ok := active.(newrepos.PublishStage); ok {
			offset = 6
		}
		for i, r := range done {
			if !receiptEvidenceValid(r) || reflect.TypeOf(r) != reflect.TypeOf(receipts[offset+i]) {
				t.Fatalf("invalid receipt prefix: %#v", done)
			}
			if file, ok := r.(newrepos.FileReceipt); ok && file.Name != receipts[offset+i].(newrepos.FileReceipt).Name {
				t.Fatal("wrong file order")
			}
		}
	})
}
