package gitea

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
	if m.State == corehosts.MRClosed || m.State == corehosts.MRMerged {
		state = "closed"
	}
	return map[string]any{"number": m.Number, "title": m.Title, "html_url": m.URL, "state": state,
		"merged": m.State == corehosts.MRMerged, "draft": m.State == corehosts.MRDraft, "updated_at": m.UpdatedAt.Format(time.RFC3339Nano),
		"user": map[string]string{"login": m.Author}, "head": map[string]string{"ref": m.Source}, "base": map[string]string{"ref": m.Target}}
}

func TestPullRoundTrip(t *testing.T) {
	at := time.Date(2026, 10, 12, 9, 30, 0, 0, time.UTC)
	var want []corehosts.MergeRequest
	var raw []map[string]any
	for i, st := range []corehosts.MRState{corehosts.MROpen, corehosts.MRDraft, corehosts.MRMerged, corehosts.MRClosed} {
		m := corehosts.MergeRequest{Number: i + 1, Title: "Add cart \"quotes\" é", URL: "https://code.example.org/acme-shop/shop-admin/pulls/1",
			Author: "ada", Source: "feature/cart", Target: "main", State: st, UpdatedAt: at.Add(time.Duration(i) * time.Minute)}
		want = append(want, m)
		raw = append(raw, formatPull(m))
	}
	body, _ := json.Marshal(raw)
	got, err := parsePulls(body, "feature/cart")
	if err != nil || !reflect.DeepEqual(got, want) {
		t.Fatalf("round trip = %+v, %v\nwant %+v", got, err, want)
	}
}
