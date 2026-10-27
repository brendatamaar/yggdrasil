package composer

import (
	"context"
	"errors"
	"fmt"
	"time"

	"github.com/brendatamaar/yggdrasil/internal/api"
	"github.com/brendatamaar/yggdrasil/internal/apperr"
	"github.com/brendatamaar/yggdrasil/internal/core"
	coretasks "github.com/brendatamaar/yggdrasil/internal/core/tasks"
	"github.com/brendatamaar/yggdrasil/internal/db"
	"github.com/brendatamaar/yggdrasil/internal/git"
	"github.com/brendatamaar/yggdrasil/internal/tasks"
)

// Differ reads a diff (*git.Git).
type Differ interface {
	Diff(ctx context.Context, dir string, o git.DiffOptions) (string, error)
}

// TaskReader reads what Draft waits for. The default reads the store.
type TaskReader interface {
	Status(ctx context.Context, id core.TaskID) (core.TaskStatus, error)
	Events(ctx context.Context, id core.TaskID) ([]db.TaskEvent, error)
}

type storeReader struct{ st *db.Store }

func (r storeReader) Status(ctx context.Context, id core.TaskID) (core.TaskStatus, error) {
	st, err := r.st.GetTaskState(ctx, id)
	if err != nil {
		return "", err
	}
	return st.Row.Status, nil
}

func (r storeReader) Events(ctx context.Context, id core.TaskID) ([]db.TaskEvent, error) {
	return r.st.ListTaskEvents(ctx, id, 0)
}

// Draft asks an agent, in Ask mode, to write a commit message or an MR
// description from the repo's current diff and returns the text for the user
// to edit. The draft task goes through the same admission, trust and integrity
// checks as any Ask task, and is dismissed when it ends so it leaves the
// board. Nothing is written to the repo.
func (s *Service) Draft(ctx context.Context, req api.DraftRequest) (api.DraftResponse, error) {
	if s.c.Tasks == nil || s.c.Git == nil {
		return api.DraftResponse{}, apperr.New(apperr.Unavailable, "Drafting isn't set up in this Yggdrasil.", nil)
	}
	title := "Draft commit message"
	switch req.Kind {
	case KindCommitMessage:
	case KindMRDescription:
		title = "Draft MR description"
	default:
		return api.DraftResponse{}, apperr.New(apperr.Invalid, "Choose what to draft: a commit message or an MR description.", nil)
	}
	if req.Agent == "" {
		return api.DraftResponse{}, apperr.New(apperr.Invalid, "Choose an agent for the draft.", nil)
	}
	if req.Model != "" && !coretasks.ValidModelName(req.Model) {
		return api.DraftResponse{}, apperr.New(apperr.Invalid, "That model name isn't valid. Use letters, digits, dots, dashes and colons, up to 64 characters.", nil)
	}
	repo, err := s.c.Store.GetRepo(ctx, req.RepoID) // tracked repos only
	if err != nil {
		if errors.Is(err, db.ErrNotFound) {
			return api.DraftResponse{}, apperr.New(apperr.NotFound, "That repo isn't tracked (any more).", err)
		}
		return api.DraftResponse{}, fmt.Errorf("read repo: %w", err)
	}
	if repo.ProjectID != req.ProjectID {
		return api.DraftResponse{}, apperr.New(apperr.Invalid, "That repo isn't part of the project.", nil)
	}
	dir, err := s.draftDir(ctx, repo, req.TaskID)
	if err != nil {
		return api.DraftResponse{}, err
	}
	diff, err := s.readDiff(ctx, dir, repo, req.Kind)
	if err != nil {
		return api.DraftResponse{}, err
	}

	res, err := s.c.Tasks.Message(ctx, tasks.MessageRequest{
		ProjectID: req.ProjectID, Prompt: BuildDraftPrompt(req.Kind, diff, s.c.NewID("dr")),
		Mode: string(core.ModeAsk), Scope: string(core.ScopeRepo), Repos: []string{string(repo.ID)},
		Agent: req.Agent, Model: req.Model, Title: title,
	})
	if err != nil {
		return api.DraftResponse{}, messageErr(err)
	}
	id := res.Task.ID
	// The draft task always leaves the board, whatever happened.
	defer s.finish(id)

	raw, err := s.await(ctx, id)
	if err != nil {
		return api.DraftResponse{}, err
	}
	d, err := CleanDraft(req.Kind, raw)
	if err != nil {
		return api.DraftResponse{}, apperr.New(apperr.Unavailable, "The agent didn't return a draft. Try again or write the text yourself.", err)
	}
	return api.DraftResponse{Text: d.Text, Title: d.Title, Description: d.Description, PushDescription: d.PushDescription}, nil
}

// draftDir is the folder the diff is read in: the worktree of the given task
// for this repo, else the repo's main checkout.
func (s *Service) draftDir(ctx context.Context, repo core.Repo, task core.TaskID) (string, error) {
	if task == "" {
		return repo.Path, nil
	}
	wts, err := s.c.Store.ListWorktrees(ctx, task)
	if err != nil {
		return "", fmt.Errorf("list worktrees: %w", err)
	}
	for _, w := range wts {
		if w.RepoID == repo.ID {
			return w.Path, nil
		}
	}
	return "", apperr.New(apperr.NotFound, "That task has no worktree for this repo.", nil)
}

// readDiff reads the diff to describe: for a commit message the staged and
// unstaged changes, for an MR the commits of the current branch beyond the
// default branch. It is read-only and capped at MaxDiffBytes.
func (s *Service) readDiff(ctx context.Context, dir string, repo core.Repo, kind string) (string, error) {
	var opts []git.DiffOptions
	if kind == KindMRDescription {
		opts = []git.DiffOptions{{Mode: git.DiffRefs, From: repo.DefaultBranch, To: "HEAD", Limit: MaxDiffBytes}}
	} else {
		opts = []git.DiffOptions{{Mode: git.DiffStaged, Limit: MaxDiffBytes}, {Mode: git.DiffWorktree, Limit: MaxDiffBytes}}
	}
	var diff string
	for _, o := range opts {
		part, err := s.c.Git.Diff(ctx, dir, o)
		if err != nil {
			return "", apperr.New(apperr.Unavailable, git.UserMessage(err), err)
		}
		diff += part
		if len(diff) > MaxDiffBytes {
			return "", apperr.New(apperr.Invalid, fmt.Sprintf("The changes are too large to draft from (over %d KiB). Write the text yourself or commit in smaller steps.", MaxDiffBytes/1024), nil)
		}
	}
	if diff == "" {
		return "", apperr.New(apperr.Invalid, "There are no changes to describe.", nil)
	}
	return diff, nil
}

// messageErr turns the task service's refusals into API errors.
func messageErr(err error) error {
	var ue *coretasks.UserError
	var re *tasks.RejectedError
	switch {
	case errors.As(err, &ue):
		return apperr.New(apperr.Invalid, ue.Message, err)
	case errors.As(err, &re):
		if re.Reason == "agent_unavailable" || re.Reason == "shutting_down" {
			return apperr.New(apperr.Unavailable, re.Message, err)
		}
		return apperr.New(apperr.Conflict, re.Message, err)
	case errors.Is(err, db.ErrNotFound):
		return apperr.New(apperr.NotFound, "That item doesn't exist (any more).", err)
	}
	return err
}

// await waits until the draft task ends and returns the agent's last message.
// On the timeout, or when the caller goes away, the task is cancelled.
func (s *Service) await(ctx context.Context, id core.TaskID) (string, error) {
	timeout := time.NewTimer(s.c.DraftTimeout)
	defer timeout.Stop()
	tick := time.NewTicker(s.c.DraftPoll)
	defer tick.Stop()
	for {
		select {
		case <-ctx.Done():
			return "", ctx.Err()
		case <-timeout.C:
			return "", apperr.New(apperr.Unavailable, "The agent took too long to write the draft. Try again or write the text yourself.", context.DeadlineExceeded)
		case <-tick.C:
		}
		status, err := s.reader().Status(ctx, id)
		if err != nil {
			return "", fmt.Errorf("read the draft task: %w", err)
		}
		switch status {
		case core.TaskReview, core.TaskDone:
			events, err := s.reader().Events(ctx, id)
			if err != nil {
				return "", fmt.Errorf("read the draft: %w", err)
			}
			text := FinalAssistantText(events)
			if text == "" {
				return "", apperr.New(apperr.Unavailable, "The agent didn't return a draft. Try again or write the text yourself.", nil)
			}
			return text, nil
		case core.TaskFailed, core.TaskCancelled:
			return "", apperr.New(apperr.Unavailable, "The agent couldn't write the draft. Run `ygg doctor` to check it, or write the text yourself.", nil)
		case core.TaskNeedsYou:
			return "", apperr.New(apperr.Unavailable, "The agent needs your input to write the draft. Write the text yourself.", nil)
		}
	}
}

func (s *Service) reader() TaskReader {
	if s.c.Reader != nil {
		return s.c.Reader
	}
	return storeReader{st: s.c.Store}
}

// finish stops a draft task that is still running and takes it off the board.
// It uses its own context: the request may be gone.
func (s *Service) finish(id core.TaskID) {
	ctx, cancel := context.WithTimeout(context.Background(), 10*time.Second)
	defer cancel()
	if status, err := s.reader().Status(ctx, id); err == nil && (status == core.TaskRunning || status == core.TaskQueued) {
		_, _ = s.c.Tasks.Cancel(ctx, id)
		for i := 0; i < 50; i++ { // let the cancel land before the dismiss
			if st, err := s.reader().Status(ctx, id); err != nil || (st != core.TaskRunning && st != core.TaskQueued) {
				break
			}
			time.Sleep(20 * time.Millisecond)
		}
	}
	_, _ = s.c.Tasks.Dismiss(ctx, id)
}
