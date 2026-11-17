package standup

import (
	"cmp"
	"slices"
	"strconv"
	"strings"
	"time"
	"unicode"
	"unicode/utf8"

	"github.com/brendatamaar/yggdrasil/internal/core"
)

// Step is (standup2, commands) = Step(standup1, input) following
// docs/design/phase-6-standup.md. It is pure: time arrives in the inputs, and
// the only command that starts anything beyond a read is SubmitAskDraft, which
// only an explicit PolishRequested produces. A refresh (Requested,
// SourcesRead, ReadFailed, Cancelled) never emits it.
//
// A result input (SourcesRead, ReadFailed, Cancelled) is applied only to the
// attempt that is loading; a stale one returns the standup unchanged. Pairs the
// table does not list also return it unchanged, with no commands.
func Step(s Standup, in Input) (Standup, []Command) {
	switch in := in.(type) {
	case Requested:
		return stepRequested(s, in)
	case SourcesRead:
		return stepSourcesRead(s, in)
	case ReadFailed:
		if l, ok := s.State.(Loading); ok && l.Attempt == in.Attempt {
			reason := cleanText(in.Reason, maxReasonBytes)
			if reason == "" {
				reason = "The standup could not be read."
			}
			s.State = Failed{Reason: reason}
			return s, []Command{Explain{Reason: reason}}
		}
	case Cancelled:
		if l, ok := s.State.(Loading); ok && l.Attempt == in.Attempt {
			s.State = Empty{}
			return s, []Command{CancelRead{Attempt: l.Attempt}}
		}
	case PolishRequested:
		return stepPolish(s, in)
	}
	return s, nil
}

// stepRequested starts a read. Now is part of the input so the shell stamps the
// request; the decision does not depend on it.
func stepRequested(s Standup, in Requested) (Standup, []Command) {
	rng, err := ParseRange(in.Range.Project, in.Range.From, in.Range.To)
	if err != nil {
		return s, nil
	}
	var cmds []Command
	attempt := s.Seq
	if l, ok := s.State.(Loading); ok {
		cmds = append(cmds, CancelRead{Attempt: l.Attempt})
		attempt = max(attempt, l.Attempt)
	}
	attempt++
	s.Seq = attempt
	s.State = Loading{Attempt: attempt, Range: rng}
	return s, append(cmds, ReadSources{Attempt: attempt, Range: rng})
}

func stepSourcesRead(s Standup, in SourcesRead) (Standup, []Command) {
	l, ok := s.State.(Loading)
	if !ok || l.Attempt != in.Attempt {
		return s, nil
	}
	sum, unavail, failure := BuildSummary(l.Range, in)
	if failure != "" {
		s.State = Failed{Reason: failure}
		return s, []Command{Explain{Reason: failure}}
	}
	if len(unavail) == 0 {
		s.State = Ready{Summary: sum}
	} else {
		s.State = Partial{Summary: sum, Unavailable: unavail}
	}
	return s, []Command{ReturnSummary{Attempt: l.Attempt}}
}

func stepPolish(s Standup, in PolishRequested) (Standup, []Command) {
	if in.RequestID.Validate() != nil {
		return s, nil
	}
	var text string
	switch st := s.State.(type) {
	case Ready:
		text = st.Summary.Text
	case Partial:
		text = st.Summary.Text
	default:
		return s, nil
	}
	if text == "" {
		return s, nil
	}
	return s, []Command{SubmitAskDraft{RequestID: in.RequestID, Text: text}}
}

const (
	maxReasonBytes  = 200
	maxSubjectBytes = 200
	maxNameBytes    = 100
)

var sourceOrder = []SourceKind{SourceCommits, SourceMRs, SourceBuilds}

func kindIndex(k SourceKind) int { return slices.Index(sourceOrder, k) }

// BuildSummary turns what the shell read into a deterministic Summary: records
// outside [From, To) or malformed are dropped, duplicates removed, repos and
// records bounded, everything sorted by repo name, time and identity, and the
// text rendered. It returns the labelled unavailable sources (sorted), or a
// non-empty failure reason when no source was readable or the result is
// inconsistent. The same facts in any order give the same result. A source
// labelled unavailable contributes no records: nothing is invented for it.
func BuildSummary(rng Range, in SourcesRead) (Summary, []Unavailable, string) {
	unavail, whole, ok := normalizeUnavailable(in.Unavailable)
	if !ok {
		return Summary{}, nil, "The standup sources returned an inconsistent result."
	}
	if len(whole) == len(sourceOrder) {
		return Summary{}, nil, "None of the standup sources could be read."
	}
	if in.Readable != len(sourceOrder)-len(whole) {
		return Summary{}, nil, "The standup sources returned an inconsistent result."
	}
	skip := func(k SourceKind, repo core.RepoID) bool {
		if whole[k] {
			return true
		}
		for _, u := range unavail {
			if u.Source == k && u.Repo == repo {
				return true
			}
		}
		return false
	}
	inRange := func(t time.Time) bool { return !t.IsZero() && !t.Before(rng.From) && t.Before(rng.To) }

	var commits []Commit
	for _, c := range in.Commits {
		c.At = c.At.UTC()
		if skip(SourceCommits, c.Repo) || !inRange(c.At) || c.Repo.Validate() != nil || c.OID == "" {
			continue
		}
		c.RepoName, c.OID = cleanText(c.RepoName, maxNameBytes), cleanText(c.OID, 64)
		c.Subject, c.Author = cleanText(c.Subject, maxSubjectBytes), cleanText(c.Author, maxNameBytes)
		commits = append(commits, c)
	}
	var mrs []MR
	for _, m := range in.MRs {
		m.At = m.At.UTC()
		if skip(SourceMRs, m.Repo) || !inRange(m.At) || m.Repo.Validate() != nil || m.Number < 1 {
			continue
		}
		m.RepoName, m.Title, m.State = cleanText(m.RepoName, maxNameBytes), cleanText(m.Title, maxSubjectBytes), cleanText(m.State, 32)
		mrs = append(mrs, m)
	}
	var builds []Build
	for _, b := range in.Builds {
		b.At = b.At.UTC()
		if skip(SourceBuilds, b.Repo) || !inRange(b.At) || b.Repo.Validate() != nil || b.ID.Validate() != nil || b.Status.Validate() != nil {
			continue
		}
		b.RepoName = cleanText(b.RepoName, maxNameBytes)
		builds = append(builds, b)
	}

	cut := map[SourceKind]bool{}
	for _, k := range in.Truncated {
		if k.Validate() == nil && !whole[k] {
			cut[k] = true
		}
	}

	commits = dedupe(commits, func(c Commit) string { return string(c.Repo) + "\x00" + c.OID }, cmpCommitFull)
	mrs = dedupe(mrs, func(m MR) string { return string(m.Repo) + "\x00" + strconv.Itoa(m.Number) }, cmpMRFull)
	builds = dedupe(builds, func(b Build) string { return string(b.Repo) + "\x00" + string(b.ID) }, cmpBuildFull)

	// Repo cap: keep the first MaxRepos repos by name then ID across all records.
	type repoKey struct {
		name string
		id   core.RepoID
	}
	seen := map[core.RepoID]repoKey{}
	note := func(id core.RepoID, name string) {
		if k, ok := seen[id]; !ok || name < k.name {
			seen[id] = repoKey{name, id}
		}
	}
	for _, c := range commits {
		note(c.Repo, c.RepoName)
	}
	for _, m := range mrs {
		note(m.Repo, m.RepoName)
	}
	for _, b := range builds {
		note(b.Repo, b.RepoName)
	}
	if len(seen) > MaxRepos {
		keys := make([]repoKey, 0, len(seen))
		for _, k := range seen {
			keys = append(keys, k)
		}
		slices.SortFunc(keys, func(a, b repoKey) int { return cmp.Or(cmp.Compare(a.name, b.name), cmp.Compare(a.id, b.id)) })
		keep := map[core.RepoID]bool{}
		for _, k := range keys[:MaxRepos] {
			keep[k.id] = true
		}
		commits, cut[SourceCommits] = filterRepos(commits, keep, func(c Commit) core.RepoID { return c.Repo }, cut[SourceCommits])
		mrs, cut[SourceMRs] = filterRepos(mrs, keep, func(m MR) core.RepoID { return m.Repo }, cut[SourceMRs])
		builds, cut[SourceBuilds] = filterRepos(builds, keep, func(b Build) core.RepoID { return b.Repo }, cut[SourceBuilds])
	}

	// Record cap: the newest MaxRecordsPerSource of each source.
	commits, cut[SourceCommits] = newest(commits, cut[SourceCommits], func(a, b Commit) int { return cmp.Or(b.At.Compare(a.At), cmpCommitFull(a, b)) })
	mrs, cut[SourceMRs] = newest(mrs, cut[SourceMRs], func(a, b MR) int { return cmp.Or(b.At.Compare(a.At), cmpMRFull(a, b)) })
	builds, cut[SourceBuilds] = newest(builds, cut[SourceBuilds], func(a, b Build) int { return cmp.Or(b.At.Compare(a.At), cmpBuildFull(a, b)) })

	slices.SortFunc(commits, cmpCommit)
	slices.SortFunc(mrs, cmpMR)
	slices.SortFunc(builds, cmpBuild)

	sum := Summary{Range: rng, Commits: commits, MRs: mrs, Builds: builds}
	for _, k := range sourceOrder {
		if cut[k] {
			sum.Truncated = append(sum.Truncated, k)
		}
	}
	text, textCut := render(sum, unavail)
	for _, k := range textCut {
		if !slices.Contains(sum.Truncated, k) {
			sum.Truncated = append(sum.Truncated, k)
		}
	}
	slices.SortFunc(sum.Truncated, func(a, b SourceKind) int { return cmp.Compare(kindIndex(a), kindIndex(b)) })
	sum.Text = text
	return sum, unavail, ""
}

// normalizeUnavailable validates, cleans, dedupes and sorts the labels. A whole
// source label supersedes that source's per-repo labels. ok is false for a label
// with an unknown source or a malformed repo.
func normalizeUnavailable(in []Unavailable) (out []Unavailable, whole map[SourceKind]bool, ok bool) {
	whole = map[SourceKind]bool{}
	for _, u := range in {
		if u.Source.Validate() != nil || (u.Repo != "" && u.Repo.Validate() != nil) {
			return nil, nil, false
		}
		if u.Repo == "" {
			whole[u.Source] = true
		}
	}
	for _, u := range in {
		if u.Repo != "" && whole[u.Source] {
			continue
		}
		u.Reason = cleanText(u.Reason, maxReasonBytes)
		if u.Reason == "" {
			u.Reason = "could not be read"
		}
		u.RepoName = cleanText(u.RepoName, maxNameBytes)
		if u.Repo == "" {
			u.RepoName = ""
		}
		out = append(out, u)
	}
	slices.SortFunc(out, func(a, b Unavailable) int {
		return cmp.Or(cmp.Compare(kindIndex(a.Source), kindIndex(b.Source)), cmp.Compare(a.Repo, b.Repo), cmp.Compare(a.Reason, b.Reason), cmp.Compare(a.RepoName, b.RepoName))
	})
	out = slices.CompactFunc(out, func(a, b Unavailable) bool { return a.Source == b.Source && a.Repo == b.Repo })
	return out, whole, true
}

func dedupe[T any](in []T, identity func(T) string, full func(a, b T) int) []T {
	slices.SortFunc(in, func(a, b T) int { return cmp.Or(cmp.Compare(identity(a), identity(b)), full(a, b)) })
	return slices.CompactFunc(in, func(a, b T) bool { return identity(a) == identity(b) })
}

func filterRepos[T any](in []T, keep map[core.RepoID]bool, repo func(T) core.RepoID, cut bool) ([]T, bool) {
	out := in[:0:0]
	for _, v := range in {
		if keep[repo(v)] {
			out = append(out, v)
		} else {
			cut = true
		}
	}
	return out, cut
}

func newest[T any](in []T, cut bool, newestFirst func(a, b T) int) ([]T, bool) {
	if len(in) <= MaxRecordsPerSource {
		return in, cut
	}
	slices.SortFunc(in, newestFirst)
	return in[:MaxRecordsPerSource], true
}

func cmpCommit(a, b Commit) int {
	return cmp.Or(cmp.Compare(a.RepoName, b.RepoName), cmp.Compare(a.Repo, b.Repo), a.At.Compare(b.At), cmp.Compare(a.OID, b.OID), cmpCommitFull(a, b))
}

func cmpCommitFull(a, b Commit) int {
	return cmp.Or(cmp.Compare(a.Repo, b.Repo), cmp.Compare(a.OID, b.OID), a.At.Compare(b.At), cmp.Compare(a.RepoName, b.RepoName), cmp.Compare(a.Subject, b.Subject), cmp.Compare(a.Author, b.Author))
}

func cmpMR(a, b MR) int {
	return cmp.Or(cmp.Compare(a.RepoName, b.RepoName), cmp.Compare(a.Repo, b.Repo), a.At.Compare(b.At), cmp.Compare(a.Number, b.Number), cmpMRFull(a, b))
}

func cmpMRFull(a, b MR) int {
	return cmp.Or(cmp.Compare(a.Repo, b.Repo), cmp.Compare(a.Number, b.Number), a.At.Compare(b.At), cmp.Compare(a.RepoName, b.RepoName), cmp.Compare(a.Title, b.Title), cmp.Compare(a.State, b.State))
}

func cmpBuild(a, b Build) int {
	return cmp.Or(cmp.Compare(a.RepoName, b.RepoName), cmp.Compare(a.Repo, b.Repo), a.At.Compare(b.At), cmp.Compare(a.ID, b.ID), cmpBuildFull(a, b))
}

func cmpBuildFull(a, b Build) int {
	return cmp.Or(cmp.Compare(a.Repo, b.Repo), cmp.Compare(a.ID, b.ID), a.At.Compare(b.At), cmp.Compare(a.RepoName, b.RepoName), cmp.Compare(a.Status, b.Status))
}

// cleanText makes s safe for the single-line text: invalid UTF-8 and control or
// line-separator characters become spaces, runs of spaces collapse, and the
// result is cut at max bytes on a rune boundary.
func cleanText(s string, max int) string {
	s = strings.ToValidUTF8(s, "�")
	s = strings.Map(func(r rune) rune {
		if unicode.IsControl(r) || r == ' ' || r == ' ' {
			return ' '
		}
		return r
	}, s)
	s = strings.Join(strings.Fields(s), " ")
	if len(s) > max {
		cutAt := max
		for cutAt > 0 && !utf8.RuneStart(s[cutAt]) {
			cutAt--
		}
		s = strings.TrimSpace(s[:cutAt]) + "…"
	}
	return s
}
