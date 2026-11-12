package db

import (
	"encoding/json"
	"testing"

	"github.com/brendatamaar/yggdrasil/internal/core/newrepos"
)

func TestFoundationPublishDestinationBinding(t *testing.T) {
	spec := newReposSpec("prj_shop", "rep_shop", "shop-new")
	pub := foundationPublish(spec)
	for name, url := range map[string]string{
		"confirmed":        "https://git.acme.example/acme/shop-new.git",
		"case insensitive": "https://GIT.ACME.EXAMPLE/ACME/SHOP-NEW.git",
		"wrong host":       "https://other.acme.example/acme/shop-new.git",
		"wrong owner":      "https://git.acme.example/other/shop-new.git",
		"wrong repo":       "https://git.acme.example/acme/shop-other.git",
	} {
		t.Run(name, func(t *testing.T) {
			valid := name == "confirmed" || name == "case insensitive"
			done := []newrepos.Receipt{newrepos.RemoteReceipt{RemoteID: "R_shop", URL: url}, newrepos.OriginReceipt{URL: url}}
			resume := newrepos.ResumePublish{Spec: pub, Done: done}
			for _, st := range []newrepos.State{
				newrepos.Publishing{Spec: pub, Attempt: 1, Stage: newrepos.StagePushHead, Done: done},
				newrepos.Stopped{Stage: newrepos.StagePushHead, Reason: newrepos.StopStageFailed, Resume: resume},
				newrepos.Uncertain{Stage: newrepos.StagePushHead, Attempt: 1, Resume: resume},
			} {
				checkFoundationState(t, spec, st, valid)
			}
			// Construct wire data independently: encoder rejection cannot mask a
			// decoder which trusts a mutually matching but foreign destination.
			raw, err := encodeReceipts(done)
			if err != nil {
				t.Fatal(err)
			}
			for _, kind := range []string{"publishing", "stopped", "uncertain"} {
				d := detailJSON{Publish: &pub, Receipts: raw}
				if kind != "publishing" {
					d = detailJSON{Reason: string(newrepos.StopStageFailed), Resume: &resumeJSON{Kind: "publish", Publish: &pub, Receipts: raw}}
				}
				b, err := json.Marshal(d)
				if err != nil {
					t.Fatal(err)
				}
				if _, err := decodeNewRepoState(spec, kind, "push_head", 1, string(b)); (err == nil) != valid {
					t.Errorf("decode %s valid=%v: %v", kind, valid, err)
				}
			}
		})
	}
}
