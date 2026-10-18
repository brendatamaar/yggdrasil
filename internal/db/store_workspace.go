package db

import (
	"context"
	"database/sql"
	"fmt"
	"path/filepath"

	"github.com/brendatamaar/yggdrasil/internal/core"
	corerepos "github.com/brendatamaar/yggdrasil/internal/core/repos"
	"github.com/brendatamaar/yggdrasil/internal/db/gen"
)

// The workspace store: the persistence of the repo-tracking step
// (docs/design/repo-tracking.md). LoadWorkspace reads every project and repo
// row, tracked or not; the write methods store the row commands Decide
// returns. All writes happen inside Store.Commit through Tx.Workspace, in the
// same transaction as their audit entries.
//
// Active{Since} is stored as created_at, and a restore keeps the original
// created_at: the schema has no column for "tracked again at".

// workspaceFromRows turns database rows into the tracking aggregate. It
// rejects what the schema cannot rule out: a path or relative path of the
// wrong shape, a repo of an unknown project, two repos with one relative
// path or name, and an Active repo in an Untracked project.
func workspaceFromRows(projects []gen.Project, repos []gen.Repo, busy []string) (corerepos.Workspace, error) {
	ws := corerepos.Workspace{Projects: make(map[core.ProjectID]corerepos.Project, len(projects))}
	for _, r := range projects {
		p, err := projectRow(r)
		if err != nil {
			return corerepos.Workspace{}, err
		}
		if _, dup := ws.Projects[p.ID]; dup {
			return corerepos.Workspace{}, fmt.Errorf("project %s appears twice", p.ID)
		}
		ws.Projects[p.ID] = p
	}
	for _, r := range repos {
		pid := core.ProjectID(r.ProjectID)
		p, ok := ws.Projects[pid]
		if !ok {
			return corerepos.Workspace{}, fmt.Errorf("repo %s belongs to the unknown project %s", r.ID, r.ProjectID)
		}
		repo, err := repoRow(r)
		if err != nil {
			return corerepos.Workspace{}, err
		}
		if _, dup := p.Repos[repo.ID]; dup {
			return corerepos.Workspace{}, fmt.Errorf("repo %s appears twice", repo.ID)
		}
		for _, o := range p.Repos {
			if o.RelPath == repo.RelPath {
				return corerepos.Workspace{}, fmt.Errorf("repos %s and %s of project %s share the folder %q", o.ID, repo.ID, p.ID, repo.RelPath)
			}
			if o.Name == repo.Name {
				return corerepos.Workspace{}, fmt.Errorf("repos %s and %s of project %s share the name %q", o.ID, repo.ID, p.ID, repo.Name)
			}
		}
		if _, active := repo.Life.(corerepos.Active); active {
			if _, untracked := p.Life.(corerepos.Untracked); untracked {
				return corerepos.Workspace{}, fmt.Errorf("repo %s is tracked but its project %s is not", repo.ID, p.ID)
			}
		}
		p.Repos[repo.ID] = repo
	}
	if len(busy) > 0 {
		ws.Busy = make(map[core.RepoID]corerepos.BusyReason, len(busy))
		for _, id := range busy {
			ws.Busy[core.RepoID(id)] = corerepos.BusyBuildRunning
		}
	}
	return ws, nil
}

func projectRow(r gen.Project) (corerepos.Project, error) {
	path, err := corerepos.NewCleanPath(r.Path)
	if err != nil {
		return corerepos.Project{}, fmt.Errorf("project %s has an unusable folder: %w", r.ID, err)
	}
	if err := corerepos.ValidName(r.Name); err != nil {
		return corerepos.Project{}, fmt.Errorf("project %s: %w", r.ID, err)
	}
	life, err := lifeOf(r.CreatedAt, r.UntrackedAt)
	if err != nil {
		return corerepos.Project{}, fmt.Errorf("project %s: %w", r.ID, err)
	}
	return corerepos.Project{
		ID: core.ProjectID(r.ID), Name: r.Name, Path: path, Life: life,
		Repos: map[core.RepoID]corerepos.Repo{}, Version: r.Version,
	}, nil
}

func repoRow(r gen.Repo) (corerepos.Repo, error) {
	rel, err := corerepos.NewRelPath(r.RelPath)
	if err != nil {
		return corerepos.Repo{}, fmt.Errorf("repo %s has an unusable folder: %w", r.ID, err)
	}
	if err := corerepos.ValidName(r.Name); err != nil {
		return corerepos.Repo{}, fmt.Errorf("repo %s: %w", r.ID, err)
	}
	life, err := lifeOf(r.CreatedAt, r.UntrackedAt)
	if err != nil {
		return corerepos.Repo{}, fmt.Errorf("repo %s: %w", r.ID, err)
	}
	return corerepos.Repo{ID: core.RepoID(r.ID), Name: r.Name, RelPath: rel, Life: life, Trusted: r.Trusted != 0}, nil
}

func lifeOf(created string, untracked sql.NullString) (corerepos.Life, error) {
	since, err := parseTime(created)
	if err != nil {
		return nil, err
	}
	at, err := parseNullTime(untracked)
	if err != nil {
		return nil, err
	}
	if at != nil {
		return corerepos.Untracked{At: *at}, nil
	}
	return corerepos.Active{Since: since}, nil
}

func loadWorkspace(ctx context.Context, q *gen.Queries) (corerepos.Workspace, error) {
	projects, err := q.ListAllProjects(ctx)
	if err != nil {
		return corerepos.Workspace{}, fmt.Errorf("list projects: %w", err)
	}
	repos, err := q.ListAllRepos(ctx)
	if err != nil {
		return corerepos.Workspace{}, fmt.Errorf("list repos: %w", err)
	}
	busy, err := q.ListBusyBuildRepos(ctx)
	if err != nil {
		return corerepos.Workspace{}, fmt.Errorf("list repos with builds: %w", err)
	}
	ws, err := workspaceFromRows(projects, repos, busy)
	if err != nil {
		return corerepos.Workspace{}, fmt.Errorf("read the tracked projects (the database looks inconsistent): %w", err)
	}
	return ws, nil
}

// LoadWorkspace reads every project and repo row, tracked or not, and the
// repos with a queued or running build of their main checkout (Busy). It is
// for previews; a decision loads through Tx.Workspace in its own transaction.
func (s *Store) LoadWorkspace(ctx context.Context) (corerepos.Workspace, error) {
	return loadWorkspace(ctx, s.db.reader())
}

// WorkspaceTx is the workspace store inside one Commit. It remembers which
// projects it already version-checked, so the many row commands of one
// decision check and bump a project's version once.
type WorkspaceTx struct {
	q       *gen.Queries
	touched map[core.ProjectID]int64
}

// Workspace returns the workspace store of this transaction.
func (tx *Tx) Workspace() *WorkspaceTx {
	return &WorkspaceTx{q: tx.q, touched: map[core.ProjectID]int64{}}
}

// Load reads the workspace inside the transaction, so a decision sees what
// BEGIN IMMEDIATE has locked.
func (w *WorkspaceTx) Load(ctx context.Context) (corerepos.Workspace, error) {
	return loadWorkspace(ctx, w.q)
}

// claim checks that the project still has the version the decision read and
// bumps it, once per transaction. A stale version is ErrConflict; the owner
// reloads and decides again.
func (w *WorkspaceTx) claim(ctx context.Context, id core.ProjectID, expect int64) error {
	if v, ok := w.touched[id]; ok && v == expect {
		return nil
	}
	n, err := w.q.BumpProjectVersion(ctx, gen.BumpProjectVersionParams{ID: string(id), Version: expect})
	if err != nil {
		return fmt.Errorf("update project %s: %w", id, err)
	}
	if n == 0 {
		c, err := w.q.CountProject(ctx, string(id))
		if err != nil {
			return fmt.Errorf("update project %s: %w", id, err)
		}
		if c == 0 {
			return fmt.Errorf("%w: project %s", ErrNotFound, id)
		}
		return fmt.Errorf("%w: project %s changed since it was read", ErrConflict, id)
	}
	w.touched[id] = expect
	return nil
}

// InsertProject stores a new Active project at version 0.
func (w *WorkspaceTx) InsertProject(ctx context.Context, c corerepos.InsertProject) error {
	p := c.Project
	a, ok := p.Life.(corerepos.Active)
	if !ok || a.Since.IsZero() {
		return fmt.Errorf("insert project %s: a new project is Active and needs a time from the caller", p.ID)
	}
	if err := w.q.CreateProject(ctx, gen.CreateProjectParams{
		ID: string(p.ID), Name: p.Name, Path: string(p.Path), CreatedAt: fmtTime(a.Since),
	}); err != nil {
		return fmt.Errorf("insert project %s: %w", p.ID, err)
	}
	w.touched[p.ID] = 0
	return nil
}

// InsertRepo stores a new Active repo. Its absolute path is the project's
// folder plus RelPath.
func (w *WorkspaceTx) InsertRepo(ctx context.Context, c corerepos.InsertRepo) error {
	if err := w.claim(ctx, c.ProjectID, c.ExpectVersion); err != nil {
		return err
	}
	a, ok := c.Repo.Life.(corerepos.Active)
	if !ok || a.Since.IsZero() {
		return fmt.Errorf("insert repo %s: a new repo is Active and needs a time from the caller", c.Repo.ID)
	}
	proj, err := w.q.GetProjectAny(ctx, string(c.ProjectID))
	if err != nil {
		return found("insert repo: project "+string(c.ProjectID), err)
	}
	path := proj.Path
	if c.Repo.RelPath != "." {
		path = filepath.Join(proj.Path, filepath.FromSlash(string(c.Repo.RelPath)))
	}
	if err := w.q.CreateRepo(ctx, gen.CreateRepoParams{
		ID: string(c.Repo.ID), ProjectID: string(c.ProjectID), Name: c.Repo.Name, Path: path,
		RelPath: string(c.Repo.RelPath), Trusted: b2i(c.Repo.Trusted), CreatedAt: fmtTime(a.Since),
	}); err != nil {
		return fmt.Errorf("insert repo %s: %w", c.Repo.ID, err)
	}
	return nil
}

// RestoreProject makes an Untracked project Active again, with a new name.
func (w *WorkspaceTx) RestoreProject(ctx context.Context, c corerepos.RestoreProject) error {
	if err := w.claim(ctx, c.ProjectID, c.ExpectVersion); err != nil {
		return err
	}
	if err := w.q.ClearProjectUntracked(ctx, gen.ClearProjectUntrackedParams{Name: c.Name, ID: string(c.ProjectID)}); err != nil {
		return fmt.Errorf("restore project %s: %w", c.ProjectID, err)
	}
	return nil
}

// RestoreRepo makes an Untracked repo Active again; it is never trusted.
func (w *WorkspaceTx) RestoreRepo(ctx context.Context, c corerepos.RestoreRepo) error {
	if err := w.claim(ctx, c.ProjectID, c.ExpectVersion); err != nil {
		return err
	}
	n, err := w.q.ClearRepoUntracked(ctx, gen.ClearRepoUntrackedParams{ID: string(c.RepoID), ProjectID: string(c.ProjectID)})
	return updated("restore repo "+string(c.RepoID), n, err)
}

// MarkRepoUntracked hides a repo. Its row and builds stay.
func (w *WorkspaceTx) MarkRepoUntracked(ctx context.Context, c corerepos.MarkRepoUntracked) error {
	if err := w.claim(ctx, c.ProjectID, c.ExpectVersion); err != nil {
		return err
	}
	n, err := w.q.SetRepoUntracked(ctx, gen.SetRepoUntrackedParams{
		UntrackedAt: fmtNullTime(&c.At), ID: string(c.RepoID), ProjectID: string(c.ProjectID),
	})
	return updated("untrack repo "+string(c.RepoID), n, err)
}

// MarkProjectUntracked hides a project. Its row and builds stay.
func (w *WorkspaceTx) MarkProjectUntracked(ctx context.Context, c corerepos.MarkProjectUntracked) error {
	if err := w.claim(ctx, c.ProjectID, c.ExpectVersion); err != nil {
		return err
	}
	if err := w.q.SetProjectUntracked(ctx, gen.SetProjectUntrackedParams{UntrackedAt: fmtNullTime(&c.At), ID: string(c.ProjectID)}); err != nil {
		return fmt.Errorf("untrack project %s: %w", c.ProjectID, err)
	}
	return nil
}

// UpdateProject renames a project.
func (w *WorkspaceTx) UpdateProject(ctx context.Context, c corerepos.UpdateProject) error {
	if err := w.claim(ctx, c.ProjectID, c.ExpectVersion); err != nil {
		return err
	}
	if err := w.q.SetProjectName(ctx, gen.SetProjectNameParams{Name: c.Name, ID: string(c.ProjectID)}); err != nil {
		return fmt.Errorf("rename project %s: %w", c.ProjectID, err)
	}
	return nil
}
