package api

import (
	"bytes"
	"encoding/json"
	"errors"
	"net/http"
	"unicode/utf8"

	"github.com/brendatamaar/yggdrasil/internal/apperr"
	"github.com/brendatamaar/yggdrasil/internal/core/notify"
	coretasks "github.com/brendatamaar/yggdrasil/internal/core/tasks"
	"github.com/brendatamaar/yggdrasil/internal/db"
	"github.com/brendatamaar/yggdrasil/internal/proposals"
	"github.com/brendatamaar/yggdrasil/internal/tasks"
)

// Responses and error mapping of the Phase 6 endpoints. This file is Go-only
// wiring (no exported types or constants, so tygo has nothing to emit).

// TryAgain marks a service error as "the answer was overtaken, ask again": the
// cmd adapter wraps hosts.ErrContextSuperseded and standup.ErrSuperseded with
// it, because internal/api cannot import those packages (they import api).
// The API answers 503 with a plain "try again" message.
func TryAgain(cause error) error {
	return apperr.New(apperr.Unavailable, "That changed while it was being read. Try again.", cause)
}

// ShuttingDown marks a service error as "Yggdrasil is stopping": the cmd
// adapter wraps the ErrClosed of internal/actions, internal/newrepos and
// internal/notify with it (503).
func ShuttingDown(cause error) error {
	return apperr.New(apperr.Unavailable, "Yggdrasil is shutting down. Try again after it restarts.", cause)
}

// nothingWritten is implemented by the refusals of internal/newrepos (and any
// other owner) that say the request was turned down before anything changed.

type nothingWritten interface {
	error
	UserMessage() string
	NothingWritten() bool
}

// phase6Err translates an error of a Phase 6 service into an apperr class that
// fail turns into a status. Anything unclassified comes back unchanged and
// becomes a generic 500 whose cause is only logged. Messages are redacted.
func (s *Server) phase6Err(err error) error {
	cls := func(c apperr.Class, msg string) error { return apperr.New(c, s.redactor.Redact(msg), err) }
	var ue *coretasks.UserError
	var re *tasks.RejectedError
	var ref *proposals.Refusal
	var nw nothingWritten
	switch {
	case errors.As(err, &ue):
		return cls(apperr.Invalid, ue.Message)
	case errors.As(err, &re):
		return cls(apperr.Conflict, re.Message)
	case errors.Is(err, db.ErrDigestMismatch):
		return cls(apperr.Conflict, "That request ID was already used for a different request. Reload the page and start again.")
	case errors.Is(err, proposals.ErrRequestChanged):
		return cls(apperr.Conflict, "This request already made different proposals. Reload the page and start again.")
	case errors.As(err, &ref) || errors.Is(err, proposals.ErrAlreadyDecided) || errors.Is(err, proposals.ErrNotStarted):
		if pe := proposalErr(err); pe != err {
			return s.redactClassed(pe)
		}
	case errors.Is(err, db.ErrNotFound):
		return cls(apperr.NotFound, "That item doesn't exist (any more).")
	case errors.Is(err, db.ErrConflict):
		return cls(apperr.Conflict, "That item changed in the meantime.")
	case errors.As(err, &nw) && nw.NothingWritten():
		return cls(apperr.Conflict, nw.UserMessage())
	}
	if _, ok := apperr.ClassOf(err); ok {
		return s.redactClassed(err)
	}
	return err
}

// redactClassed keeps the class of an apperr error and redacts its message.
func (s *Server) redactClassed(err error) error {
	class, _ := apperr.ClassOf(err)
	msg, _ := apperr.MessageOf(err)
	return apperr.New(class, s.redactor.Redact(msg), err)
}

// fail6 answers a service error.
func (s *Server) fail6(w http.ResponseWriter, r *http.Request, err error) {
	s.fail(w, r, s.phase6Err(err))
}

// bad6 answers a boundary refusal (a *refusal6 from phase6_parse.go) with a 400.
func (s *Server) bad6(w http.ResponseWriter, err error) {
	badRequest(w, s.redactor.Redact(err.Error()), "Check the values you sent and try again.")
}

// decode6 reads a bounded JSON body; false means a 400 was already written.
func decode6(w http.ResponseWriter, r *http.Request, v any) bool { return decodeBody(w, r, v) }

// ok6 writes a 200 response with every string redacted (invariant 4). The
// value is encoded, decoded into a generic tree and re-encoded with each
// string passed through the redactor, so a redaction can never break the JSON.
func (s *Server) ok6(w http.ResponseWriter, r *http.Request, v any) {
	raw, err := json.Marshal(v)
	if err != nil {
		s.fail(w, r, err)
		return
	}
	dec := json.NewDecoder(bytes.NewReader(raw))
	dec.UseNumber()
	var tree any
	if err := dec.Decode(&tree); err != nil {
		s.fail(w, r, err)
		return
	}
	writeJSON(w, http.StatusOK, s.redactTree(tree))
}

func (s *Server) redactTree(v any) any {
	switch t := v.(type) {
	case string:
		return s.redactor.Redact(t)
	case []any:
		for i := range t {
			t[i] = s.redactTree(t[i])
		}
		return t
	case map[string]any:
		for k, e := range t {
			t[k] = s.redactTree(e)
		}
		return t
	}
	return v
}

// Response bounds (docs/design/phase-6-*.md). A service already bounds what it
// reads; the handler re-applies the limits so a misbehaving adapter can never
// send the browser an unbounded body.
const (
	maxP6Entries  = 500       // jobs, issues, ignore candidates (ContextMaxEntries)
	maxP6JobLog   = 256 << 10 // one job log
	maxP6Text     = 64 << 10  // an issue body, an MR diff, a preview context, a standup text
	maxP6Lines    = 100       // lines and problems of a preview
	maxP6Records  = 200       // standup records per source, new-repo operations listed
	maxP6Offering = 64        // catalogue entries
)

// cutText cuts s to at most max bytes without splitting a character.
func cutText(s string, max int) (string, bool) {
	if len(s) <= max {
		return s, false
	}
	s = s[:max]
	for len(s) > 0 && !utf8.ValidString(s) {
		s = s[:len(s)-1]
	}
	return s, true
}

func capSlice[T any](in []T, max int) ([]T, bool) {
	if len(in) <= max {
		return in, false
	}
	return in[:max], true
}

func nonNil6[T any](in []T) []T {
	if in == nil {
		return []T{}
	}
	return in
}

func boundCatalogue(r *ActionCatalogueResponse) {
	r.Offerings, _ = capSlice(nonNil6(r.Offerings), maxP6Offering)
}

func boundPreview(p *ActionPreviewDTO) {
	var cut bool
	if p.ContextText, cut = cutText(p.ContextText, maxP6Text); cut {
		p.Truncated = true
	}
	p.Unavailable, _ = capSlice(p.Unavailable, maxP6Lines)
}

func boundFailedJobs(r *FailedJobsResponse) {
	var cut bool
	if r.Jobs, cut = capSlice(nonNil6(r.Jobs), maxP6Entries); cut {
		r.Truncated = true
	}
	for i := range r.Jobs {
		if r.Jobs[i].Log, cut = cutText(r.Jobs[i].Log, maxP6JobLog); cut {
			r.Jobs[i].LogTruncated = true
		}
	}
}

func boundIssue(d *IssueDTO) {
	if d == nil {
		return
	}
	var cut bool
	if d.Body, cut = cutText(d.Body, maxP6Text); cut {
		d.BodyTruncated = true
	}
}

func boundIssues(r *IssuesResponse) {
	var cut bool
	if r.Issues, cut = capSlice(nonNil6(r.Issues), maxP6Entries); cut {
		r.Truncated = true
	}
	for i := range r.Issues {
		boundIssue(&r.Issues[i])
	}
}

func boundMRContext(r *MRContextResponse) {
	var cut bool
	if r.Diff, cut = cutText(r.Diff, maxP6Text); cut {
		r.DiffTruncated = true
	}
}

func boundNoise(r *NoiseResponse) {
	r.Candidates, _ = capSlice(nonNil6(r.Candidates), maxP6Entries)
}

func boundNotices(r *NoticesResponse, limit int) {
	r.Notices, _ = capSlice(nonNil6(r.Notices), min(limit, notify.MaxPage))
}

func boundStandup(r *StandupResponse) {
	mark := func(name string, cut bool) {
		if cut {
			r.Truncated = append(r.Truncated, name)
		}
	}
	var cut bool
	r.Commits, cut = capSlice(nonNil6(r.Commits), maxP6Records)
	mark("commits", cut)
	r.MRs, cut = capSlice(nonNil6(r.MRs), maxP6Records)
	mark("merge_requests", cut)
	r.Builds, cut = capSlice(nonNil6(r.Builds), maxP6Records)
	mark("builds", cut)
	r.Unavailable, _ = capSlice(r.Unavailable, maxP6Entries)
	r.Text, cut = cutText(r.Text, maxP6Text)
	mark("text", cut)
}

func boundLines(lines, problems []string) ([]string, []string) {
	lines, _ = capSlice(nonNil6(lines), maxP6Lines)
	problems, _ = capSlice(problems, maxP6Lines)
	return lines, problems
}

func boundOperation(o *NewRepoOperationDTO) {
	o.Stages, _ = capSlice(o.Stages, maxP6Lines)
}
