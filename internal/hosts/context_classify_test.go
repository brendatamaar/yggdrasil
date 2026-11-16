package hosts

import (
	"context"
	"errors"
	"fmt"
	"net/http"
	"strings"
	"testing"

	corehosts "github.com/brendatamaar/yggdrasil/internal/core/hosts"
)

// FE S3: the provider boundary names deterministic failures, so the shell can
// refuse them instead of retrying.

// A just-pushed merge request has no GitLab diff version for its head yet. That
// is a temporary failure (the host catches up), not an unsupported host.
func TestContextGitLabMRWithoutDiffVersionIsRetryable(t *testing.T) {
	head := strings.Repeat("a", 40)
	srv, in, _ := contextFixture(t, func(w http.ResponseWriter, r *http.Request) {
		if strings.HasSuffix(r.URL.Path, "/versions") {
			fmt.Fprint(w, `[]`)
			return
		}
		fmt.Fprintf(w, `{"iid":3,"title":"Review","state":"opened","diff_refs":{"head_sha":%q}}`, head)
	})
	api := ContextAPI{Getter: NewGetter(srv.Client(), nil), Root: srv.URL, GitLab: true}
	_, err := api.MRDiff(context.Background(), in, "sentinel-secret", "acme/shop", 3)
	if err == nil {
		t.Fatal("a merge request without a diff version was accepted")
	}
	if errors.Is(err, ErrContextUnsupported) {
		t.Fatal("a just-pushed merge request was reported as unsupported")
	}
	var fe *FetchError
	if !errors.As(err, &fe) || fe.Kind != corehosts.FailTransient {
		t.Fatalf("want a retryable failure, got %v", err)
	}
	if _, refused := contextRefusal(err); refused {
		t.Fatal("the shell would refuse instead of retrying")
	}
}

func TestContextProviderNamesDeterministicFailures(t *testing.T) {
	for name, c := range map[string]struct {
		body string
		want error
	}{
		"invalid json":          {`{"number":`, ErrContextInvalid},
		"null body":             {`null`, ErrContextInvalid},
		"no issue identity":     {`{"number":0,"title":"x"}`, ErrContextInvalid},
		"no title":              {`{"number":1,"title":""}`, ErrContextInvalid},
		"pull request is wrong": {`{"number":1,"title":"x","pull_request":{}}`, ErrContextScope},
		"other issue number":    {`{"number":2,"title":"x"}`, ErrContextScope},
	} {
		t.Run(name, func(t *testing.T) {
			srv, in, _ := contextFixture(t, func(w http.ResponseWriter, r *http.Request) { fmt.Fprint(w, c.body) })
			api := ContextAPI{Getter: NewGetter(srv.Client(), nil), Root: srv.URL}
			_, err := api.Issue(context.Background(), in, "sentinel-secret", "acme/shop", 1)
			if !errors.Is(err, c.want) {
				t.Fatalf("got %v, want %v", err, c.want)
			}
			reason, refused := contextRefusal(err)
			if !refused {
				t.Fatalf("%v would be retried", err)
			}
			wantReason := corehosts.RefusedUnsupported
			if errors.Is(c.want, ErrContextScope) {
				wantReason = corehosts.RefusedNotFound
			}
			if reason != wantReason {
				t.Fatalf("reason %s, want %s", reason, wantReason)
			}
		})
	}
}

func TestContextRefusalLeavesTemporaryFailuresToTheCore(t *testing.T) {
	for _, err := range []error{
		fetchErr(corehosts.FailTransient, "503"),
		fetchErr(corehosts.FailRateLimited, "429"),
		context.DeadlineExceeded,
		errors.New("connection reset"),
	} {
		if reason, refused := contextRefusal(err); refused {
			t.Errorf("%v was refused as %s", err, reason)
		}
	}
}
