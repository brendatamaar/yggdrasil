package github

import (
	"encoding/json"
	"reflect"
	"testing"
	"time"

	corehosts "github.com/brendatamaar/yggdrasil/internal/core/hosts"
)

// formatPull writes a pull request the way the host sends it.
func formatPull(m corehosts.MergeRequest) map[string]any {
	state := "open"
	var mergedAt any
	switch m.State {
	case corehosts.MRClosed:
		state = "closed"
	case corehosts.MRMerged:
		state, mergedAt = "closed", m.UpdatedAt.Format(time.RFC3339Nano)
	}
	return map[string]any{"number": m.Number, "title": m.Title, "html_url": m.URL, "state": state,
		"draft": m.State == corehosts.MRDraft, "merged_at": mergedAt, "updated_at": m.UpdatedAt.Format(time.RFC3339Nano),
		"user": map[string]string{"login": m.Author}, "head": map[string]string{"ref": m.Source}, "base": map[string]string{"ref": m.Target}}
}

func TestPullRoundTrip(t *testing.T) {
	at := time.Date(2026, 10, 12, 9, 30, 0, 0, time.UTC)
	var want []corehosts.MergeRequest
	var raw []map[string]any
	for i, st := range []corehosts.MRState{corehosts.MROpen, corehosts.MRDraft, corehosts.MRMerged, corehosts.MRClosed} {
		m := corehosts.MergeRequest{Number: i + 1, Title: "Add cart \"quotes\" é", URL: "https://github.com/acme-shop/shop-web/pull/1",
			Author: "ada", Source: "feature/cart", Target: "main", State: st, UpdatedAt: at.Add(time.Duration(i) * time.Minute)}
		want = append(want, m)
		raw = append(raw, formatPull(m))
	}
	body, _ := json.Marshal(raw)
	got, err := parsePulls(body)
	if err != nil || !reflect.DeepEqual(got, want) {
		t.Fatalf("round trip = %+v, %v\nwant %+v", got, err, want)
	}
}
