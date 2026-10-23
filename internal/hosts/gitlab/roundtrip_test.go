package gitlab

import (
	"encoding/json"
	"reflect"
	"testing"
	"time"

	corehosts "github.com/brendatamaar/yggdrasil/internal/core/hosts"
)

// formatMR writes a merge request the way the host sends it.
func formatMR(m corehosts.MergeRequest) map[string]any {
	state := map[corehosts.MRState]string{corehosts.MROpen: "opened", corehosts.MRDraft: "opened", corehosts.MRMerged: "merged", corehosts.MRClosed: "closed"}[m.State]
	return map[string]any{"iid": m.Number, "title": m.Title, "web_url": m.URL, "state": state,
		"draft": m.State == corehosts.MRDraft, "updated_at": m.UpdatedAt.Format(time.RFC3339Nano),
		"source_branch": m.Source, "target_branch": m.Target, "author": map[string]string{"username": m.Author}}
}

// parse(format(x)) == x for every MR state.
func TestMRRoundTrip(t *testing.T) {
	at := time.Date(2026, 10, 12, 9, 30, 0, 0, time.UTC)
	var want []corehosts.MergeRequest
	var raw []map[string]any
	for i, st := range []corehosts.MRState{corehosts.MROpen, corehosts.MRDraft, corehosts.MRMerged, corehosts.MRClosed} {
		m := corehosts.MergeRequest{Number: i + 1, Title: "Add cart \"quotes\" é", URL: "https://git.acme.dev/acme-shop/shop-api/-/merge_requests/1",
			Author: "ada", Source: "feature/cart", Target: "main", State: st, UpdatedAt: at.Add(time.Duration(i) * time.Minute)}
		want = append(want, m)
		raw = append(raw, formatMR(m))
	}
	body, err := json.Marshal(raw)
	if err != nil {
		t.Fatal(err)
	}
	got, err := parseMRs(body)
	if err != nil || !reflect.DeepEqual(got, want) {
		t.Fatalf("round trip = %+v, %v\nwant %+v", got, err, want)
	}
}

func TestPipelineRoundTrip(t *testing.T) {
	at := time.Date(2026, 10, 12, 9, 45, 0, 0, time.UTC)
	body, _ := json.Marshal([]map[string]any{{"status": "failed", "web_url": "https://git.acme.dev/acme-shop/shop-api/-/pipelines/7", "updated_at": at.Format(time.RFC3339Nano)}})
	p, err := parsePipelines(body)
	if err != nil || p == nil || p.Status != corehosts.PipelineFailed || p.URL != "https://git.acme.dev/acme-shop/shop-api/-/pipelines/7" || !p.UpdatedAt.Equal(at) {
		t.Fatalf("pipeline = %+v, %v", p, err)
	}
}
