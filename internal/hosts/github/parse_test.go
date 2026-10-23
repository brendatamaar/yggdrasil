package github

import (
	"testing"

	corehosts "github.com/brendatamaar/yggdrasil/internal/core/hosts"
)

const okPull = `{"number":1,"title":"t","state":"open","updated_at":"2026-10-12T09:30:00Z","html_url":"https://h/u","head":{"ref":"a"},"base":{"ref":"main"}}`

func TestParsePullsRejectsEachRule(t *testing.T) {
	cases := map[string]string{
		"not json":      `{`,
		"no number":     `[{"title":"t","state":"open","updated_at":"2026-10-12T09:30:00Z","html_url":"https://h/u","head":{"ref":"a"},"base":{"ref":"b"}}]`,
		"no title":      `[{"number":1,"state":"open","updated_at":"2026-10-12T09:30:00Z","html_url":"https://h/u","head":{"ref":"a"},"base":{"ref":"b"}}]`,
		"unknown state": `[{"number":1,"title":"t","state":"merged","updated_at":"2026-10-12T09:30:00Z","html_url":"https://h/u","head":{"ref":"a"},"base":{"ref":"b"}}]`,
		"no head":       `[{"number":1,"title":"t","state":"open","updated_at":"2026-10-12T09:30:00Z","html_url":"https://h/u","base":{"ref":"b"}}]`,
		"bad time":      `[{"number":1,"title":"t","state":"open","updated_at":"x","html_url":"https://h/u","head":{"ref":"a"},"base":{"ref":"b"}}]`,
	}
	for name, body := range cases {
		if got, err := parsePulls([]byte(body)); err == nil {
			t.Errorf("%s: accepted %+v", name, got)
		}
	}
	if got, err := parsePulls([]byte("[" + okPull + "]")); err != nil || got[0].State != corehosts.MROpen {
		t.Fatalf("ok pull = %+v, %v", got, err)
	}
}

func TestParsePipelineAggregate(t *testing.T) {
	run := func(status, concl string) string {
		c := "null"
		if concl != "" {
			c = `"` + concl + `"`
		}
		return `{"status":"` + status + `","conclusion":` + c + `,"html_url":"https://x/run","started_at":"2026-10-12T09:40:00Z","completed_at":"2026-10-12T09:45:00Z"}`
	}
	checks := func(rs ...string) []byte {
		s := `{"check_runs":[`
		for i, r := range rs {
			if i > 0 {
				s += ","
			}
			s += r
		}
		return []byte(s + `]}`)
	}
	status := func(state string, n int) []byte {
		st := ""
		if n > 0 {
			st = `{"state":"` + state + `","target_url":"https://x/status","updated_at":"2026-10-12T09:50:00Z"}`
		}
		return []byte(`{"state":"` + state + `","total_count":` + string(rune('0'+n)) + `,"statuses":[` + st + `]}`)
	}
	cases := []struct {
		name   string
		checks []byte
		status []byte
		want   corehosts.PipelineStatus
		none   bool
	}{
		{"all success", checks(run("completed", "success")), nil, corehosts.PipelineSuccess, false},
		{"one failure wins", checks(run("completed", "success"), run("completed", "failure"), run("in_progress", "")), nil, corehosts.PipelineFailed, false},
		{"timed out is failed", checks(run("completed", "timed_out")), nil, corehosts.PipelineFailed, false},
		{"pending is running", checks(run("completed", "success"), run("queued", "")), nil, corehosts.PipelineRunning, false},
		{"status failure", checks(run("completed", "success")), status("failure", 1), corehosts.PipelineFailed, false},
		{"status error", nil, status("error", 1), corehosts.PipelineFailed, false},
		{"status pending", checks(run("completed", "success")), status("pending", 1), corehosts.PipelineRunning, false},
		{"status only success", nil, status("success", 1), corehosts.PipelineSuccess, false},
		{"empty pending status ignored", checks(run("completed", "success")), status("pending", 0), corehosts.PipelineSuccess, false},
		{"all canceled", checks(run("completed", "cancelled")), nil, corehosts.PipelineCanceled, false},
		{"all skipped", checks(run("completed", "skipped"), run("completed", "neutral")), nil, corehosts.PipelineSkipped, false},
		{"nothing", checks(), status("pending", 0), "", true},
		{"no bodies", nil, nil, "", true},
	}
	for _, c := range cases {
		p, err := parsePipeline(c.checks, c.status, "https://fallback")
		if err != nil {
			t.Errorf("%s: %v", c.name, err)
			continue
		}
		if c.none {
			if p != nil {
				t.Errorf("%s: got %+v, want none", c.name, p)
			}
			continue
		}
		if p == nil || p.Status != c.want || p.URL == "" {
			t.Errorf("%s: got %+v, want %s", c.name, p, c.want)
		}
	}
	for _, bad := range [][2][]byte{
		{[]byte(`{"check_runs":[{"status":"completed","conclusion":"bogus"}]}`), nil},
		{[]byte(`{"check_runs":[{"conclusion":"success"}]}`), nil},
		{nil, []byte(`{"state":"weird","total_count":1,"statuses":[{}]}`)},
		{[]byte(`nope`), nil},
	} {
		if p, err := parsePipeline(bad[0], bad[1], ""); err == nil {
			t.Errorf("accepted %s %s as %+v", bad[0], bad[1], p)
		}
	}
}

func TestMergedAndDraft(t *testing.T) {
	merged := `[{"number":1,"title":"t","state":"closed","merged_at":"2026-10-12T09:30:00Z","updated_at":"2026-10-12T09:30:00Z","html_url":"https://h/u","head":{"ref":"a"},"base":{"ref":"b"}}]`
	closed := `[{"number":1,"title":"t","state":"closed","merged_at":null,"updated_at":"2026-10-12T09:30:00Z","html_url":"https://h/u","head":{"ref":"a"},"base":{"ref":"b"}}]`
	draft := `[{"number":1,"title":"t","state":"open","draft":true,"updated_at":"2026-10-12T09:30:00Z","html_url":"https://h/u","head":{"ref":"a"},"base":{"ref":"b"}}]`
	for body, want := range map[string]corehosts.MRState{merged: corehosts.MRMerged, closed: corehosts.MRClosed, draft: corehosts.MRDraft} {
		got, err := parsePulls([]byte(body))
		if err != nil || got[0].State != want {
			t.Errorf("%s: %+v, %v; want %s", body, got, err, want)
		}
	}
}

func TestRepoFromAPIURL(t *testing.T) {
	if repoFromAPIURL("https://api.github.com/repos/acme-shop/shop-api") != "acme-shop/shop-api" {
		t.Error("plain")
	}
	for _, bad := range []string{"", "https://x/repos/a", "https://x/repos/a/b/c", "https://x/repos//b", "https://x/other/a/b"} {
		if repoFromAPIURL(bad) != "" {
			t.Errorf("accepted %q", bad)
		}
	}
}

func FuzzParsePulls(f *testing.F) {
	f.Add([]byte("[" + okPull + "]"))
	f.Fuzz(func(t *testing.T, b []byte) {
		mrs, err := parsePulls(b)
		if err != nil {
			return
		}
		for _, m := range mrs {
			if m.State.Validate() != nil || m.Number <= 0 || m.URL == "" || m.Source == "" {
				t.Fatalf("invalid MR %+v", m)
			}
		}
	})
}

func FuzzParsePipeline(f *testing.F) {
	f.Add([]byte(`{"check_runs":[{"status":"completed","conclusion":"success"}]}`), []byte(`{"state":"success","total_count":1,"statuses":[{}]}`))
	f.Fuzz(func(t *testing.T, a, b []byte) {
		if p, err := parsePipeline(a, b, "https://h/u"); err == nil && p != nil && (p.Status.Validate() != nil || p.URL == "") {
			t.Fatalf("invalid pipeline %+v", p)
		}
	})
}

func FuzzParseSearch(f *testing.F) {
	f.Add([]byte(`{"items":[{"number":1,"title":"t","state":"open","updated_at":"2026-10-12T09:30:00Z","html_url":"https://h/u","repository_url":"https://x/repos/a/b"}]}`))
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
	const fallback = "https://h/acme/actions"
	for _, bad := range []string{"javascript:alert(1)", "data:text/html,x", "//h/x", "file:///etc/passwd"} {
		body := `[{"number":1,"title":"t","state":"open","updated_at":"2026-10-12T09:30:00Z","html_url":"` + bad + `","head":{"ref":"a"},"base":{"ref":"b"}}]`
		if got, err := parsePulls([]byte(body)); err == nil {
			t.Errorf("PR with %q accepted: %+v", bad, got)
		}
		checks := `{"check_runs":[{"status":"completed","conclusion":"failure","html_url":"` + bad + `","started_at":"2026-10-12T09:40:00Z","completed_at":"2026-10-12T09:45:00Z"}]}`
		status := `{"state":"failure","total_count":1,"statuses":[{"target_url":"` + bad + `","state":"failure","updated_at":"2026-10-12T09:45:00Z"}]}`
		p, err := parsePipeline([]byte(checks), []byte(status), fallback)
		if err != nil || p == nil || p.URL != fallback {
			t.Errorf("pipeline with %q = %+v, %v; want the fallback link", bad, p, err)
		}
	}
}
