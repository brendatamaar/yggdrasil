package gitea

import (
	"testing"

	corehosts "github.com/brendatamaar/yggdrasil/internal/core/hosts"
)

const okPull = `{"number":1,"title":"t","state":"open","updated_at":"2026-10-12T09:30:00Z","html_url":"https://h/u","head":{"ref":"a"},"base":{"ref":"main"}}`

func TestParsePullsRejectsEachRule(t *testing.T) {
	cases := map[string]string{
		"not json":      `{`,
		"no number":     `[{"title":"t","state":"open","updated_at":"2026-10-12T09:30:00Z","html_url":"https://h/u","head":{"ref":"a"},"base":{"ref":"b"}}]`,
		"unknown state": `[{"number":1,"title":"t","state":"all","updated_at":"2026-10-12T09:30:00Z","html_url":"https://h/u","head":{"ref":"a"},"base":{"ref":"b"}}]`,
		"no head":       `[{"number":1,"title":"t","state":"open","updated_at":"2026-10-12T09:30:00Z","html_url":"https://h/u","base":{"ref":"b"}}]`,
		"bad time":      `[{"number":1,"title":"t","state":"open","updated_at":"x","html_url":"https://h/u","head":{"ref":"a"},"base":{"ref":"b"}}]`,
	}
	for name, body := range cases {
		if got, err := parsePulls([]byte(body), "a"); err == nil {
			t.Errorf("%s: accepted %+v", name, got)
		}
	}
	got, err := parsePulls([]byte("["+okPull+"]"), "other")
	if err != nil || len(got) != 0 {
		t.Fatalf("a PR of another branch was kept: %+v, %v", got, err)
	}
}

func TestStateAndDraftMapping(t *testing.T) {
	mk := func(extra string) string {
		return `[{"number":1,"title":"` + extra + `","state":"STATE","updated_at":"2026-10-12T09:30:00Z","html_url":"https://h/u","head":{"ref":"a"},"base":{"ref":"b"},"merged":MERGED}]`
	}
	_ = mk
	cases := []struct {
		body string
		want corehosts.MRState
	}{
		{`[{"number":1,"title":"t","state":"open","merged":false,"updated_at":"2026-10-12T09:30:00Z","html_url":"https://h/u","head":{"ref":"a"},"base":{"ref":"b"}}]`, corehosts.MROpen},
		{`[{"number":1,"title":"WIP: t","state":"open","merged":false,"updated_at":"2026-10-12T09:30:00Z","html_url":"https://h/u","head":{"ref":"a"},"base":{"ref":"b"}}]`, corehosts.MRDraft},
		{`[{"number":1,"title":"t","state":"closed","merged":true,"updated_at":"2026-10-12T09:30:00Z","html_url":"https://h/u","head":{"ref":"a"},"base":{"ref":"b"}}]`, corehosts.MRMerged},
		{`[{"number":1,"title":"t","state":"closed","merged":false,"updated_at":"2026-10-12T09:30:00Z","html_url":"https://h/u","head":{"ref":"a"},"base":{"ref":"b"}}]`, corehosts.MRClosed},
	}
	for _, c := range cases {
		got, err := parsePulls([]byte(c.body), "a")
		if err != nil || got[0].State != c.want {
			t.Errorf("%s: %+v, %v; want %s", c.body, got, err, c.want)
		}
	}
}

func TestParseStatus(t *testing.T) {
	cases := map[string]corehosts.PipelineStatus{
		"success": corehosts.PipelineSuccess, "pending": corehosts.PipelineRunning, "failure": corehosts.PipelineFailed,
		"error": corehosts.PipelineFailed, "warning": corehosts.PipelineSuccess, "odd": corehosts.PipelineUnknown,
	}
	for state, want := range cases {
		p, err := parseStatus([]byte(`{"state":"` + state + `","statuses":[{"target_url":"u","updated_at":"2026-10-12T09:45:00Z"}]}`))
		if err != nil || p == nil || p.Status != want {
			t.Errorf("%s: %+v, %v", state, p, err)
		}
	}
	if p, err := parseStatus([]byte(`{"state":"pending","statuses":[]}`)); err != nil || p != nil {
		t.Errorf("no statuses = %+v, %v", p, err)
	}
	if _, err := parseStatus([]byte(`{"state":"success","statuses":[{"updated_at":"bad"}]}`)); err == nil {
		t.Error("bad time accepted")
	}
}

func FuzzParsePulls(f *testing.F) {
	f.Add([]byte("["+okPull+"]"), "a")
	f.Fuzz(func(t *testing.T, b []byte, branch string) {
		mrs, err := parsePulls(b, branch)
		if err != nil {
			return
		}
		for _, m := range mrs {
			if m.State.Validate() != nil || m.Source != branch || m.URL == "" {
				t.Fatalf("invalid MR %+v", m)
			}
		}
	})
}

func FuzzParseStatus(f *testing.F) {
	f.Add([]byte(`{"state":"success","statuses":[{"target_url":"u"}]}`))
	f.Fuzz(func(t *testing.T, b []byte) {
		if p, err := parseStatus(b); err == nil && p != nil && p.Status.Validate() != nil {
			t.Fatalf("invalid pipeline %+v", p)
		}
	})
}

func FuzzParseSearch(f *testing.F) {
	f.Add([]byte(`[{"number":1,"title":"t","state":"open","updated_at":"2026-10-12T09:30:00Z","html_url":"https://h/u","repository":{"full_name":"a/b"}}]`))
	f.Fuzz(func(t *testing.T, b []byte) {
		items, err := parseSearch(b, corehosts.RoleAuthored)
		if err != nil {
			return
		}
		for _, it := range items {
			if it.Repo == "" || it.MR.State.Validate() != nil {
				t.Fatalf("invalid item %+v", it)
			}
		}
	})
}

func TestUnsafeURLs(t *testing.T) {
	for _, bad := range []string{"javascript:alert(1)", "data:text/html,x", "//h/x", "file:///etc/passwd"} {
		body := `[{"number":1,"title":"t","state":"open","updated_at":"2026-10-12T09:30:00Z","html_url":"` + bad + `","head":{"ref":"a"},"base":{"ref":"b"}}]`
		if got, err := parsePulls([]byte(body), "a"); err == nil {
			t.Errorf("PR with %q accepted: %+v", bad, got)
		}
		p, err := parseStatus([]byte(`{"state":"success","statuses":[{"target_url":"` + bad + `","updated_at":"2026-10-12T09:45:00Z"}]}`))
		if err != nil || p == nil || p.URL != "" {
			t.Errorf("status with %q = %+v, %v; want a blank URL", bad, p, err)
		}
	}
}
