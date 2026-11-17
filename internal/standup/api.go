package standup

import (
	"context"
	"errors"
	"fmt"
	"time"
	"unicode"

	"github.com/brendatamaar/yggdrasil/internal/api"
	"github.com/brendatamaar/yggdrasil/internal/apperr"
	"github.com/brendatamaar/yggdrasil/internal/core"
	corestandup "github.com/brendatamaar/yggdrasil/internal/core/standup"
)

// Get serves GET /api/projects/{id}/standup. It is a refresh: no agent, no
// model call, no network call.
func (s *Service) Get(ctx context.Context, project core.ProjectID, from, to time.Time) (api.StandupResponse, error) {
	res, err := s.Refresh(ctx, project, from, to)
	if err != nil {
		return api.StandupResponse{}, err
	}
	return toResponse(project, res), nil
}

func toResponse(project core.ProjectID, res Result) api.StandupResponse {
	out := api.StandupResponse{
		ProjectID: project, From: res.Range.From, To: res.Range.To,
		Commits: []api.StandupCommitDTO{}, MRs: []api.StandupMRDTO{}, Builds: []api.StandupBuildDTO{},
	}
	var sum corestandup.Summary
	switch st := res.State.(type) {
	case corestandup.Ready:
		out.Phase, sum = api.StandupReady, st.Summary
	case corestandup.Partial:
		out.Phase, sum = api.StandupPartial, st.Summary
		for _, u := range st.Unavailable {
			out.Unavailable = append(out.Unavailable, api.StandupUnavailableDTO{Source: string(u.Source), RepoID: u.Repo, Reason: u.Reason})
		}
	case corestandup.Failed:
		out.Phase, out.Message = api.StandupFailed, st.Reason
		return out
	default:
		out.Phase, out.Message = api.StandupFailed, "The standup is not available."
		return out
	}
	for _, c := range sum.Commits {
		out.Commits = append(out.Commits, api.StandupCommitDTO{RepoID: c.Repo, RepoName: c.RepoName, ID: c.OID, Subject: c.Subject, Author: c.Author, At: c.At})
	}
	for _, m := range sum.MRs {
		out.MRs = append(out.MRs, api.StandupMRDTO{RepoID: m.Repo, RepoName: m.RepoName, Number: m.Number, Title: m.Title, State: m.State, At: m.At})
	}
	for _, b := range sum.Builds {
		out.Builds = append(out.Builds, api.StandupBuildDTO{RepoID: b.Repo, RepoName: b.RepoName, BuildID: b.ID, Status: b.Status, At: b.At})
	}
	for _, k := range sum.Truncated {
		out.Truncated = append(out.Truncated, string(k))
	}
	out.Text = sum.Text
	return out
}

// Polish serves POST /api/projects/{id}/standup/polish. It is the only caller of
// the Polisher, and only on an explicit request: it rebuilds the summary text on
// the server (the client never supplies text), then submits one visible Ask
// task through the ordinary action path. The request ID makes it idempotent:
// concurrent submissions of one ID start one task, and the Polisher answers a
// repeat with the first result. The saved summary is not rewritten.
func (s *Service) Polish(ctx context.Context, project core.ProjectID, req api.StandupPolishRequest) (api.ActionResultDTO, error) {
	if err := req.RequestID.Validate(); err != nil {
		return api.ActionResultDTO{}, apperr.New(apperr.Invalid, "The request has no valid id. Try again.", err)
	}
	rng, err := corestandup.ParseRange(project, req.From, req.To)
	if err != nil {
		return api.ActionResultDTO{}, invalidRange(err)
	}
	if err := validAgent(req.Agent, req.Model); err != nil {
		return api.ActionResultDTO{}, err
	}

	key := fmt.Sprintf("%s|%d|%d|%s|%s", project, rng.From.UnixNano(), rng.To.UnixNano(), req.Agent, req.Model)
	s.mu.Lock()
	if c, ok := s.inflight[req.RequestID]; ok {
		s.mu.Unlock()
		if c.key != key {
			return api.ActionResultDTO{}, apperr.New(apperr.Conflict, "This request was already used for a different standup. Try again.", nil)
		}
		select {
		case <-c.done:
			return c.res, c.err
		case <-ctx.Done():
			return api.ActionResultDTO{}, fmt.Errorf("wait for the standup polish: %w", ctx.Err())
		}
	}
	c := &polishCall{key: key, done: make(chan struct{})}
	s.inflight[req.RequestID] = c
	s.mu.Unlock()

	c.res, c.err = s.polish(ctx, project, rng, req)
	s.mu.Lock()
	delete(s.inflight, req.RequestID)
	s.mu.Unlock()
	close(c.done)
	return c.res, c.err
}

func (s *Service) polish(ctx context.Context, project core.ProjectID, rng corestandup.Range, req api.StandupPolishRequest) (api.ActionResultDTO, error) {
	scope, scopeCut, err := s.scope(ctx, project)
	if err != nil {
		return api.ActionResultDTO{}, err
	}
	// A private state: polishing never supersedes the shared refresh.
	st, _ := corestandup.Step(corestandup.Standup{State: corestandup.Empty{}}, corestandup.Requested{Range: rng, Now: s.d.Clock()})
	l, ok := st.State.(corestandup.Loading)
	if !ok {
		return api.ActionResultDTO{}, invalidRange(corestandup.ErrInvalidRange)
	}
	readCtx, cancel := context.WithTimeout(ctx, corestandup.ReadTimeout)
	defer cancel()
	in := s.collect(readCtx, ctx, rng, scope, scopeCut, l.Attempt, project)
	if _, cancelled := in.(corestandup.Cancelled); cancelled {
		return api.ActionResultDTO{}, fmt.Errorf("standup polish cancelled: %w", ctx.Err())
	}
	st, _ = corestandup.Step(st, in)
	switch st.State.(type) {
	case corestandup.Ready, corestandup.Partial:
	default:
		return api.ActionResultDTO{}, apperr.New(apperr.Unavailable, "The standup could not be read, so there is nothing to polish. Refresh and try again.", nil)
	}
	_, cmds := corestandup.Step(st, corestandup.PolishRequested{RequestID: req.RequestID})
	for _, c := range cmds {
		draft, ok := c.(corestandup.SubmitAskDraft)
		if !ok {
			continue
		}
		task, err := s.d.Polish.SubmitPolish(ctx, draft.RequestID, project, draft.Text, req.Agent, req.Model)
		if err != nil {
			return api.ActionResultDTO{}, fmt.Errorf("submit standup polish %s: %w", draft.RequestID, err)
		}
		res := api.ActionResultDTO{
			RequestID: draft.RequestID, State: api.ActionStateCompleted, TaskID: task,
			Message: "Started an Ask task to polish the standup text. Your summary is unchanged.",
		}
		if s.d.Hub != nil {
			s.d.Hub.PublishAction(api.ActionChangedMessage{RequestID: res.RequestID, State: res.State})
		}
		return res, nil
	}
	return api.ActionResultDTO{}, errors.New("standup: polish produced no submission")
}

// validAgent checks the agent and model names at the boundary: short, printable,
// no path or option syntax.
func validAgent(agent, model string) error {
	bad := func(msg string) error { return apperr.New(apperr.Invalid, msg, nil) }
	if agent == "" || len(agent) > 64 {
		return bad("Choose an agent to polish the standup.")
	}
	for _, r := range agent {
		if !(r >= 'a' && r <= 'z' || r >= 'A' && r <= 'Z' || r >= '0' && r <= '9' || r == '-' || r == '_' || r == '.') {
			return bad("The agent name is not valid.")
		}
	}
	if len(model) > 128 {
		return bad("The model name is too long.")
	}
	for _, r := range model {
		if unicode.IsControl(r) || unicode.IsSpace(r) || r == '\\' {
			return bad("The model name is not valid.")
		}
	}
	return nil
}
