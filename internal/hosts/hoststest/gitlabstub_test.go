package hoststest_test

import (
	"context"
	"encoding/json"
	"net/http"
	"net/url"
	"strings"
	"testing"
	"time"

	corehosts "github.com/brendatamaar/yggdrasil/internal/core/hosts"
	"github.com/brendatamaar/yggdrasil/internal/hosts"
	"github.com/brendatamaar/yggdrasil/internal/hosts/gitlab"
	"github.com/brendatamaar/yggdrasil/internal/hosts/hoststest"
)

const host = "https://git.acme-shop.example"

func do(t *testing.T, method, u string, form url.Values) (int, map[string]any) {
	t.Helper()
	req, err := http.NewRequestWithContext(context.Background(), method, u, strings.NewReader(form.Encode()))
	if err != nil {
		t.Fatal(err)
	}
	if form != nil {
		req.Header.Set("Content-Type", "application/x-www-form-urlencoded")
	}
	resp, err := http.DefaultClient.Do(req)
	if err != nil {
		t.Fatal(err)
	}
	defer resp.Body.Close()
	var m map[string]any
	_ = json.NewDecoder(resp.Body).Decode(&m)
	return resp.StatusCode, m
}

func mrsURL(s *hoststest.GitLabStub, project string) string {
	return s.Server.URL + "/api/v4/projects/" + url.PathEscape(project) + "/merge_requests"
}

func TestStubRecordsCreationOrderAndDescriptionUpdates(t *testing.T) {
	s := hoststest.NewGitLabStub(t, host)
	desc := "Part of acme-shop cart work. " + hoststest.LinkMarker
	for _, p := range []string{"acme-shop/shop-core", "acme-shop/shop-api", "acme-shop/shop-web"} {
		code, m := do(t, http.MethodPost, mrsURL(s, p), url.Values{"source_branch": {"ygg/cart"}, "title": {"Add cart"}, "description": {desc}})
		if code != http.StatusCreated || m["iid"] != float64(1) {
			t.Fatalf("create in %s = %d %v", p, code, m)
		}
	}
	if got := strings.Join(s.CreatedOrder(), ","); got != "acme-shop/shop-core,acme-shop/shop-api,acme-shop/shop-web" {
		t.Fatalf("creation order = %s", got)
	}
	filled := strings.Replace(desc, hoststest.LinkMarker, "shop-core (ygg/cart): "+s.WebURL("acme-shop/shop-core", 1), 1)
	code, m := do(t, http.MethodPut, mrsURL(s, "acme-shop/shop-api")+"/1", url.Values{"description": {filled}})
	if code != http.StatusOK || m["description"] != filled {
		t.Fatalf("update = %d %v", code, m)
	}
	mr, ok := s.MR("acme-shop/shop-api", 1)
	if !ok || strings.Contains(mr.Description, hoststest.LinkMarker) || !strings.Contains(mr.Description, "/acme-shop/shop-core/-/merge_requests/1") {
		t.Fatalf("stored description = %q", mr.Description)
	}
	var updates []hoststest.StubRequest
	for _, r := range s.Requests() {
		if r.Kind == "update_mr" {
			updates = append(updates, r)
		}
	}
	if len(updates) != 1 || updates[0].Description != filled || updates[0].IID != 1 {
		t.Fatalf("recorded updates = %+v", updates)
	}
	for _, r := range s.Requests() {
		if r.Kind == "create_mr" && !strings.Contains(r.Description, hoststest.LinkMarker) {
			t.Errorf("create %s lost the marker", r.Project)
		}
	}
}

func TestStubNumbersPerProjectAndRejectsBadRequests(t *testing.T) {
	s := hoststest.NewGitLabStub(t, host)
	_, a := do(t, http.MethodPost, mrsURL(s, "acme-shop/shop-api"), url.Values{"source_branch": {"a"}, "title": {"t"}})
	_, b := do(t, http.MethodPost, mrsURL(s, "acme-shop/shop-api"), url.Values{"source_branch": {"b"}, "title": {"t"}})
	_, c := do(t, http.MethodPost, mrsURL(s, "acme-shop/shop-web"), url.Values{"source_branch": {"a"}, "title": {"t"}})
	if a["iid"] != float64(1) || b["iid"] != float64(2) || c["iid"] != float64(1) {
		t.Fatalf("iids = %v %v %v", a["iid"], b["iid"], c["iid"])
	}
	if code, _ := do(t, http.MethodPost, mrsURL(s, "acme-shop/shop-web"), url.Values{"title": {"t"}}); code != http.StatusBadRequest {
		t.Errorf("create without source_branch = %d", code)
	}
	if code, _ := do(t, http.MethodGet, mrsURL(s, "acme-shop/shop-web")+"/99", nil); code != http.StatusNotFound {
		t.Errorf("unknown MR = %d", code)
	}
	if code, _ := do(t, http.MethodPut, mrsURL(s, "acme-shop/shop-web")+"/99", url.Values{"description": {"x"}}); code != http.StatusNotFound {
		t.Errorf("update of unknown MR = %d", code)
	}
}

// The stub is shaped like GitLab: the real GitLab client reads MR and pipeline
// state from it.
func TestStubIsReadableByGitLabClient(t *testing.T) {
	s := hoststest.NewGitLabStub(t, host)
	do(t, http.MethodPost, mrsURL(s, "acme-shop/shop-api"), url.Values{"source_branch": {"ygg/cart"}, "title": {"Add cart"}})
	s.SetPipeline("acme-shop/shop-api", "ygg/cart", "running")
	g := hosts.NewGetter(nil, func() time.Time { return hoststest.ContractNow })
	inst := corehosts.Instance{ID: "git.acme-shop.example", Kind: corehosts.KindGitLab, Base: s.Server.URL, Token: true}
	st, _, err := gitlab.New(g).RepoState(context.Background(), inst, "tok", "acme-shop/shop-api", "ygg/cart", "")
	if err != nil {
		t.Fatal(err)
	}
	if st.MR == nil || st.MR.Number != 1 || st.MR.Title != "Add cart" || st.MR.URL != s.WebURL("acme-shop/shop-api", 1) {
		t.Fatalf("MR = %+v", st.MR)
	}
	if st.Pipeline == nil || st.Pipeline.Status != corehosts.PipelineRunning {
		t.Fatalf("pipeline = %+v", st.Pipeline)
	}
	st, _, err = gitlab.New(g).RepoState(context.Background(), inst, "tok", "acme-shop/shop-api", "other", "")
	if err != nil || st.MR != nil || st.Pipeline != nil {
		t.Fatalf("other branch = %+v, %v", st, err)
	}
}
