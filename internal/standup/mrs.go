package standup

import (
	"cmp"
	"context"
	"errors"
	"fmt"
	"slices"
	"strings"
	"time"
	"unicode"

	"github.com/brendatamaar/yggdrasil/internal/api"
	"github.com/brendatamaar/yggdrasil/internal/core"
	corehosts "github.com/brendatamaar/yggdrasil/internal/core/hosts"
	corestandup "github.com/brendatamaar/yggdrasil/internal/core/standup"
	"github.com/brendatamaar/yggdrasil/internal/safety"
)

// Errors of the merge request source. Both are labelled in the summary by
// reasonOf; neither carries host details.
var (
	// ErrMRNotLinked: the repo has no git host link (no remote, unknown host,
	// detached HEAD), so there is nothing to read.
	ErrMRNotLinked = errors.New("standup: the repo is not linked to a git host")
	// ErrMRNotCached: the host poller has not fetched this repo yet.
	ErrMRNotCached = errors.New("standup: no merge request data has been fetched for the repo yet")
)

// HostCache is what the merge request source may ask of the host service: two
// reads of data the poller already holds in memory. It has no refresh, fetch,
// token or client method on purpose, so a standup refresh cannot cause a
// network call or touch a credential. *hosts.Service satisfies it (the same
// two methods as api.HostsService).
type HostCache interface {
	RepoHost(ctx context.Context, repo core.RepoID) (api.RepoHostView, error)
	Inbox(ctx context.Context) (api.InboxView, error)
}

// CachedMRs is the MRSource over the cached host data (AU-02, UI-03).
type CachedMRs struct {
	cache HostCache
	red   safety.Redactor
}

var _ MRSource = (*CachedMRs)(nil)

// NewMRSource returns the merge request source. red redacts titles; nil means
// the default redactor (well-known token formats, credentials in URLs).
func NewMRSource(cache HostCache, red safety.Redactor) (*CachedMRs, error) {
	if cache == nil {
		return nil, errors.New("standup: the merge request source needs the host cache")
	}
	if red == nil {
		red = safety.NewRedactor()
	}
	return &CachedMRs{cache: cache, red: red}, nil
}

// maxMRTitleBytes bounds one title before the core cuts it for the text.
const maxMRTitleBytes = 1024

// MRs returns the cached merge requests of repo whose last update is in
// [from, to), newest first (ties by number), at most max; the bool is true when
// more matched.
//
// The cache holds the merge request of the repo's current branch and the user's
// inbox (review requested, assigned, authored) for the repo's host project. It
// is not a complete history: a merge request outside both is not seen, and
// At is the host's last-update time, so a merge request updated again later
// moves out of an older window. The summary says what the cache holds, nothing
// more.
func (s *CachedMRs) MRs(ctx context.Context, repo core.RepoID, from, to time.Time, max int) ([]corestandup.MR, bool, error) {
	if err := ctx.Err(); err != nil {
		return nil, false, fmt.Errorf("standup merge requests: %w", err)
	}
	if err := repo.Validate(); err != nil || max < 1 || !from.Before(to) {
		return nil, false, fmt.Errorf("%w: merge request read arguments", errInvalidData)
	}
	view, err := s.cache.RepoHost(ctx, repo)
	if err != nil {
		return nil, false, fmt.Errorf("standup merge requests: repo %s: %w", repo, err)
	}
	if !view.Linked {
		return nil, false, ErrMRNotLinked
	}
	own, fetched := cachedData(view.Entry)
	if !fetched {
		return nil, false, ErrMRNotCached
	}
	inbox, err := s.cache.Inbox(ctx)
	if err != nil {
		return nil, false, fmt.Errorf("standup merge requests: %w", err)
	}
	if err := ctx.Err(); err != nil {
		return nil, false, fmt.Errorf("standup merge requests: %w", err)
	}
	var found []corehosts.MergeRequest
	if rs, ok := own.(corehosts.RepoState); ok && rs.MR != nil {
		found = append(found, *rs.MR)
	}
	for _, e := range inbox.Items {
		if e.Instance == view.Instance.ID && corehosts.ProjectPath(e.Item.Repo) == view.Project {
			found = append(found, e.Item.MR)
		}
	}
	mrs := make([]corestandup.MR, 0, len(found))
	for _, m := range found {
		out, ok, err := s.mrOf(m, repo, from, to)
		if err != nil {
			return nil, false, err
		}
		if ok {
			mrs = append(mrs, out)
		}
	}
	out, cut := newestMRs(mrs, max)
	return out, cut, nil
}

// cachedData returns the last data of an entry in any polling phase, and
// whether the key was ever fetched.
func cachedData(e corehosts.Entry) (corehosts.Payload, bool) {
	var prev corehosts.Idle
	switch ph := e.Phase.(type) {
	case corehosts.Idle:
		prev = ph
	case corehosts.Fetching:
		prev = ph.Prev
	case corehosts.Waiting:
		prev = ph.Prev
	case corehosts.Blocked:
		prev = ph.Prev
	}
	return prev.Data, !prev.FetchedAt.IsZero()
}

// mrOf parses one cached merge request. ok is false when its last update is
// outside [from, to). A malformed one (no number, unknown state, no update
// time, empty title) is an error: the cache is not trusted to be well formed.
// The title is made valid UTF-8 first and redacted after, so a token split by
// an invalid byte is not reassembled.
func (s *CachedMRs) mrOf(m corehosts.MergeRequest, repo core.RepoID, from, to time.Time) (corestandup.MR, bool, error) {
	if m.Number < 1 || m.State.Validate() != nil || m.UpdatedAt.IsZero() {
		return corestandup.MR{}, false, fmt.Errorf("%w: merge request %d", errInvalidData, m.Number)
	}
	title := strings.ToValidUTF8(m.Title, "")
	if len(title) > maxMRTitleBytes {
		title = strings.ToValidUTF8(title[:maxMRTitleBytes], "")
	}
	title = s.red.Redact(title)
	if strings.TrimFunc(title, unicode.IsSpace) == "" {
		return corestandup.MR{}, false, fmt.Errorf("%w: merge request %d has no title", errInvalidData, m.Number)
	}
	if m.UpdatedAt.Before(from) || !m.UpdatedAt.Before(to) {
		return corestandup.MR{}, false, nil
	}
	return corestandup.MR{Repo: repo, Number: m.Number, Title: title, State: string(m.State), At: m.UpdatedAt.UTC()}, true, nil
}

// newestMRs drops repeats of one number (the same merge request seen in the
// branch entry and the inbox keeps its latest update), orders newest first
// (ties by number) and keeps at most max; the bool is true when something was
// cut.
func newestMRs(in []corestandup.MR, max int) ([]corestandup.MR, bool) {
	byNumber := make(map[int]corestandup.MR, len(in))
	for _, m := range in {
		if cur, ok := byNumber[m.Number]; !ok || m.At.After(cur.At) || (m.At.Equal(cur.At) && cmp.Or(cmp.Compare(m.Title, cur.Title), cmp.Compare(m.State, cur.State)) < 0) {
			byNumber[m.Number] = m
		}
	}
	out := make([]corestandup.MR, 0, len(byNumber))
	for _, m := range byNumber {
		out = append(out, m)
	}
	slices.SortFunc(out, func(a, b corestandup.MR) int {
		return cmp.Or(b.At.Compare(a.At), cmp.Compare(a.Number, b.Number))
	})
	if len(out) > max {
		return out[:max], true
	}
	return out, false
}
