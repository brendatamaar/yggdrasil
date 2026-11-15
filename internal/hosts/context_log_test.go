package hosts

import (
	"context"
	"encoding/json"
	"fmt"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"

	corehosts "github.com/brendatamaar/yggdrasil/internal/core/hosts"
)

func TestContextSignedLogRedirectAllowlist(t *testing.T) {
	signed := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.Header.Get("Authorization") != "" || r.Header.Get("PRIVATE-TOKEN") != "" {
			t.Error("host token forwarded")
		}
		if r.URL.Query().Get("signature") != "fictional" {
			t.Error("signed query lost")
		}
		fmt.Fprint(w, "log text")
	}))
	defer signed.Close()
	own := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.Header.Get("Authorization") != "Bearer sentinel-secret" {
			t.Error("missing own token")
		}
		http.Redirect(w, r, signed.URL+"/log?signature=fictional", http.StatusFound)
	}))
	defer own.Close()
	g, err := NewGetterWithLogOrigins(own.Client(), nil, []string{signed.URL})
	if err != nil {
		t.Fatal(err)
	}
	result, err := g.GetLogBounded(context.Background(), own.URL, "sentinel-secret", AuthBearer, 100)
	if err != nil || string(result.Body) != "log text" {
		t.Fatal(string(result.Body), err)
	}
	g = NewGetter(own.Client(), nil)
	_, err = g.GetLogBounded(context.Background(), own.URL, "sentinel-secret", AuthBearer, 100)
	if err == nil || strings.Contains(err.Error(), "signature") || strings.Contains(err.Error(), signed.URL) {
		t.Fatal("redirect refused without sanitizing error", err)
	}
	for _, bad := range []string{"https://user:secret@logs.acme.test", "https://logs.acme.test/path", "http://logs.acme.test", "https://logs.acme.test?signature=x"} {
		if _, err := NewGetterWithLogOrigins(nil, nil, []string{bad}); err == nil {
			t.Fatal("invalid log origin accepted", bad)
		}
	}
}

func TestContextRedactionBoundsStructuralOverhead(t *testing.T) {
	labels := make([]string, 40000)
	result := redactContext(ContextResult{Value: corehosts.IssueDetail{Summary: corehosts.IssueSummary{Number: 1, Title: "sentinel-secret", Labels: labels}, Body: strings.Repeat("a", corehosts.ContextMaxPromptBytes)}}, "sentinel-secret")
	raw, err := json.Marshal(result)
	if err != nil {
		t.Fatal(err)
	}
	if len(raw) > corehosts.ContextMaxPromptBytes || strings.Contains(string(raw), "sentinel-secret") {
		t.Fatal("oversized or unredacted context", len(raw))
	}
	if !result.Value.(corehosts.IssueDetail).BodyTruncated {
		t.Fatal("silent truncation")
	}
}
