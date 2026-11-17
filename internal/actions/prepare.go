package actions

import (
	"context"
	"errors"
	"fmt"
	"strconv"
	"strings"
	"unicode/utf8"

	"github.com/brendatamaar/yggdrasil/internal/core"
	coreactions "github.com/brendatamaar/yggdrasil/internal/core/actions"
	corehosts "github.com/brendatamaar/yggdrasil/internal/core/hosts"
	corerepos "github.com/brendatamaar/yggdrasil/internal/core/repos"
	"github.com/brendatamaar/yggdrasil/internal/db"
)

// loaded is everything one attempt read before the preview. The core only ever
// sees the bounded PreparedContext; the rest stays in the shell for the effect.
type loaded struct {
	facts  coreactions.Facts
	target Target
	pctx   coreactions.PreparedContext
	// issue is set for ImportIssue when the host gave the issue.
	issueTitle string
	issueNum   int
	pull       PullTarget
}

const (
	beginData = "=====BEGIN UNTRUSTED DATA====="
	endData   = "=====END UNTRUSTED DATA====="
	// maxPart bounds one section of attached context so no single source can
	// push out the others; the core bounds the whole at MaxContextBytes.
	maxPart = 48 << 10
)

// clean makes host or build text safe to attach: valid UTF-8, no NUL or other
// control characters except line breaks and tabs, and no copy of the data
// delimiter, so text cannot close its own frame and pose as an instruction.
func clean(s string) string {
	if !utf8.ValidString(s) {
		s = strings.ToValidUTF8(s, "?")
	}
	s = strings.ReplaceAll(s, "\r\n", "\n")
	s = strings.Map(func(r rune) rune {
		if r == '\n' || r == '\t' {
			return r
		}
		if r < 0x20 || r == 0x7f || r == '\r' {
			return -1
		}
		return r
	}, s)
	s = strings.ReplaceAll(s, beginData, "[marker removed]")
	return strings.ReplaceAll(s, endData, "[marker removed]")
}

func clamp(s string, max int) (string, bool) {
	if len(s) <= max {
		return s, false
	}
	n := max
	for n > 0 && !utf8.RuneStart(s[n]) {
		n--
	}
	return s[:n], true
}

// blockedText says plainly why a host read gave nothing.
func blockedText(r corehosts.ContextBlockReason) string {
	switch r {
	case corehosts.ContextNoToken:
		return "no access token is configured for the host"
	case corehosts.ContextForbidden:
		return "the host token was refused or is too narrow"
	case corehosts.ContextNotFound:
		return "the host could not find it"
	case corehosts.ContextUnsupported:
		return "this host cannot give it"
	case corehosts.ContextRetryExhausted:
		return "the host kept failing"
	case corehosts.ContextTooLarge:
		return "it is too large to attach"
	}
	return "it is not available"
}

// hostRead reads one host key and turns every non-answer into a labelled gap:
// a missing token or a failing host never fails the action (E6, AC-01).
func (s *Service) hostRead(ctx context.Context, key corehosts.ContextKey, what string) (corehosts.ContextPayload, []string) {
	if err := key.Validate(); err != nil {
		return nil, []string{what + " could not be read: the identity is not valid."}
	}
	out, err := s.d.Context.Host(ctx, key)
	switch {
	case err != nil:
		return nil, []string{what + " could not be read right now."}
	case out.Blocked != nil:
		return nil, []string{what + " is not available: " + blockedText(out.Blocked.Reason) + "."}
	case out.Result == nil:
		return nil, []string{what + " is not ready yet; try again in a moment."}
	}
	var gaps []string
	for _, u := range out.Result.Unavailable {
		gaps = append(gaps, fmt.Sprintf("%s is not available: %s.", clean(u.Source), blockedText(u.Reason)))
	}
	return out.Result.Value, gaps
}

// prepare loads the facts and the bounded, redacted, source-labelled context of
// a request. A reason other than "" means nothing can be offered.
func (s *Service) prepare(ctx context.Context, req coreactions.Request) (*loaded, coreactions.RefuseReason) {
	facts, err := s.d.Scope.Facts(ctx, req.RepoID)
	if err != nil {
		return nil, scopeReason(err)
	}
	tgt, err := s.d.Scope.Target(ctx, req.RepoID)
	if err != nil {
		return nil, scopeReason(err)
	}
	l := &loaded{facts: facts, target: tgt}
	if ok, _ := offered(facts, req); !ok {
		// The core refuses with the exact reason; nothing is read for it.
		return l, ""
	}
	var (
		source, text string
		gaps         []string
		cut          bool
	)
	switch req.Kind {
	case coreactions.DiagnosePipeline, coreactions.FixPipeline:
		source, text, gaps, cut = s.ciContext(ctx, req, facts)
	case coreactions.DiagnoseBuild:
		source, text, gaps, cut = s.buildContext(ctx, req, facts)
	case coreactions.ReviewMR:
		source, text, gaps, cut = s.mrContext(ctx, req, facts)
	case coreactions.ProposeCommits:
		var why coreactions.RefuseReason
		source, text, gaps, cut, why = s.commitContext(ctx, req, facts)
		if why != "" {
			return nil, why
		}
	case coreactions.ProposeIgnore:
		source, text, gaps, cut = s.ignoreContext(ctx, req, facts)
	case coreactions.DiagnoseRepo:
		source, text, gaps = repoContext(req, facts)
	case coreactions.ImportIssue:
		var why coreactions.RefuseReason
		source, text, gaps, why = s.issueContext(ctx, req, l)
		if why != "" {
			return nil, why
		}
	case coreactions.PullAndCheck:
		pt, err := s.d.Scope.PullTarget(ctx, req.RepoID)
		if err != nil {
			return nil, scopeReason(err)
		}
		l.pull = pt
		source = "repo " + string(req.RepoID)
	}
	text, c2 := clamp(s.d.Redactor.Redact(clean(text)), coreactions.MaxContextBytes)
	for i := range gaps {
		gaps[i] = s.d.Redactor.Redact(gaps[i])
	}
	l.pctx = coreactions.PreparedContext{
		Source: s.d.Redactor.Redact(source), Text: text, Truncated: cut || c2, Unavailable: gaps,
		Availability: availabilityOf(text, gaps, req.Kind), WaitingForFolder: !facts.Readable && req.Kind != coreactions.ImportIssue,
	}
	return l, ""
}

func availabilityOf(text string, gaps []string, k coreactions.Kind) coreactions.ContextAvailability {
	switch {
	case k == coreactions.PullAndCheck || k == coreactions.ImportIssue:
		return coreactions.ContextNone
	case len(gaps) > 0:
		return coreactions.ContextMetadataOnly
	case text == "":
		return coreactions.ContextMetadataOnly
	}
	return coreactions.ContextAvailable
}

func scopeReason(err error) coreactions.RefuseReason {
	if errors.Is(err, db.ErrNotFound) {
		return coreactions.RefuseUntracked
	}
	return coreactions.RefusePrepareFailed
}

// offered reports whether the facts still offer the requested kind and source.
// It mirrors the core's admission so the shell can recheck at execution.
func offered(f coreactions.Facts, r coreactions.Request) (bool, coreactions.RefuseReason) {
	if f.Repo != r.RepoID {
		return false, coreactions.RefuseScopeChanged
	}
	if r.Kind == coreactions.ImportIssue {
		if !f.Caps.HostIssues {
			return false, coreactions.RefuseCapability
		}
		return true, ""
	}
	same := false
	for _, o := range coreactions.Catalogue(f) {
		if o.Kind != r.Kind {
			continue
		}
		if o.SourceID == r.SourceID {
			return true, ""
		}
		same = true
	}
	if same {
		return false, coreactions.RefuseScopeChanged
	}
	return false, coreactions.RefuseNoOffering
}

func factLines(f coreactions.Facts) string {
	var b strings.Builder
	fmt.Fprintf(&b, "Repo: %s\nHEAD: %s\n", f.Repo, f.Head)
	if len(f.Reasons) > 0 {
		var rs []string
		for _, r := range f.Reasons {
			rs = append(rs, string(r))
		}
		fmt.Fprintf(&b, "Needs attention because: %s\n", strings.Join(rs, ", "))
	}
	if !f.Readable {
		b.WriteString("The working folder cannot be read right now.\n")
	}
	return b.String()
}

func (s *Service) ciContext(ctx context.Context, req coreactions.Request, f coreactions.Facts) (string, string, []string, bool) {
	source := fmt.Sprintf("CI run %s on repo %s", req.SourceID, req.RepoID)
	var b strings.Builder
	b.WriteString(factLines(f))
	fmt.Fprintf(&b, "Failed pipeline run: %s\n", req.SourceID)
	if f.CIHead != "" {
		fmt.Fprintf(&b, "Pipeline commit: %s\n", f.CIHead)
	}
	var gaps []string
	cut := false
	if !f.Caps.HostLogs {
		gaps = append(gaps, "The job log is not available: no linked host or token. Only the pipeline's name, status and commit are attached.")
		return source, b.String(), gaps, false
	}
	val, g := s.hostRead(ctx, corehosts.ContextKey{Repo: req.RepoID, Kind: corehosts.ContextFailedJobs, ObjectID: req.SourceID, Head: f.CIHead}, "The job log")
	gaps = append(gaps, g...)
	if logs, ok := val.(corehosts.JobLogs); ok {
		cut = logs.Truncated
		for _, j := range logs.Jobs {
			fmt.Fprintf(&b, "\n### Failed job %q (stage %q)\n", clean(j.Name), clean(j.Stage))
			log, c := clamp(j.Log, maxPart/max(1, len(logs.Jobs)))
			b.WriteString(log)
			if c || j.LogTruncated {
				cut = true
				b.WriteString("\n[log cut]\n")
			}
			if j.LogUnavailable != "" {
				gaps = append(gaps, fmt.Sprintf("The log of job %q is not available.", clean(j.Name)))
			}
		}
	} else if len(gaps) == 0 {
		gaps = append(gaps, "The job log could not be read.")
	}
	return source, b.String(), gaps, cut
}

func (s *Service) buildContext(ctx context.Context, req coreactions.Request, f coreactions.Facts) (string, string, []string, bool) {
	source := fmt.Sprintf("build %s on repo %s", req.SourceID, req.RepoID)
	var b strings.Builder
	b.WriteString(factLines(f))
	fmt.Fprintf(&b, "Failed build: %s\n", req.SourceID)
	if !f.Caps.BuildLog {
		return source, b.String(), []string{"The build log can't be read. Only the build's identity is attached."}, false
	}
	log, cut, err := s.d.Context.BuildLog(ctx, req.RepoID, core.BuildID(req.SourceID))
	if err != nil {
		return source, b.String(), []string{"The build log could not be read right now."}, false
	}
	log, c2 := clamp(log, maxPart)
	b.WriteString("\n### Build log\n")
	b.WriteString(log)
	return source, b.String(), nil, cut || c2
}

func (s *Service) mrContext(ctx context.Context, req coreactions.Request, f coreactions.Facts) (string, string, []string, bool) {
	source := fmt.Sprintf("merge request %s on repo %s", req.SourceID, req.RepoID)
	var b strings.Builder
	b.WriteString(factLines(f))
	fmt.Fprintf(&b, "Merge request: %s\nMerge request head (pinned): %s\n", req.SourceID, f.MRHead)
	if !f.Caps.HostMR {
		return source, b.String(), []string{"The merge request's diff can't be read (no linked host or token). Only its head is attached."}, false
	}
	val, gaps := s.hostRead(ctx, corehosts.ContextKey{Repo: req.RepoID, Kind: corehosts.ContextMRDiff, ObjectID: req.SourceID, Head: f.MRHead}, "The merge request diff")
	cut := false
	if mr, ok := val.(corehosts.MRContext); ok {
		if mr.Head != "" && mr.Head != f.MRHead {
			gaps = append(gaps, "The merge request changed while it was read, so its diff is not attached.")
		} else {
			fmt.Fprintf(&b, "Title: %s\nSource branch: %s\nTarget branch: %s\n\n### Diff\n", clean(mr.MR.Title), clean(mr.MR.Source), clean(mr.MR.Target))
			d, c := clamp(mr.Diff, maxPart)
			b.WriteString(d)
			cut = c || mr.DiffTruncated
		}
	} else if len(gaps) == 0 {
		gaps = append(gaps, "The merge request diff could not be read.")
	}
	return source, b.String(), gaps, cut
}

func (s *Service) commitContext(ctx context.Context, req coreactions.Request, f coreactions.Facts) (string, string, []string, bool, coreactions.RefuseReason) {
	source := "current changes of repo " + string(req.RepoID)
	var b strings.Builder
	b.WriteString(factLines(f))
	if !f.Readable {
		return source, b.String(), []string{"The folder can't be read, so the diff is not attached."}, false, ""
	}
	d, err := s.d.Context.Diff(ctx, req.RepoID)
	if err != nil {
		return source, b.String(), []string{"The current diff could not be read right now."}, false, ""
	}
	if d.Head != f.Head {
		// The repo moved between the two reads: a preview of one commit and a
		// diff of another would pin the wrong thing.
		return "", "", nil, false, coreactions.RefuseStaleHead
	}
	fmt.Fprintf(&b, "Diff hash (pinned): %s\nChanged paths (use exactly these, never stage everything):\n", d.DiffHash)
	for _, p := range d.Paths {
		fmt.Fprintf(&b, "- %s\n", clean(p))
	}
	body, c := clamp(d.Text, maxPart)
	b.WriteString("\n### Diff\n")
	b.WriteString(body)
	return source, b.String(), nil, d.Truncated || c, ""
}

func (s *Service) ignoreContext(ctx context.Context, req coreactions.Request, f coreactions.Facts) (string, string, []string, bool) {
	source := "untracked files of repo " + string(req.RepoID)
	var b strings.Builder
	b.WriteString(factLines(f))
	nf, err := s.d.Scope.NoiseFacts(ctx, req.RepoID)
	if err != nil {
		return source, b.String(), []string{"The untracked files could not be read right now."}, false
	}
	fmt.Fprintf(&b, "Untracked files: %d (sample of %d)\n", nf.UntrackedCount, len(nf.Sample))
	n, _ := corerepos.NoiseStep(corerepos.Noise{Repo: nf.Repo, State: corerepos.NoSuggestion{}}, corerepos.NoiseObserved{Facts: nf})
	if sg, ok := n.State.(corerepos.Suggested); ok {
		b.WriteString("Candidate ignore entries:\n")
		for _, c := range sg.Candidates {
			fmt.Fprintf(&b, "- %s (%d sampled files)\n", clean(c.Pattern), c.SampleCount)
		}
	}
	b.WriteString("\n### Sample of untracked paths\n")
	for i, p := range nf.Sample {
		if i == 200 {
			break
		}
		fmt.Fprintf(&b, "%s\n", clean(p))
	}
	return source, b.String(), nil, nf.IsPartial()
}

func repoContext(req coreactions.Request, f coreactions.Facts) (string, string, []string) {
	source := "tracked facts of repo " + string(req.RepoID)
	var gaps []string
	if !f.Readable {
		gaps = append(gaps, "The folder can't be read. The agent gets only tracked metadata, and the draft waits for a valid folder.")
	} else {
		gaps = append(gaps, "Only the repo's tracked facts are attached; the agent reads the files itself.")
	}
	return source, factLines(f), gaps
}

// issueContext reads the issue and keeps its title for the draft. An issue the
// host cannot give leaves nothing to import, so it is a refusal at preview.
func (s *Service) issueContext(ctx context.Context, req coreactions.Request, l *loaded) (string, string, []string, coreactions.RefuseReason) {
	source := fmt.Sprintf("issue %s on repo %s", req.SourceID, req.RepoID)
	num, err := strconv.Atoi(req.SourceID)
	if err != nil || num <= 0 {
		return "", "", nil, coreactions.RefuseInvalid
	}
	val, gaps := s.hostRead(ctx, corehosts.ContextKey{Repo: req.RepoID, Kind: corehosts.ContextIssue, ObjectID: req.SourceID}, "The issue")
	d, ok := val.(corehosts.IssueDetail)
	if !ok || strings.TrimSpace(d.Summary.Title) == "" {
		return "", "", gaps, coreactions.RefuseCapability
	}
	l.issueTitle, l.issueNum = d.Summary.Title, num
	var b strings.Builder
	fmt.Fprintf(&b, "Issue #%d: %s\n", num, clean(d.Summary.Title))
	if len(d.Summary.Labels) > 0 {
		fmt.Fprintf(&b, "Labels: %s\n", clean(strings.Join(d.Summary.Labels, ", ")))
	}
	b.WriteString("\n")
	body, _ := clamp(d.Body, maxPart)
	b.WriteString(body)
	return source, b.String(), gaps, ""
}
