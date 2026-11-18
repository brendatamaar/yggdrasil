package main

import (
	"context"
	"errors"
	"fmt"
	"strconv"
	"strings"
	"time"

	"github.com/brendatamaar/yggdrasil/internal/actions"
	"github.com/brendatamaar/yggdrasil/internal/api"
	"github.com/brendatamaar/yggdrasil/internal/apperr"
	"github.com/brendatamaar/yggdrasil/internal/core"
	coreactions "github.com/brendatamaar/yggdrasil/internal/core/actions"
	corenotify "github.com/brendatamaar/yggdrasil/internal/core/notify"
	coreprop "github.com/brendatamaar/yggdrasil/internal/core/proposals"
	corerepos "github.com/brendatamaar/yggdrasil/internal/core/repos"
	coretasks "github.com/brendatamaar/yggdrasil/internal/core/tasks"
	"github.com/brendatamaar/yggdrasil/internal/db"
	"github.com/brendatamaar/yggdrasil/internal/notify"
	"github.com/brendatamaar/yggdrasil/internal/platform"
	"github.com/brendatamaar/yggdrasil/internal/proposals"
	"github.com/brendatamaar/yggdrasil/internal/repos"
	"github.com/brendatamaar/yggdrasil/internal/standup"
	"github.com/brendatamaar/yggdrasil/internal/tasks"
)

// Adapters between the Phase 6 services and the ports of their consumers. They
// translate shapes and errors and decide nothing: the decisions are in the
// core packages and the owning services.

// ---- proposals for actions ----

// proposalService is the request-bound creation of internal/proposals.
type proposalService interface {
	CreateForRequest(ctx context.Context, id core.RequestID, items []proposals.RequestProposal) ([]core.ProposalID, error)
	FindForRequest(ctx context.Context, id core.RequestID) ([]core.ProposalID, error)
}

// draftSaver stores the Ask-mode draft an unreadable repo or a proposal anchor
// needs (internal/tasks).
type draftSaver interface {
	SaveWaitingDraft(ctx context.Context, id core.RequestID, d tasks.WaitingDraft) (core.TaskID, error)
}

// proposalCreator implements actions.ProposalCreator. A proposal hangs under a
// task (its change set), and an action such as a pull has none, so the adapter
// gives the repo one visible Ask-mode draft task as its anchor (no turn,
// worktree or process). The anchor is per repo with a fixed payload, so every
// request of the repo reuses it: a refused or repeated request never makes more
// than that one draft.
type proposalCreator struct {
	svc    proposalService
	drafts draftSaver
	store  interface {
		GetRepo(ctx context.Context, id core.RepoID) (core.Repo, error)
	}
}

var _ actions.ProposalCreator = (*proposalCreator)(nil)

func (c *proposalCreator) anchor(ctx context.Context, repo core.RepoID) (core.TaskID, error) {
	r, err := c.store.GetRepo(ctx, repo)
	if err != nil {
		return "", err
	}
	// The title and brief are fixed (not the repo's name) so a rename cannot
	// change the payload; the request ID is reserved, so no client can occupy it.
	return c.drafts.SaveWaitingDraft(ctx, coretasks.AnchorRequest(repo), tasks.WaitingDraft{
		Project: r.ProjectID, Repo: repo, Title: coretasks.AnchorTitle, Brief: coretasks.AnchorBrief,
		Agent: coretasks.AnchorAgent,
	})
}

func (c *proposalCreator) CreateForRequest(ctx context.Context, id core.RequestID, items []actions.ProposalSpec) ([]core.ProposalID, error) {
	out := make([]proposals.RequestProposal, len(items))
	anchors := map[core.RepoID]core.TaskID{}
	for i, it := range items {
		task := it.Task
		if task == "" {
			t, ok := anchors[it.Repo]
			if !ok {
				var err error
				if t, err = c.anchor(ctx, it.Repo); err != nil {
					return nil, fmt.Errorf("anchor the proposals of request %s: %w", id, err)
				}
				anchors[it.Repo] = t
			}
			task = t
		}
		out[i] = proposals.RequestProposal{Task: task, Repo: it.Repo, Summary: it.Summary, Kind: it.Kind, Payload: it.Payload}
	}
	return c.svc.CreateForRequest(ctx, id, out)
}

// FindForRequest is read-only. The real service answers "none" with an empty
// list and no error; the port says db.ErrNotFound, so both mean not found.
func (c *proposalCreator) FindForRequest(ctx context.Context, id core.RequestID) ([]core.ProposalID, error) {
	ids, err := c.svc.FindForRequest(ctx, id)
	if err != nil {
		return nil, err
	}
	if len(ids) == 0 {
		return nil, db.ErrNotFound
	}
	return ids, nil
}

// folderDrafts implements actions.FolderDrafts over tasks.SaveWaitingDraft.
type folderDrafts struct{ svc draftSaver }

var _ actions.FolderDrafts = folderDrafts{}

func (f folderDrafts) SaveWaitingDraft(ctx context.Context, id core.RequestID, d actions.WaitingDraft) (core.TaskID, error) {
	return f.svc.SaveWaitingDraft(ctx, id, tasks.WaitingDraft{
		Project: d.Project, Repo: d.Repo, Title: d.Title, Brief: d.Brief, Agent: d.Agent, Model: d.Model,
	})
}

// ---- api.ActionService ----

// actionsAPI implements api.ActionService over internal/actions.
type actionsAPI struct{ svc *actions.Service }

var _ api.ActionService = actionsAPI{}

// actionErr classifies an error of the action service for the API.
func actionErr(err error) error {
	switch {
	case err == nil:
		return nil
	case errors.Is(err, actions.ErrClosed):
		return api.ShuttingDown(err)
	case errors.Is(err, coreactions.ErrInvalidRequest):
		msg := strings.TrimPrefix(err.Error(), coreactions.ErrInvalidRequest.Error()+": ")
		return apperr.New(apperr.Invalid, "That action isn't valid: "+msg, err)
	}
	return err
}

var kindTitles = map[coreactions.Kind]string{
	coreactions.DiagnosePipeline: "Diagnose with agent",
	coreactions.FixPipeline:      "Fix in worktree",
	coreactions.ReviewMR:         "Review with agent",
	coreactions.ProposeCommits:   "Propose commits",
	coreactions.PullAndCheck:     "Pull and check",
	coreactions.ImportIssue:      "Add draft task",
	coreactions.DiagnoseBuild:    "Diagnose build",
	coreactions.DiagnoseRepo:     "Diagnose repo",
	coreactions.ProposeIgnore:    "Review ignore suggestions",
}

func (a actionsAPI) Catalogue(ctx context.Context, repo core.RepoID) (api.ActionCatalogueResponse, error) {
	cat, err := a.svc.Offerings(ctx, actions.Scope{Repo: repo})
	if err != nil {
		return api.ActionCatalogueResponse{}, actionErr(err)
	}
	resp := api.ActionCatalogueResponse{RepoID: repo, Offerings: []api.ActionOfferingDTO{}}
	for _, r := range cat.Repos {
		for _, o := range r.Offerings {
			resp.Offerings = append(resp.Offerings, api.ActionOfferingDTO{
				Kind: api.ActionKind(o.Kind), Reason: api.AttentionReason(o.Reason), Title: kindTitles[o.Kind],
				Mode: o.Mode, Context: api.ActionContextAvailability(o.Context), SourceID: o.SourceID,
				WaitingForFolder: o.WaitingForFolder, Note: o.Note,
			})
		}
	}
	return resp, nil
}

func requestOf(d api.ActionRequestDTO) (coreactions.Request, error) {
	return coreactions.ParseRequest(string(d.RequestID), string(d.RepoID), string(d.Kind), d.SourceID, d.Agent, d.Model)
}

func (a actionsAPI) Preview(ctx context.Context, dto api.ActionRequestDTO) (api.ActionPreviewDTO, error) {
	req, err := requestOf(dto)
	if err != nil {
		return api.ActionPreviewDTO{}, actionErr(err)
	}
	p, err := a.svc.Preview(ctx, req)
	if err != nil {
		return api.ActionPreviewDTO{}, actionErr(err)
	}
	if p.Refusal != "" {
		return api.ActionPreviewDTO{}, apperr.New(apperr.Conflict, p.Message, nil)
	}
	return api.ActionPreviewDTO{
		RequestID: dto.RequestID, Digest: p.Digest, Kind: dto.Kind, Mode: p.Mode,
		Source: p.Context.Source, ContextText: p.Context.Text, Truncated: p.Context.Truncated,
		Unavailable: p.Context.Unavailable, Context: api.ActionContextAvailability(p.Context.Availability),
		WaitingForFolder: p.Context.WaitingForFolder, ExpectedHead: p.ExpectedHead,
	}, nil
}

func resultOf(o actions.Outcome) api.ActionResultDTO {
	return api.ActionResultDTO{
		RequestID: o.RequestID, State: api.ActionRequestState(o.State), TaskID: o.Result.TaskID,
		ProposalIDs: o.Result.ProposalIDs, BatchID: o.Result.BatchID, Refusal: string(o.Refusal), Message: o.Message,
	}
}

func (a actionsAPI) Execute(ctx context.Context, dto api.ActionExecuteRequest) (api.ActionResultDTO, error) {
	req, err := requestOf(dto.ActionRequestDTO)
	if err != nil {
		return api.ActionResultDTO{}, actionErr(err)
	}
	out, err := a.svc.Dispatch(ctx, actions.Input{Request: req, Digest: dto.Digest, ExpectedHead: dto.ExpectedHead})
	if err != nil {
		return api.ActionResultDTO{}, actionErr(err)
	}
	return resultOf(out), nil
}

func (a actionsAPI) Get(ctx context.Context, id core.RequestID) (api.ActionResultDTO, error) {
	out, err := a.svc.Get(ctx, id)
	if err != nil {
		return api.ActionResultDTO{}, actionErr(err)
	}
	return resultOf(out), nil
}

// ---- api.DraftService ----

// draftAPI implements api.DraftService. An issue import goes through the action
// path (the same preview-free dispatch, catalogue rules and request record),
// and starting a draft goes through the ordinary admission in internal/tasks.
type draftAPI struct {
	actions *actions.Service
	tasks   interface {
		StartDraft(ctx context.Context, start coretasks.DraftStart, agent string) (tasks.MessageResult, error)
	}
	store interface {
		GetTask(ctx context.Context, id core.TaskID) (core.Task, error)
	}
	now func() time.Time
}

var _ api.DraftService = draftAPI{}

func (d draftAPI) ImportIssueDraft(ctx context.Context, repo core.RepoID, number int, req api.ImportIssueDraftRequest) (api.DraftTaskResponse, error) {
	cr, err := coreactions.ParseRequest(string(req.RequestID), string(repo), string(coreactions.ImportIssue), strconv.Itoa(number), req.Agent, req.Model)
	if err != nil {
		return api.DraftTaskResponse{}, actionErr(err)
	}
	before := d.now()
	out, err := d.actions.Dispatch(ctx, actions.Input{Request: cr})
	if err != nil {
		return api.DraftTaskResponse{}, actionErr(err)
	}
	if out.State != db.ActionCompleted || out.Result.TaskID == "" {
		msg := out.Message
		if msg == "" {
			msg = "The issue could not be imported."
		}
		return api.DraftTaskResponse{}, apperr.New(apperr.Conflict, msg, nil)
	}
	t, err := d.store.GetTask(ctx, out.Result.TaskID)
	if err != nil {
		return api.DraftTaskResponse{}, fmt.Errorf("read draft task %s: %w", out.Result.TaskID, err)
	}
	return api.DraftTaskResponse{Task: t, Created: !out.Replayed && !t.CreatedAt.Before(before.Add(-time.Second))}, nil
}

func (d draftAPI) StartDraft(ctx context.Context, task core.TaskID, req api.StartDraftRequest) (api.TaskResponse, error) {
	res, err := d.tasks.StartDraft(ctx, coretasks.DraftStart{Task: task, Mode: req.Mode, OptIn: req.OptIn, Model: req.Model}, "")
	if err != nil {
		return api.TaskResponse{}, err
	}
	return api.TaskResponse{Task: res.Task, Warning: res.Warning}, nil
}

// ---- api.NoiseService ----

// noiseAPI implements api.NoiseService. Candidates come from the pure noise
// analysis of the last status read; the ignore proposal is pending until the
// user confirms it (WS-09).
type noiseAPI struct {
	scope     *scopeReader
	proposals interface {
		List(ctx context.Context, f proposals.ListFilter) ([]proposals.View, error)
		Get(ctx context.Context, id core.ProposalID) (proposals.View, error)
	}
	creator *proposalCreator
}

var _ api.NoiseService = noiseAPI{}

func (n noiseAPI) candidates(ctx context.Context, repo core.RepoID) ([]corerepos.NoiseCandidate, corerepos.NoiseFacts, error) {
	facts, err := n.scope.NoiseFacts(ctx, repo)
	if err != nil {
		return nil, facts, err
	}
	st, _ := corerepos.NoiseStep(corerepos.Noise{Repo: repo, State: corerepos.NoSuggestion{}}, corerepos.NoiseObserved{Facts: facts})
	if s, ok := st.State.(corerepos.Suggested); ok {
		return s.Candidates, facts, nil
	}
	return nil, facts, nil
}

func (n noiseAPI) Noise(ctx context.Context, repo core.RepoID) (api.NoiseResponse, error) {
	cands, _, err := n.candidates(ctx, repo)
	switch {
	case errors.Is(err, db.ErrNotFound):
		return api.NoiseResponse{}, err
	case errors.Is(err, repos.ErrNoiseUnavailable):
		return api.NoiseResponse{RepoID: repo, Candidates: []api.NoiseCandidateDTO{}, Message: "This repo could not be read, so no ignore suggestions are available."}, nil
	case err != nil:
		return api.NoiseResponse{}, fmt.Errorf("noise of repo %s: %w", repo, err)
	}
	resp := api.NoiseResponse{RepoID: repo, Available: true, Candidates: []api.NoiseCandidateDTO{}}
	for _, c := range cands {
		resp.Candidates = append(resp.Candidates, api.NoiseCandidateDTO{
			Directory: c.Directory, Pattern: c.Pattern, SampleCount: c.SampleCount, Incomplete: c.Incomplete,
			Evidence: api.NoiseEvidenceDTO{Total: c.Evidence.Total, Sampled: c.Evidence.Sampled, UnderDirectory: c.Evidence.UnderDirectory},
		})
	}
	if len(resp.Candidates) == 0 {
		resp.Message = "Nothing in this repo looks like generated files that should be ignored."
	}
	if views, lerr := n.proposals.List(ctx, proposals.ListFilter{Status: core.ProposalPending}); lerr == nil {
		for _, v := range views {
			if v.Kind == core.ProposalIgnoreEntries && v.Repo == repo {
				resp.PendingProposalID = v.ID
			}
		}
	}
	return resp, nil
}

func (n noiseAPI) CreateIgnoreProposal(ctx context.Context, repo core.RepoID, req api.CreateIgnoreProposalRequest) (api.ProposalDTO, error) {
	cands, _, err := n.candidates(ctx, repo)
	if err != nil {
		if errors.Is(err, repos.ErrNoiseUnavailable) {
			return api.ProposalDTO{}, &coretasks.UserError{Message: "This repo could not be read, so nothing can be proposed. Try again when it can."}
		}
		return api.ProposalDTO{}, err
	}
	// Only entries that are current candidates: a stale list is not proposed.
	current := map[string]bool{}
	for _, c := range cands {
		current[c.Pattern] = true
	}
	for _, e := range req.Entries {
		if !current[e] {
			return api.ProposalDTO{}, &coretasks.UserError{Message: "The suggestions changed since you looked. Reload them and choose again."}
		}
	}
	// The server reads the current .gitignore itself and pins its hash (or its
	// absence), so a file that changed before the confirmation refuses the write.
	r, err := n.scope.tracked(ctx, repo)
	if err != nil {
		return api.ProposalDTO{}, err
	}
	action, err := ignoreAction(ctx, repo, r.Path, req.Entries)
	if err != nil {
		return api.ProposalDTO{}, err
	}
	kind, payload := coreprop.RowOf(action)
	ids, err := n.creator.CreateForRequest(ctx, req.RequestID, []actions.ProposalSpec{{
		Repo: repo, Kind: kind, Payload: payload, Summary: "Ignore generated files (" + strconv.Itoa(len(req.Entries)) + " entries)",
	}})
	if err != nil {
		return api.ProposalDTO{}, err
	}
	v, err := n.proposals.Get(ctx, ids[0])
	if err != nil {
		return api.ProposalDTO{}, err
	}
	return api.NewProposalDTO(v), nil
}

// ignoreAction builds the pinned IgnoreEntries action of a repo: it reads the
// repo's .gitignore through the guarded platform reader (no file is written).
func ignoreAction(ctx context.Context, repo core.RepoID, dir string, entries []string) (coreprop.IgnoreEntries, error) {
	snap, err := platform.NewIgnoreWriter().ReadIgnore(ctx, dir)
	if err != nil {
		return coreprop.IgnoreEntries{}, &coretasks.UserError{Message: "Yggdrasil could not read this repo's .gitignore. It may be in use, a link, or larger than 1 MiB."}
	}
	return coreprop.IgnoreEntries{
		Where: coreprop.MainCheckout{Repo: repo}, Entries: append([]string(nil), entries...),
		ExpectedHash: snap.Hash, ExpectedAbsent: snap.Absent,
	}, nil
}

// ---- api.NoticeService ----

// noticeAPI implements api.NoticeService over the notification history.
type noticeAPI struct{ prod *notify.Producer }

var _ api.NoticeService = noticeAPI{}

func (n noticeAPI) Notices(ctx context.Context, after int64, limit int) (api.NoticesResponse, error) {
	page, err := n.prod.ListNotices(ctx, after, limit)
	switch {
	case errors.Is(err, notify.ErrClosed):
		return api.NoticesResponse{}, api.ShuttingDown(err)
	case errors.Is(err, notify.ErrInvalid):
		return api.NoticesResponse{}, apperr.New(apperr.Invalid, "The notification query isn't valid.", err)
	case err != nil:
		return api.NoticesResponse{}, err
	}
	resp := api.NoticesResponse{Notices: make([]api.NoticeDTO, 0, len(page.Notices)), Oldest: page.Oldest, Newest: page.Newest, Gap: page.Gap}
	for _, x := range page.Notices {
		resp.Notices = append(resp.Notices, api.NoticeDTO{
			ID: x.ID, TaskID: x.Task, TaskTitle: x.TaskTitle, Status: api.NoticeStatus(x.Status),
			Revision: x.SourceRevision, CreatedAt: x.CreatedAt, Cursor: x.Cursor,
		})
	}
	return resp, nil
}

var _ = corenotify.MaxPage

// ---- api.StandupService ----

// standupAPI wraps the standup service to classify its errors for the API.
type standupAPI struct{ svc *standup.Service }

var _ api.StandupService = standupAPI{}

func standupErr(err error) error {
	if errors.Is(err, standup.ErrSuperseded) {
		return api.TryAgain(err)
	}
	return err
}

func (s standupAPI) Get(ctx context.Context, project core.ProjectID, from, to time.Time) (api.StandupResponse, error) {
	r, err := s.svc.Get(ctx, project, from, to)
	return r, standupErr(err)
}

func (s standupAPI) Polish(ctx context.Context, project core.ProjectID, req api.StandupPolishRequest) (api.ActionResultDTO, error) {
	r, err := s.svc.Polish(ctx, project, req)
	return r, standupErr(err)
}

// polisher implements standup.Polisher: one visible Ask task through the
// request-bound task submission, never from a refresh. The mode is fixed here;
// no caller can choose another.
type polisher struct {
	scope standup.ScopeReader
	tasks interface {
		SubmitWithRequest(ctx context.Context, id core.RequestID, req tasks.MessageRequest) (tasks.MessageResult, error)
		LookupRequestKey(ctx context.Context, id core.RequestID, key string) (core.TaskID, error)
	}
}

var _ standup.Polisher = polisher{}

// FindPolish answers a repeat from the stored link: the task made for id when
// its stored digest is that of key. The text is not needed (and not rebuilt).
func (p polisher) FindPolish(ctx context.Context, id core.RequestID, key string) (core.TaskID, bool, error) {
	task, err := p.tasks.LookupRequestKey(ctx, id, key)
	switch {
	case errors.Is(err, db.ErrNotFound):
		return "", false, nil
	case errors.Is(err, db.ErrDigestMismatch):
		return "", false, standup.ErrRequestReused
	case err != nil:
		return "", false, err
	}
	return task, true, nil
}

func (p polisher) SubmitPolish(ctx context.Context, id core.RequestID, key string, project core.ProjectID, text, agent, model string) (core.TaskID, error) {
	// An Ask task reads one tracked repo at a time (the whole-project scope needs
	// a write-capable mode), so the polish task is attached to the project's
	// first ready repo; it needs no repo content, only the summary text.
	rs, err := p.scope.Repos(ctx, project)
	if err != nil {
		return "", err
	}
	if len(rs) == 0 {
		return "", &coretasks.UserError{Message: "This project has no repo to attach the task to."}
	}
	res, err := p.tasks.SubmitWithRequest(ctx, id, tasks.MessageRequest{
		ProjectID: project, Mode: string(core.ModeAsk), Scope: string(core.ScopeRepo), Repos: []string{string(rs[0].ID)},
		Prompt: "Rewrite this standup summary as short, friendly notes for a team chat. Keep every fact; do not add anything.\n\n" + text,
		Agent:  agent, Model: model, Title: "Polish the standup",
		// The digest is over the request's fixed fields, not over the text.
		RequestKey: key,
	})
	if errors.Is(err, db.ErrDigestMismatch) {
		return "", standup.ErrRequestReused
	}
	if err != nil {
		return "", err
	}
	return res.Task.ID, nil
}
