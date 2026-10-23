package gitlab

import (
	"testing"

	corehosts "github.com/brendatamaar/yggdrasil/internal/core/hosts"
	"github.com/brendatamaar/yggdrasil/internal/hosts"
)

const okMR = `{"iid":1,"title":"t","state":"opened","updated_at":"2026-10-12T09:30:00Z","source_branch":"a","target_branch":"main","web_url":"https://h/x/-/merge_requests/1"}`

func TestParseMRsRejectsEachRule(t *testing.T) {
	cases := map[string]string{
		"not json":      `{`,
		"not a list":    `{}`,
		"no iid":        `[{"title":"t","state":"opened","updated_at":"2026-10-12T09:30:00Z","source_branch":"a","target_branch":"b","web_url":"https://h/u"}]`,
		"zero iid":      `[{"iid":0,"title":"t","state":"opened","updated_at":"2026-10-12T09:30:00Z","source_branch":"a","target_branch":"b","web_url":"https://h/u"}]`,
		"no title":      `[{"iid":1,"state":"opened","updated_at":"2026-10-12T09:30:00Z","source_branch":"a","target_branch":"b","web_url":"https://h/u"}]`,
		"no url":        `[{"iid":1,"title":"t","state":"opened","updated_at":"2026-10-12T09:30:00Z","source_branch":"a","target_branch":"b"}]`,
		"unknown state": `[{"iid":1,"title":"t","state":"weird","updated_at":"2026-10-12T09:30:00Z","source_branch":"a","target_branch":"b","web_url":"https://h/u"}]`,
		"no state":      `[{"iid":1,"title":"t","updated_at":"2026-10-12T09:30:00Z","source_branch":"a","target_branch":"b","web_url":"https://h/u"}]`,
		"bad time":      `[{"iid":1,"title":"t","state":"opened","updated_at":"yesterday","source_branch":"a","target_branch":"b","web_url":"https://h/u"}]`,
		"no branches":   `[{"iid":1,"title":"t","state":"opened","updated_at":"2026-10-12T09:30:00Z","web_url":"https://h/u"}]`,
	}
	for name, body := range cases {
		if got, err := parseMRs([]byte(body)); err == nil {
			t.Errorf("%s: accepted %+v", name, got)
		}
	}
	got, err := parseMRs([]byte("[" + okMR + "]"))
	if err != nil || len(got) != 1 || got[0].State != corehosts.MROpen {
		t.Fatalf("ok MR = %+v, %v", got, err)
	}
	if got, err := parseMRs(nil); err != nil || got != nil {
		t.Errorf("empty body = %v, %v", got, err)
	}
}

func TestPipelineStatusMapping(t *testing.T) {
	cases := map[string]corehosts.PipelineStatus{
		"created": corehosts.PipelinePending, "pending": corehosts.PipelinePending, "running": corehosts.PipelineRunning,
		"success": corehosts.PipelineSuccess, "failed": corehosts.PipelineFailed, "canceled": corehosts.PipelineCanceled,
		"skipped": corehosts.PipelineSkipped, "manual": corehosts.PipelineUnknown, "brand_new": corehosts.PipelineUnknown,
	}
	for in, want := range cases {
		body := `[{"status":"` + in + `","web_url":"https://h/u","updated_at":"2026-10-12T09:45:00Z"}]`
		p, err := parsePipelines([]byte(body))
		if err != nil || p.Status != want {
			t.Errorf("%s: %+v, %v; want %s", in, p, err, want)
		}
	}
	for _, bad := range []string{`[{"status":"","web_url":"https://h/u","updated_at":"2026-10-12T09:45:00Z"}]`, `[{"status":"success","updated_at":"2026-10-12T09:45:00Z"}]`, `nope`} {
		if _, err := parsePipelines([]byte(bad)); err == nil {
			t.Errorf("accepted %s", bad)
		}
	}
}

func TestDraftFlag(t *testing.T) {
	mrs, err := parseMRs([]byte(`[{"iid":1,"title":"t","state":"opened","draft":true,"updated_at":"2026-10-12T09:30:00Z","source_branch":"a","target_branch":"b","web_url":"https://h/u"}]`))
	if err != nil || mrs[0].State != corehosts.MRDraft {
		t.Fatalf("%+v, %v", mrs, err)
	}
}

func FuzzParseMRs(f *testing.F) {
	f.Add([]byte("[" + okMR + "]"))
	f.Add([]byte(`[]`))
	f.Fuzz(func(t *testing.T, b []byte) {
		mrs, err := parseMRs(b)
		if err != nil {
			return
		}
		for _, m := range mrs { // accepted => valid
			if m.State.Validate() != nil || m.Number <= 0 || m.URL == "" || m.UpdatedAt.IsZero() {
				t.Fatalf("invalid MR %+v", m)
			}
		}
	})
}

func FuzzParsePipelines(f *testing.F) {
	f.Add([]byte(`[{"status":"success","web_url":"https://h/u","updated_at":"2026-10-12T09:45:00Z"}]`))
	f.Fuzz(func(t *testing.T, b []byte) {
		if p, err := parsePipelines(b); err == nil && p != nil && (p.Status.Validate() != nil || hosts.SafeWebURL(p.URL) != p.URL) {
			t.Fatalf("invalid pipeline %+v", p)
		}
	})
}

func FuzzParseInbox(f *testing.F) {
	f.Add([]byte(`[{"iid":1,"title":"t","state":"opened","updated_at":"2026-10-12T09:30:00Z","web_url":"https://h/u","references":{"full":"a/b!1"}}]`))
	f.Fuzz(func(t *testing.T, b []byte) {
		items, err := parseInbox(b, corehosts.RoleAssigned)
		if err != nil {
			return
		}
		for _, it := range items {
			if it.Repo == "" || it.MR.State.Validate() != nil || it.Role.Validate() != nil {
				t.Fatalf("invalid item %+v", it)
			}
		}
	})
}

func TestUnsafeURLs(t *testing.T) {
	for _, bad := range []string{"javascript:alert(1)", "data:text/html,x", "//h/x", "file:///etc/passwd"} {
		body := `[{"iid":1,"title":"t","state":"opened","updated_at":"2026-10-12T09:30:00Z","source_branch":"a","target_branch":"b","web_url":"` + bad + `"}]`
		if got, err := parseMRs([]byte(body)); err == nil {
			t.Errorf("MR with %q accepted: %+v", bad, got)
		}
		p, err := parsePipelines([]byte(`[{"status":"success","web_url":"` + bad + `","updated_at":"2026-10-12T09:45:00Z"}]`))
		if err != nil || p == nil || p.URL != "" {
			t.Errorf("pipeline with %q = %+v, %v; want a blank URL", bad, p, err)
		}
	}
}
