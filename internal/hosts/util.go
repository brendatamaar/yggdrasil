package hosts

import (
	"fmt"
	"net/url"
	"strings"
	"time"

	corehosts "github.com/brendatamaar/yggdrasil/internal/core/hosts"
)

// ParseTime reads a host timestamp (RFC 3339, with or without fractions).
func ParseTime(s string) (time.Time, error) {
	t, err := time.Parse(time.RFC3339Nano, s)
	if err != nil {
		return time.Time{}, fmt.Errorf("bad timestamp %q", truncate(s, 40))
	}
	return t.UTC(), nil
}

// PickMR chooses the MR to show for a branch from a list that is newest first:
// the newest open (or draft) one, else the newest. nil when the list is empty.
func PickMR(mrs []corehosts.MergeRequest) *corehosts.MergeRequest {
	for i := range mrs {
		if mrs[i].State == corehosts.MROpen || mrs[i].State == corehosts.MRDraft {
			return &mrs[i]
		}
	}
	if len(mrs) > 0 {
		return &mrs[0]
	}
	return nil
}

func rolePriority(r corehosts.Role) int {
	switch r {
	case corehosts.RoleReviewRequested:
		return 0
	case corehosts.RoleAssigned:
		return 1
	}
	return 2
}

// DedupeInbox keeps each MR (by URL) once, with its most pressing role:
// review requested, then assigned, then authored. Order of first appearance
// is kept.
func DedupeInbox(items []corehosts.InboxItem) []corehosts.InboxItem {
	idx := map[string]int{}
	var out []corehosts.InboxItem
	for _, it := range items {
		if i, ok := idx[it.MR.URL]; ok {
			if rolePriority(it.Role) < rolePriority(out[i].Role) {
				out[i].Role = it.Role
			}
			continue
		}
		idx[it.MR.URL] = len(out)
		out = append(out, it)
	}
	return out
}

// PathSegments escapes each segment of a "group/sub/repo" path and keeps the
// slashes.
func PathSegments(p corehosts.ProjectPath) string {
	segs := strings.Split(string(p), "/")
	for i, s := range segs {
		segs[i] = url.PathEscape(s)
	}
	return strings.Join(segs, "/")
}

// NeedToken is the error of a call that requires a token when there is none.
func NeedToken() error {
	return fetchErr(corehosts.FailUnauthorized, "this needs a token and none is configured")
}

// SafeWebURL returns raw when it is an http or https URL with a host, else "".
// Host JSON is untrusted: a link such as "javascript:..." or "data:..." must
// never reach the browser as an href (SAFETY-B1).
func SafeWebURL(raw string) string {
	u, err := url.Parse(raw)
	if err != nil || u.Host == "" {
		return ""
	}
	switch strings.ToLower(u.Scheme) {
	case "http", "https":
		return raw
	}
	return ""
}
