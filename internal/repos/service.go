package repos

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"log/slog"
	"slices"
	"strings"
	"time"

	"github.com/brendatamaar/yggdrasil/internal/apperr"
	"github.com/brendatamaar/yggdrasil/internal/audit"
	"github.com/brendatamaar/yggdrasil/internal/core"
	corerepos "github.com/brendatamaar/yggdrasil/internal/core/repos"
	"github.com/brendatamaar/yggdrasil/internal/db"
	"github.com/brendatamaar/yggdrasil/internal/safety"
)

// StatusRefresher is what tracking needs from repo-status: reload the set of
// active repos after any tracking change (so untracked repos leave the status
// and the counts), and read the added or restored repos now. It is nil-safe:
// a nil refresher means no refresh.
type StatusRefresher interface {
	ReloadRepos(ctx context.Context) error
	Refresh(ctx context.Context, ids []core.RepoID, reason corerepos.RefreshReason) error
}

// ReposPublisher tells subscribers that the repo list changed (the repos topic
// of the API). Nil means nobody listens. *api.Server implements it.
type ReposPublisher interface {
	PublishReposChanged()
}

// Deps are what NewTrackingService needs. Only Store and Redactor are
// required; the rest default to the live implementations.
type Deps struct {
	Store    *db.Store
	Redactor safety.Redactor
	Scanner  Scanner
	Dirs     DirLister
	Status   StatusRefresher
	// Publisher is told after every commit that changed the repo list.
	Publisher ReposPublisher
	Log       *slog.Logger
	// Clock and IDs are the shell's sources of time and identifiers; the
	// core receives them as input fields.
	Clock  func() time.Time
	IDs    func(prefix string) string
	Limits Limits
}

// TrackingService implements api.TrackingService (docs/design/repo-tracking.md).
type TrackingService struct {
	d Deps
}

// NewTrackingService returns the service.
func NewTrackingService(d Deps) (*TrackingService, error) {
	switch {
	case d.Store == nil:
		return nil, errors.New("repos: a store is required")
	case d.Redactor == nil:
		return nil, errors.New("repos: a redactor is required (audit entries must be redacted)")
	}
	if d.Scanner == nil {
		d.Scanner = FSScanner{}
	}
	if d.Dirs == nil {
		d.Dirs = FSDirs{}
	}
	if d.Log == nil {
		d.Log = slog.New(safety.NewLogHandler(slog.DiscardHandler, d.Redactor))
	}
	if d.Clock == nil {
		d.Clock = func() time.Time { return time.Now().UTC() }
	}
	if d.IDs == nil {
		d.IDs = db.NewID
	}
	if d.Limits == (Limits{}) {
		d.Limits = DefaultLimits
	}
	return &TrackingService{d: d}, nil
}

// BrowseDirs implements api.TrackingService.
func (s *TrackingService) BrowseDirs(ctx context.Context, path string) (corerepos.DirListing, error) {
	cp, err := ParseBrowsePath(path)
	if err != nil {
		return corerepos.DirListing{}, err
	}
	if cp == "" {
		roots, err := s.d.Dirs.Roots()
		if err != nil {
			return corerepos.DirListing{}, apperr.New(apperr.Unavailable, "The drives couldn't be listed.", err)
		}
		out := corerepos.DirListing{Entries: make([]corerepos.DirEntry, 0, len(roots))}
		for _, r := range roots {
			out.Entries = append(out.Entries, corerepos.DirEntry{Name: r, Path: r})
		}
		return out, nil
	}
	dirs, err := s.d.Dirs.ListDirs(ctx, cp)
	if err != nil {
		return corerepos.DirListing{}, err
	}
	out := corerepos.DirListing{Path: string(cp), Parent: parentOf(cp), Entries: make([]corerepos.DirEntry, 0, len(dirs))}
	for _, d := range dirs {
		out.Entries = append(out.Entries, corerepos.DirEntry{Name: d.Name, Path: d.Path, IsRepo: d.IsRepo})
	}
	return out, nil
}

func (s *TrackingService) load(ctx context.Context) (corerepos.Workspace, error) {
	ws, err := s.d.Store.GetWorkspace(ctx)
	if err != nil {
		return corerepos.Workspace{}, err
	}
	return ws, nil
}

// Preview implements api.TrackingService.
func (s *TrackingService) Preview(ctx context.Context, path string) (corerepos.Preview, error) {
	cp, err := ParseFolder(path)
	if err != nil {
		return corerepos.Preview{}, err
	}
	scan, err := s.d.Scanner.Scan(ctx, cp, s.d.Limits)
	if err != nil {
		return corerepos.Preview{}, fmt.Errorf("scan %s: %w", cp, err)
	}
	ws, err := s.load(ctx)
	if err != nil {
		return corerepos.Preview{}, err
	}
	return corerepos.BuildPreview(cp, scan, ws), nil
}

// Projects implements api.TrackingService: Active projects, each with its
// Active repos, ordered by name.
func (s *TrackingService) Projects(ctx context.Context) ([]corerepos.Project, error) {
	ws, err := s.load(ctx)
	if err != nil {
		return nil, err
	}
	var out []corerepos.Project
	for _, p := range ws.Projects {
		if _, ok := p.Life.(corerepos.Active); !ok {
			continue
		}
		repos := make(map[core.RepoID]corerepos.Repo, len(p.Repos))
		for id, r := range p.Repos {
			if _, ok := r.Life.(corerepos.Active); ok {
				repos[id] = r
			}
		}
		p.Repos = repos
		out = append(out, p)
	}
	slices.SortFunc(out, func(a, b corerepos.Project) int { return strings.Compare(a.Name, b.Name) })
	return out, nil
}

// withIDs gives every pick a fresh repo ID. Only picks that need a new row
// use it; the core ignores it otherwise.
func (s *TrackingService) withIDs(picks []corerepos.Pick) []corerepos.Pick {
	out := make([]corerepos.Pick, len(picks))
	for i, p := range picks {
		p.NewRepoID = core.RepoID(s.d.IDs("rep"))
		out[i] = p
	}
	return out
}

// checkPicks asks the disk about every pick and returns the picks spelled as
// on disk; two spellings of one folder are one pick.
func (s *TrackingService) checkPicks(ctx context.Context, path corerepos.CleanPath, picks []corerepos.Pick) ([]corerepos.Pick, error) {
	seen := make(map[corerepos.RelPath]bool, len(picks))
	out := make([]corerepos.Pick, 0, len(picks))
	for _, p := range picks {
		rel, err := s.d.Scanner.CheckRepo(ctx, path, p.RelPath)
		if err != nil {
			return nil, err
		}
		if seen[rel] {
			return nil, invalid(fmt.Sprintf("The repo folder %q is selected twice.", string(rel)), nil)
		}
		seen[rel] = true
		p.RelPath = rel
		out = append(out, p)
	}
	return out, nil
}

// Track implements api.TrackingService.
func (s *TrackingService) Track(ctx context.Context, path, name string, picks []corerepos.Pick, actor corerepos.Actor) (corerepos.Outcome, error) {
	cp, err := ParseFolder(path)
	if err != nil {
		return nil, err
	}
	n, err := ParseName(name)
	if err != nil {
		return nil, err
	}
	ps, err := ParsePicks(cp, picks)
	if err != nil {
		return nil, err
	}
	ps, err = s.checkPicks(ctx, cp, ps)
	if err != nil {
		return nil, err
	}
	return s.decide(ctx, func(now time.Time) corerepos.Input {
		return corerepos.TrackProject{
			NewProjectID: core.ProjectID(s.d.IDs("prj")), Name: n, Path: cp,
			Repos: s.withIDs(ps), Actor: actor, Now: now,
		}
	})
}

// Rescan implements api.TrackingService.
func (s *TrackingService) Rescan(ctx context.Context, project core.ProjectID) (corerepos.RescanReport, error) {
	ws, err := s.load(ctx)
	if err != nil {
		return corerepos.RescanReport{}, err
	}
	p, ok := ws.Projects[project]
	if !ok || !isActive(p.Life) {
		return corerepos.RescanReport{}, errProjectNotFound(project)
	}
	scan, err := s.d.Scanner.Scan(ctx, p.Path, s.d.Limits)
	if err != nil {
		return corerepos.RescanReport{}, fmt.Errorf("scan %s: %w", p.Path, err)
	}
	return corerepos.Rescan(scan, ws, project), nil
}

func isActive(l corerepos.Life) bool { _, ok := l.(corerepos.Active); return ok }

func errProjectNotFound(id core.ProjectID) error {
	return apperr.New(apperr.NotFound, "This project isn't tracked (any more). Refresh the page.", fmt.Errorf("project %s", id))
}

// TrackRepos implements api.TrackingService.
func (s *TrackingService) TrackRepos(ctx context.Context, project core.ProjectID, picks []corerepos.Pick, actor corerepos.Actor) (corerepos.Outcome, error) {
	ws, err := s.load(ctx)
	if err != nil {
		return nil, err
	}
	p, ok := ws.Projects[project]
	if !ok || !isActive(p.Life) {
		return nil, errProjectNotFound(project)
	}
	ps, err := ParsePicks(p.Path, picks)
	if err != nil {
		return nil, err
	}
	ps, err = s.checkPicks(ctx, p.Path, ps)
	if err != nil {
		return nil, err
	}
	return s.decide(ctx, func(now time.Time) corerepos.Input {
		return corerepos.TrackRepos{ProjectID: project, Repos: s.withIDs(ps), Actor: actor, Now: now}
	})
}

// UntrackRepo implements api.TrackingService.
func (s *TrackingService) UntrackRepo(ctx context.Context, repo core.RepoID, actor corerepos.Actor) (corerepos.Outcome, error) {
	return s.decide(ctx, func(now time.Time) corerepos.Input {
		return corerepos.UntrackRepo{RepoID: repo, Actor: actor, Now: now}
	})
}

// UntrackProject implements api.TrackingService.
func (s *TrackingService) UntrackProject(ctx context.Context, project core.ProjectID, actor corerepos.Actor) (corerepos.Outcome, error) {
	return s.decide(ctx, func(now time.Time) corerepos.Input {
		return corerepos.UntrackProject{ProjectID: project, Actor: actor, Now: now}
	})
}

// RenameProject implements api.TrackingService.
func (s *TrackingService) RenameProject(ctx context.Context, project core.ProjectID, name string, actor corerepos.Actor) (corerepos.Outcome, error) {
	n, err := ParseName(name)
	if err != nil {
		return nil, err
	}
	return s.decide(ctx, func(now time.Time) corerepos.Input {
		return corerepos.RenameProject{ProjectID: project, Name: n, Actor: actor, Now: now}
	})
}

// decide is steps 2 to 4 of the shell: load the workspace, call Decide, write
// the rows and audit entries in one transaction, then run what follows the
// commit. A version conflict reloads and decides once more.
func (s *TrackingService) decide(ctx context.Context, mk func(now time.Time) corerepos.Input) (corerepos.Outcome, error) {
	var lastErr error
	for attempt := 0; attempt < 2; attempt++ {
		out, after, err := s.once(ctx, mk(s.d.Clock().UTC()))
		if err == nil {
			s.runAfter(ctx, after)
			return out, nil
		}
		if !errors.Is(err, db.ErrConflict) {
			return nil, err
		}
		lastErr = err
	}
	return nil, apperr.New(apperr.Unavailable, "Another change to your projects was in progress. Try again.", lastErr)
}

// once runs one transaction. It returns the commands to run after commit.
func (s *TrackingService) once(ctx context.Context, in corerepos.Input) (corerepos.Outcome, []corerepos.Command, error) {
	var out corerepos.Outcome
	var after []corerepos.Command
	err := s.d.Store.Commit(ctx, func(tx *db.Tx) error {
		after = nil
		w := tx.Workspace()
		ws, err := w.Load(ctx)
		if err != nil {
			return err
		}
		_, cmds, outcome := corerepos.Decide(ws, in)
		out = outcome
		now := inputTime(in)
		for _, c := range cmds {
			switch c := c.(type) {
			case corerepos.InsertProject:
				err = w.InsertProject(ctx, c)
			case corerepos.InsertRepo:
				err = w.InsertRepo(ctx, c)
			case corerepos.RestoreProject:
				err = w.RestoreProject(ctx, c)
			case corerepos.RestoreRepo:
				err = w.RestoreRepo(ctx, c)
			case corerepos.MarkRepoUntracked:
				err = w.MarkRepoUntracked(ctx, c)
			case corerepos.MarkProjectUntracked:
				err = w.MarkProjectUntracked(ctx, c)
			case corerepos.UpdateProject:
				err = w.UpdateProject(ctx, c)
			case corerepos.WriteAudit:
				err = s.appendAudit(ctx, tx, c, now)
			case corerepos.PublishReposChanged, corerepos.RefreshStatus:
				after = append(after, c)
			default:
				err = fmt.Errorf("unknown tracking command %T", c)
			}
			if err != nil {
				return err
			}
		}
		return nil
	})
	if err != nil {
		if errors.Is(err, db.ErrConflict) {
			return nil, nil, err
		}
		if errors.Is(err, db.ErrNotFound) {
			return nil, nil, apperr.New(apperr.NotFound, "A project or repo changed while you worked. Refresh the page.", err)
		}
		return nil, nil, fmt.Errorf("track: %w", err)
	}
	return out, after, nil
}

func (s *TrackingService) appendAudit(ctx context.Context, tx *db.Tx, c corerepos.WriteAudit, now time.Time) error {
	detail, err := json.Marshal(c.Detail)
	if err != nil {
		return fmt.Errorf("encode audit detail: %w", err)
	}
	e, err := audit.Prepare(core.AuditEntry{Actor: string(c.Actor), Action: c.Action, Subject: c.Subject, Detail: detail},
		s.d.Redactor, core.AuditID(s.d.IDs("aud")), now)
	if err != nil {
		return err
	}
	return tx.AppendAudit(ctx, e)
}

// runAfter performs what follows a commit. It is best effort: the UI
// refetches on reconnect and repo-status ticks catch up.
func (s *TrackingService) runAfter(ctx context.Context, cmds []corerepos.Command) {
	// The commit is done; a client that hung up must not skip the refresh and
	// the notification, so they get their own short deadline.
	ctx, cancel := context.WithTimeout(context.WithoutCancel(ctx), afterTimeout)
	defer cancel()
	if s.d.Status != nil && len(cmds) > 0 {
		if err := s.d.Status.ReloadRepos(ctx); err != nil {
			s.d.Log.Warn("reload repos after tracking failed", "err", err)
		}
	}
	for _, c := range cmds {
		r, ok := c.(corerepos.RefreshStatus)
		if !ok || s.d.Status == nil {
			continue
		}
		if err := s.d.Status.Refresh(ctx, r.RepoIDs, corerepos.ReasonTracking); err != nil {
			s.d.Log.Warn("refresh after tracking failed", "err", err)
		}
	}
	// Last, so a client that refetches on the message sees the new status set.
	for _, c := range cmds {
		if _, ok := c.(corerepos.PublishReposChanged); ok && s.d.Publisher != nil {
			s.d.Publisher.PublishReposChanged()
			break
		}
	}
}

// afterTimeout bounds what runs after a commit.
const afterTimeout = 10 * time.Second

// inputTime is the Now of an input, used for audit entries.
func inputTime(in corerepos.Input) time.Time {
	switch in := in.(type) {
	case corerepos.TrackProject:
		return in.Now
	case corerepos.TrackRepos:
		return in.Now
	case corerepos.UntrackRepo:
		return in.Now
	case corerepos.UntrackProject:
		return in.Now
	case corerepos.RenameProject:
		return in.Now
	}
	return time.Time{}
}
