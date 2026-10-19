package hoststest

import (
	"context"
	"errors"
	"slices"
	"strings"
	"testing"
	"time"

	corehosts "github.com/brendatamaar/yggdrasil/internal/core/hosts"
	"github.com/brendatamaar/yggdrasil/internal/hosts"
)

// Harness is a HostClient under test and the way to put it in a scenario.
type Harness struct {
	Client   hosts.HostClient
	Instance corehosts.Instance
	// Token is a sentinel token. A client must send it only in a header.
	Token string
	// Use puts the client's world in the scenario (the fake is scripted, a
	// replay server switches directory).
	Use func(t *testing.T, s Scenario)
	// Requests returns what a replay server saw (nil for the fake).
	Requests func() []Recorded
}

const (
	project = corehosts.ProjectPath("acme-shop/shop-api")
	branch  = "feature/cart"
)

// HostClientContract is the suite every HostClient passes. newHarness is
// called once per scenario.
func HostClientContract(t *testing.T, newHarness func(t *testing.T) Harness) {
	t.Helper()
	ctx, cancel := context.WithTimeout(context.Background(), 30*time.Second)
	defer cancel()

	repo := func(t *testing.T, s Scenario, etag string) (corehosts.RepoState, string, Harness, error) {
		h := newHarness(t)
		h.Use(t, s)
		rs, next, err := h.Client.RepoState(ctx, h.Instance, h.Token, project, branch, etag)
		if h.Requests != nil {
			checkRequests(t, h)
		}
		return rs, next, h, err
	}
	wantMR := func(t *testing.T, got *corehosts.MergeRequest, state corehosts.MRState) {
		t.Helper()
		if got == nil {
			t.Fatalf("no MR, want one in state %s", state)
		}
		want := ExpectedMR(state)
		if got.Number != want.Number || got.Title != want.Title || got.Author != want.Author || got.Source != want.Source ||
			got.Target != want.Target || got.State != want.State || !got.UpdatedAt.Equal(want.UpdatedAt) || got.URL == "" {
			t.Errorf("MR = %+v, want %+v with a URL", *got, want)
		}
	}
	wantPipeline := func(t *testing.T, got *corehosts.Pipeline, st corehosts.PipelineStatus) {
		t.Helper()
		if got == nil {
			t.Fatalf("no pipeline, want %s", st)
		}
		want := ExpectedPipeline(st)
		if got.Status != want.Status || !got.UpdatedAt.Equal(want.UpdatedAt) || got.URL == "" {
			t.Errorf("pipeline = %+v, want %+v with a URL", *got, want)
		}
	}
	wantFail := func(t *testing.T, err error, kind corehosts.FailKind) *hosts.FetchError {
		t.Helper()
		var fe *hosts.FetchError
		if !errors.As(err, &fe) || fe.Kind != kind {
			t.Fatalf("error = %v, want a %s fetch error", err, kind)
		}
		return fe
	}

	t.Run("open MR with a successful pipeline", func(t *testing.T) {
		rs, etag, _, err := repo(t, OpenSuccess, "")
		if err != nil {
			t.Fatal(err)
		}
		wantMR(t, rs.MR, corehosts.MROpen)
		wantPipeline(t, rs.Pipeline, corehosts.PipelineSuccess)
		if etag == "" {
			t.Error("no ETag to send next time")
		}
	})
	t.Run("no MR", func(t *testing.T) {
		rs, _, _, err := repo(t, NoMR, "")
		if err != nil || rs.MR != nil {
			t.Fatalf("got %+v, %v", rs, err)
		}
		wantPipeline(t, rs.Pipeline, corehosts.PipelineSuccess)
	})
	t.Run("draft MR", func(t *testing.T) {
		rs, _, _, err := repo(t, Draft, "")
		if err != nil {
			t.Fatal(err)
		}
		wantMR(t, rs.MR, corehosts.MRDraft)
	})
	t.Run("merged MR", func(t *testing.T) {
		rs, _, _, err := repo(t, Merged, "")
		if err != nil {
			t.Fatal(err)
		}
		wantMR(t, rs.MR, corehosts.MRMerged)
	})
	t.Run("failed pipeline", func(t *testing.T) {
		rs, _, _, err := repo(t, PipelineFailed, "")
		if err != nil {
			t.Fatal(err)
		}
		wantMR(t, rs.MR, corehosts.MROpen)
		wantPipeline(t, rs.Pipeline, corehosts.PipelineFailed)
	})
	t.Run("no pipeline", func(t *testing.T) {
		rs, _, _, err := repo(t, NoPipeline, "")
		if err != nil || rs.Pipeline != nil {
			t.Fatalf("got %+v, %v", rs, err)
		}
		wantMR(t, rs.MR, corehosts.MROpen)
	})
	t.Run("304 with an ETag", func(t *testing.T) {
		_, _, _, err := repo(t, NotModified, `"v1"`)
		if !errors.Is(err, hosts.ErrNotModified) {
			t.Fatalf("error = %v, want ErrNotModified", err)
		}
	})
	t.Run("401 is unauthorized", func(t *testing.T) {
		_, _, _, err := repo(t, Unauthorized, "")
		_ = wantFail(t, err, corehosts.FailUnauthorized)
	})
	t.Run("404 is not found", func(t *testing.T) {
		_, _, _, err := repo(t, NotFound, "")
		_ = wantFail(t, err, corehosts.FailNotFound)
	})
	t.Run("429 with Retry-After is rate limited with RetryAt", func(t *testing.T) {
		_, _, _, err := repo(t, RateLimited429, "")
		fe := wantFail(t, err, corehosts.FailRateLimited)
		if fe.RetryAt == nil || !fe.RetryAt.Equal(ContractNow.Add(120*time.Second)) {
			t.Errorf("RetryAt = %v, want %v", fe.RetryAt, ContractNow.Add(120*time.Second))
		}
	})
	t.Run("403 with rate-limit headers is rate limited", func(t *testing.T) {
		_, _, _, err := repo(t, RateLimited403, "")
		fe := wantFail(t, err, corehosts.FailRateLimited)
		if fe.RetryAt == nil || !fe.RetryAt.Equal(ContractNow.Add(5*time.Minute)) {
			t.Errorf("RetryAt = %v, want %v", fe.RetryAt, ContractNow.Add(5*time.Minute))
		}
	})
	t.Run("500 is transient", func(t *testing.T) {
		_, _, _, err := repo(t, ServerError, "")
		_ = wantFail(t, err, corehosts.FailTransient)
	})
	t.Run("inbox roles", func(t *testing.T) {
		h := newHarness(t)
		h.Use(t, InboxRoles)
		items, err := h.Client.Inbox(ctx, h.Instance, h.Token)
		if err != nil {
			t.Fatal(err)
		}
		if h.Requests != nil {
			checkRequests(t, h)
		}
		type row struct {
			n           int
			title, repo string
			role        corehosts.Role
		}
		var got, want []row
		for _, it := range items {
			got = append(got, row{it.MR.Number, it.MR.Title, it.Repo, it.Role})
			if it.MR.URL == "" {
				t.Errorf("inbox MR %d has no URL", it.MR.Number)
			}
		}
		for _, it := range ExpectedInbox() {
			want = append(want, row{it.MR.Number, it.MR.Title, it.Repo, it.Role})
		}
		less := func(a, b row) int { return a.n - b.n }
		slices.SortFunc(got, less)
		slices.SortFunc(want, less)
		if !slices.Equal(got, want) {
			t.Errorf("inbox = %v, want %v", got, want)
		}
	})
	t.Run("inbox 401 is unauthorized", func(t *testing.T) {
		h := newHarness(t)
		h.Use(t, Unauthorized)
		_, err := h.Client.Inbox(ctx, h.Instance, h.Token)
		_ = wantFail(t, err, corehosts.FailUnauthorized)
	})
}

// checkRequests holds a replay server to S1 and invariant 4: only GET, and the
// token only in a header.
func checkRequests(t *testing.T, h Harness) {
	t.Helper()
	reqs := h.Requests()
	if len(reqs) == 0 {
		t.Error("the client sent no request")
	}
	for _, r := range reqs {
		if r.Method != "GET" {
			t.Errorf("%s %s: host clients must only GET", r.Method, r.URI)
		}
		if h.Token != "" && strings.Contains(r.URI, h.Token) {
			t.Errorf("the token is in a URL: %s", r.URI)
		}
		var inHeader bool
		for k, vs := range r.Header {
			for _, v := range vs {
				if strings.Contains(v, h.Token) {
					inHeader = true
					if k != "Authorization" && k != "Private-Token" {
						t.Errorf("the token is in header %s", k)
					}
				}
			}
		}
		if h.Token != "" && !inHeader {
			t.Errorf("%s %s: no token header was sent", r.Method, r.URI)
		}
	}
}
