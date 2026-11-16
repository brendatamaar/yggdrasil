package hosts

import (
	"context"
	"crypto/sha256"
	"encoding/hex"
	"errors"
	"fmt"
	"net/http"
	"strings"
	"sync/atomic"
	"testing"

	corehosts "github.com/brendatamaar/yggdrasil/internal/core/hosts"
	coreproposals "github.com/brendatamaar/yggdrasil/internal/core/proposals"
)

// A resolver failure that is not a scope error (a database failure, say) must
// not be shown as "not tracked", and must stop the write before any POST.
func TestConfirmedResolverErrorsKeepTheirKind(t *testing.T) {
	head := strings.Repeat("a", 40)
	dbDown := errors.New("database is locked")
	for name, tc := range map[string]struct {
		resolve   func(call int) error
		wantScope bool
	}{
		"first lookup, infrastructure": {resolve: func(int) error { return dbDown }},
		"first lookup, scope":          {resolve: func(int) error { return fmt.Errorf("repo is not tracked: %w", ErrContextScope) }, wantScope: true},
		"recheck before POST, infra":   {resolve: func(n int) error { return pick(n >= 3, dbDown) }},
		"recheck before POST, scope":   {resolve: func(n int) error { return pick(n >= 3, fmt.Errorf("moved: %w", ErrContextScope)) }, wantScope: true},
	} {
		t.Run(name, func(t *testing.T) {
			var posts atomic.Int32
			srv, in, cfg := contextFixture(t, func(w http.ResponseWriter, r *http.Request) {
				if r.Method == http.MethodPost {
					posts.Add(1)
					w.WriteHeader(http.StatusCreated)
					return
				}
				fmt.Fprintf(w, `{"number":7,"head":{"sha":%q}}`, head)
			})
			var calls int
			resolve := contextResolverFunc(func(context.Context, corehosts.ContextKey) (ContextTarget, error) {
				calls++
				if err := tc.resolve(calls); err != nil {
					return ContextTarget{}, err
				}
				return ContextTarget{Instance: in.ID, Project: "acme/shop", Revision: "1"}, nil
			})
			h := NewConfirmedHosts(New(Deps{Config: cfg}), resolve, srv.Client())
			_, err := h.PostReviewComment(context.Background(), "rep_1", 7, head, "text", strings.Repeat("b", 16))
			if err == nil {
				t.Fatal("the write went ahead")
			}
			if got := errors.Is(err, ErrContextScope); got != tc.wantScope {
				t.Fatalf("errors.Is(ErrContextScope) = %v, want %v: %v", got, tc.wantScope, err)
			}
			if !tc.wantScope && !errors.Is(err, dbDown) {
				t.Fatalf("the cause was lost: %v", err)
			}
			if posts.Load() != 0 {
				t.Fatalf("POSTed %d times after a scope or resolver failure", posts.Load())
			}
		})
	}
}

func pick(fail bool, err error) error {
	if fail {
		return err
	}
	return nil
}

// Found and Absent are conclusive, so scope is proven again after the pages are
// read: a repo relinked while they were read must not yield a receipt or leave
// to act.
func TestFindReviewCommentRechecksScopeAfterReading(t *testing.T) {
	head := strings.Repeat("a", 40)
	marker := strings.Repeat("b", 16)
	body := "Review text"
	digest := sha256.Sum256([]byte(body))
	stored := body + "\n\n" + commentMarker(marker, head)
	for name, listing := range map[string]string{
		"found":  fmt.Sprintf(`[{"id":99,"body":%q}]`, stored),
		"absent": `[]`,
	} {
		t.Run(name, func(t *testing.T) {
			var read atomic.Bool
			srv, in, cfg := contextFixture(t, func(w http.ResponseWriter, r *http.Request) {
				if strings.Contains(r.URL.Path, "/pulls/") {
					fmt.Fprintf(w, `{"number":7,"head":{"sha":%q}}`, head)
					return
				}
				read.Store(true)
				fmt.Fprint(w, listing)
			})
			resolve := contextResolverFunc(func(context.Context, corehosts.ContextKey) (ContextTarget, error) {
				rev := "1"
				if read.Load() {
					rev = "2" // relinked while the pages were read
				}
				return ContextTarget{Instance: in.ID, Project: "acme/shop", Revision: rev}, nil
			})
			h := NewConfirmedHosts(New(Deps{Config: cfg}), resolve, srv.Client())
			got, err := h.FindReviewComment(context.Background(), "rep_1", 7, marker, hex.EncodeToString(digest[:]), head)
			if !errors.Is(err, ErrContextScope) {
				t.Fatalf("want ErrContextScope, got %+v, %v", got, err)
			}
			if got.Outcome == coreproposals.CommentFound || got.Outcome == coreproposals.CommentAbsent {
				t.Fatalf("a conclusive outcome %v escaped a scope change", got.Outcome)
			}
		})
	}
}
