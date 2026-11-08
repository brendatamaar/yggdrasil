package api

import (
	"context"
	"errors"
	"fmt"

	"github.com/brendatamaar/yggdrasil/internal/apperr"
	"github.com/brendatamaar/yggdrasil/internal/changesets"
	"github.com/brendatamaar/yggdrasil/internal/core"
	cs "github.com/brendatamaar/yggdrasil/internal/core/changesets"
	"github.com/brendatamaar/yggdrasil/internal/core/review"
	"github.com/brendatamaar/yggdrasil/internal/db"
)

// Go-only wiring (excluded from tygo): adapters from *changesets.Service to
// ChangeSetService and ReviewService. They map the service's plain "no"
// errors to apperr classes and its core values to DTOs.

// ChangeSetBackend is the part of *changesets.Service the change-set
// endpoints call.
type ChangeSetBackend interface {
	Get(ctx context.Context, id core.ChangeSetID) (changesets.View, error)
	ForTask(ctx context.Context, task core.TaskID) (core.ChangeSetID, error)
	Preflight(ctx context.Context, id core.ChangeSetID, scope, repo string) ([]cs.Warning, error)
	SetOrder(ctx context.Context, id core.ChangeSetID, raw []string, actor string) (changesets.View, error)
	ConfirmRun(ctx context.Context, id core.ChangeSetID, raw changesets.RawConfirm, actor string) (changesets.View, error)
	StopRun(ctx context.Context, id core.ChangeSetID, actor string) (changesets.View, error)
	Close(ctx context.Context, id core.ChangeSetID, actor string) (changesets.View, error)
}

// ReviewBackend is the part of *changesets.Service the note endpoints call.
type ReviewBackend interface {
	ListNotes(ctx context.Context, task core.TaskID) ([]review.Note, error)
	AddNote(ctx context.Context, task core.TaskID, raw review.RawNote, actor string) (review.Note, error)
	EditNote(ctx context.Context, task core.TaskID, id, text, actor string) (review.Note, error)
	DeleteNote(ctx context.Context, task core.TaskID, id, actor string) error
	SendNotes(ctx context.Context, task core.TaskID, ids []string, actor string, optIn bool) error
}

// NoteReader finds the task of a note (*db.Store satisfies it).
type NoteReader interface {
	GetNoteRow(ctx context.Context, id string) (db.NoteRow, error)
}

const uiActor = "user"

// changeSetErr gives an error from the change-set service an apperr class.
// A classified error passes through unchanged.
func changeSetErr(err error) error {
	if err == nil {
		return nil
	}
	if _, ok := apperr.ClassOf(err); ok {
		return err
	}
	var user *changesets.UserError
	var refused *changesets.Refused
	switch {
	case errors.As(err, &user) && user.Kind == changesets.KindConflict:
		return apperr.New(apperr.Conflict, user.Message, err)
	case errors.As(err, &user):
		return apperr.New(apperr.Invalid, user.Message, err)
	case errors.As(err, &refused):
		return apperr.New(apperr.Conflict, refused.Message, err)
	case errors.Is(err, changesets.ErrAgentBusy), errors.Is(err, changesets.ErrQueueFull):
		return apperr.New(apperr.Conflict, "The agent is busy. Try again when it has finished.", err)
	case errors.Is(err, db.ErrNotFound):
		return apperr.New(apperr.NotFound, "That change set or note does not exist (any more).", err)
	case errors.Is(err, db.ErrConflict):
		return apperr.New(apperr.Conflict, "That changed while you were working. Reload the page and try again.", err)
	}
	return err
}

type changeSetAdapter struct {
	b     ChangeSetBackend
	names RepoNamer
}

// NewChangeSetService serves the change-set endpoints over the change-set
// service. names resolves repo names for display; it may be nil.
func NewChangeSetService(b ChangeSetBackend, names RepoNamer) ChangeSetService {
	return &changeSetAdapter{b: b, names: names}
}

func (a *changeSetAdapter) Get(ctx context.Context, id core.ChangeSetID) (ChangeSetDTO, error) {
	v, err := a.b.Get(ctx, id)
	if err != nil {
		return ChangeSetDTO{}, changeSetErr(err)
	}
	return a.dto(ctx, v), nil
}

func (a *changeSetAdapter) ForTask(ctx context.Context, task core.TaskID) (ChangeSetDTO, error) {
	id, err := a.b.ForTask(ctx, task)
	if err != nil && !errors.Is(err, db.ErrNotFound) {
		return ChangeSetDTO{}, changeSetErr(err)
	}
	if err != nil || id == "" {
		return ChangeSetDTO{}, apperr.New(apperr.NotFound, "This task has no change set yet. It appears with the first proposal.", fmt.Errorf("task %s has no change set", task))
	}
	return a.Get(ctx, id)
}

func (a *changeSetAdapter) SetOrder(ctx context.Context, id core.ChangeSetID, req SetOrderRequest) (ChangeSetDTO, error) {
	raw := make([]string, len(req.Order))
	for i, r := range req.Order {
		raw[i] = string(r)
	}
	v, err := a.b.SetOrder(ctx, id, raw, uiActor)
	if err != nil {
		return ChangeSetDTO{}, changeSetErr(err)
	}
	return a.dto(ctx, v), nil
}

func (a *changeSetAdapter) Preflight(ctx context.Context, id core.ChangeSetID, req PreflightRequest) (PreflightResponse, error) {
	ws, err := a.b.Preflight(ctx, id, req.Scope.Kind, string(req.Scope.RepoID))
	if err != nil {
		return PreflightResponse{}, changeSetErr(err)
	}
	return PreflightResponse{Warnings: a.warnings(ctx, ws)}, nil
}

func (a *changeSetAdapter) ConfirmRun(ctx context.Context, id core.ChangeSetID, req ConfirmRunRequest) (ChangeSetDTO, error) {
	v, err := a.b.ConfirmRun(ctx, id, changesets.RawConfirm{
		Scope: req.Scope.Kind, Repo: string(req.Scope.RepoID), Seen: req.Seen, Ack: req.Ack,
	}, uiActor)
	if err != nil {
		return ChangeSetDTO{}, changeSetErr(err)
	}
	return a.dto(ctx, v), nil
}

func (a *changeSetAdapter) StopRun(ctx context.Context, id core.ChangeSetID) (ChangeSetDTO, error) {
	v, err := a.b.StopRun(ctx, id, uiActor)
	if err != nil {
		return ChangeSetDTO{}, changeSetErr(err)
	}
	return a.dto(ctx, v), nil
}

func (a *changeSetAdapter) Close(ctx context.Context, id core.ChangeSetID) (ChangeSetDTO, error) {
	v, err := a.b.Close(ctx, id, uiActor)
	if err != nil {
		return ChangeSetDTO{}, changeSetErr(err)
	}
	return a.dto(ctx, v), nil
}

func (a *changeSetAdapter) name(ctx context.Context, r core.RepoID) string {
	if a.names == nil {
		return string(r)
	}
	if n := a.names(ctx, r); n != "" {
		return n
	}
	return string(r)
}

func (a *changeSetAdapter) warnings(ctx context.Context, ws []cs.Warning) []WarningDTO {
	out := make([]WarningDTO, 0, len(ws))
	for _, w := range ws {
		out = append(out, WarningDTO{
			Code: string(w.Code), Dependent: w.Dependent, Dependency: w.Dependency,
			Message: fmt.Sprintf("%s is merged before %s, which it depends on.", a.name(ctx, w.Dependent), a.name(ctx, w.Dependency)),
		})
	}
	return out
}

// dto converts a view. Nothing in it holds a path, a diff or a token.
func (a *changeSetAdapter) dto(ctx context.Context, v changesets.View) ChangeSetDTO {
	var open cs.Open
	d := ChangeSetDTO{ID: v.ID, TaskID: v.Task, Status: v.State.Status()}
	switch st := v.State.(type) {
	case cs.Open:
		open, d.State = st, ChangeSetStateOpen
	case cs.Confirming:
		open, d.State = st.Open, ChangeSetStateConfirming
		d.Confirming = &ConfirmingDTO{Scope: scopeDTO(st.Scope), Current: st.Current, Remaining: len(st.Queue)}
	case cs.Shipped:
		open, d.State = st.Open, ChangeSetStateShipped
	case cs.Closed:
		open, d.State = st.Open, ChangeSetStateClosed
	}
	d.Order = append([]core.RepoID{}, open.Set.Order...)
	d.SuggestedOrder = append([]core.RepoID{}, v.Suggested...)
	if len(v.Cycle) > 0 {
		d.Cycle = append([]core.RepoID{}, v.Cycle...)
	}
	d.Repos = make([]ChangeSetRepoDTO, 0, len(open.Set.Repos))
	for _, r := range open.Set.Repos {
		d.Repos = append(d.Repos, ChangeSetRepoDTO{RepoID: r, Name: a.name(ctx, r)})
	}
	d.Members = make([]ChangeSetMemberDTO, 0, len(open.Members))
	for _, m := range open.Members {
		d.Members = append(d.Members, ChangeSetMemberDTO{ProposalID: m.ID, RepoID: m.Repo, Kind: m.Kind, Status: m.Status, Hash: m.Hash})
	}
	d.Links = make([]MRLinkDTO, 0, len(open.Links))
	for _, r := range open.Set.Order {
		if u, ok := open.Links[r]; ok {
			d.Links = append(d.Links, MRLinkDTO{RepoID: r, URL: u})
		}
	}
	d.Run = RunDTO{State: RunStateIdle}
	switch r := open.LastRun.(type) {
	case cs.RunHalted:
		d.Run = RunDTO{State: RunStateHalted, At: r.At, Reason: r.Reason}
	case cs.RunCompleted:
		d.Run = RunDTO{State: RunStateCompleted}
	}
	d.Warnings = a.warnings(ctx, v.Warnings)
	return d
}

func scopeDTO(s cs.Scope) ScopeDTO {
	if r, ok := s.(cs.ScopeRepo); ok {
		return ScopeDTO{Kind: "repo", RepoID: r.Repo}
	}
	return ScopeDTO{Kind: "all"}
}

type reviewAdapter struct {
	b     ReviewBackend
	notes NoteReader
}

// NewReviewService serves the review-note endpoints. notes finds the task of
// a note for edit and delete.
func NewReviewService(b ReviewBackend, notes NoteReader) ReviewService {
	return &reviewAdapter{b: b, notes: notes}
}

func (a *reviewAdapter) List(ctx context.Context, task core.TaskID) (ReviewNotesResponse, error) {
	ns, err := a.b.ListNotes(ctx, task)
	if err != nil {
		return ReviewNotesResponse{}, changeSetErr(err)
	}
	return ReviewNotesResponse{Notes: noteDTOs(ns)}, nil
}

func (a *reviewAdapter) Create(ctx context.Context, task core.TaskID, req CreateNoteRequest) (ReviewNoteDTO, error) {
	n, err := a.b.AddNote(ctx, task, review.RawNote{
		Repo: string(req.RepoID), Path: req.Path, Line: req.Line, Side: req.Side, Text: req.Text, DiffHash: req.DiffHash,
	}, uiActor)
	if err != nil {
		return ReviewNoteDTO{}, changeSetErr(err)
	}
	return noteDTO(n), nil
}

func (a *reviewAdapter) taskOf(ctx context.Context, id review.NoteID) (core.TaskID, error) {
	if err := id.Validate(); err != nil {
		return "", apperr.New(apperr.NotFound, "That note does not exist (any more).", err)
	}
	row, err := a.notes.GetNoteRow(ctx, string(id))
	if err != nil {
		return "", changeSetErr(err)
	}
	return row.Task, nil
}

func (a *reviewAdapter) Edit(ctx context.Context, id review.NoteID, req EditNoteRequest) (ReviewNoteDTO, error) {
	task, err := a.taskOf(ctx, id)
	if err != nil {
		return ReviewNoteDTO{}, err
	}
	n, err := a.b.EditNote(ctx, task, string(id), req.Text, uiActor)
	if err != nil {
		return ReviewNoteDTO{}, changeSetErr(err)
	}
	return noteDTO(n), nil
}

func (a *reviewAdapter) Delete(ctx context.Context, id review.NoteID) error {
	task, err := a.taskOf(ctx, id)
	if err != nil {
		return err
	}
	return changeSetErr(a.b.DeleteNote(ctx, task, string(id), uiActor))
}

func (a *reviewAdapter) Send(ctx context.Context, task core.TaskID, req SendNotesRequest) (SendNotesResponse, error) {
	if _, err := changesets.ParseNoteIDs(req.IDs); err != nil {
		return SendNotesResponse{}, apperr.New(apperr.Invalid, err.Error(), err)
	}
	if err := a.b.SendNotes(ctx, task, req.IDs, uiActor, req.OptIn); err != nil {
		return SendNotesResponse{}, changeSetErr(err)
	}
	all, err := a.b.ListNotes(ctx, task)
	if err != nil {
		return SendNotesResponse{}, changeSetErr(err)
	}
	out := SendNotesResponse{Notes: []ReviewNoteDTO{}}
	for _, n := range all {
		for _, id := range req.IDs {
			if string(n.ID) != id {
				continue
			}
			out.Notes = append(out.Notes, noteDTO(n))
			if sent, ok := n.State.(review.NoteSent); ok {
				out.TurnID = string(sent.Turn)
			}
		}
	}
	return out, nil
}

func noteDTOs(ns []review.Note) []ReviewNoteDTO {
	out := make([]ReviewNoteDTO, 0, len(ns))
	for _, n := range ns {
		out = append(out, noteDTO(n))
	}
	return out
}

func noteDTO(n review.Note) ReviewNoteDTO {
	state := NoteOpen
	switch n.State.(type) {
	case review.NoteSending:
		state = NoteSending
	case review.NoteSent:
		state = NoteSent
	case review.NoteOutdated:
		state = NoteOutdated
	}
	return ReviewNoteDTO{
		ID: string(n.ID), TaskID: n.Task, RepoID: n.Repo, Path: string(n.Anchor.Path), Line: n.Anchor.Line,
		Side: string(n.Anchor.Side), Text: n.Text, State: state, CreatedAt: n.Created.UTC(),
	}
}
