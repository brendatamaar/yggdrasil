package main

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"os"
	"path/filepath"
	"time"

	"github.com/brendatamaar/yggdrasil/internal/audit"
	"github.com/brendatamaar/yggdrasil/internal/core"
	"github.com/brendatamaar/yggdrasil/internal/db"
	"github.com/brendatamaar/yggdrasil/internal/platform"
	"github.com/brendatamaar/yggdrasil/internal/safety"
)

// seed tracks a repo so a task can be started from the start page. It stands
// in for the project pages (WS-01, Phase 2) and prints the two IDs the start
// page asks for. It never changes the repo's files.
func seed(ctx context.Context, w io.Writer, cfg config, repoPath, projectName string) error {
	abs, err := filepath.Abs(repoPath)
	if err != nil {
		return fmt.Errorf("read the folder %q: %w", repoPath, err)
	}
	if err := platform.CheckPath(abs); err != nil {
		return err
	}
	if long, err := platform.LongPath(abs); err == nil {
		abs = long
	}
	// The folder is the one the person typed on their own command line; these
	// calls only look at it.
	if st, err := os.Stat(abs); err != nil || !st.IsDir() { //nolint:gosec // path chosen by the user at the CLI
		return fmt.Errorf("%s isn't a folder. Give the path of a git repo, such as `ygg seed C:\\work\\shop-api`", abs)
	}
	if _, err := os.Stat(filepath.Join(abs, ".git")); err != nil { //nolint:gosec // path chosen by the user at the CLI
		return fmt.Errorf("%s isn't a git repo (no .git inside). Give the path of the repo's top folder", abs)
	}
	if projectName == "" {
		projectName = filepath.Base(filepath.Dir(abs))
		if projectName == "." || projectName == string(filepath.Separator) || projectName == filepath.VolumeName(abs)+string(filepath.Separator) {
			projectName = "project"
		}
	}

	d, store, err := openStore(ctx, cfg, safety.NewRedactor())
	if err != nil {
		return err
	}
	defer d.Close()

	// Decide what is missing, then create it together with its audit entries
	// in one commit: tracking is all or nothing (invariant 8).
	project, projectFound, err := findProject(ctx, store, projectName)
	if err != nil {
		return err
	}
	var repo core.Repo
	repoFound := false
	if projectFound {
		if repo, repoFound, err = findRepo(ctx, store, project.ID, filepath.Base(abs), abs); err != nil {
			return err
		}
	}
	if !projectFound || !repoFound {
		redactor := safety.NewRedactor()
		now := time.Now().UTC()
		err = store.Commit(ctx, func(tx *db.Tx) error {
			if !projectFound {
				p, err := tx.CreateProject(ctx, core.Project{ID: core.ProjectID(db.NewID("prj")), Name: projectName, Path: filepath.Dir(abs), CreatedAt: now})
				if err != nil {
					return err
				}
				project = p
				if err := appendAudit(ctx, tx, redactor, "project.track", "project:"+string(p.ID), map[string]any{"name": p.Name}); err != nil {
					return err
				}
			}
			if !repoFound {
				r, err := tx.CreateRepo(ctx, core.Repo{ID: core.RepoID(db.NewID("rep")), ProjectID: project.ID, Name: filepath.Base(abs), Path: abs, CreatedAt: now})
				if err != nil {
					return err
				}
				repo = r
				return appendAudit(ctx, tx, redactor, "repo.track", "repo:"+string(r.ID), map[string]any{"name": r.Name})
			}
			return nil
		})
		if err != nil {
			return fmt.Errorf("track the repo: %w", err)
		}
	}
	fmt.Fprintf(w, "Project  %-20s %s\n", project.Name, project.ID)
	fmt.Fprintf(w, "Repo     %-20s %s\n", repo.Name, repo.ID)
	fmt.Fprintln(w)
	fmt.Fprintln(w, `Paste these IDs into the "Project ID" and "Repo ID" fields on the start page.`)
	return nil
}

// appendAudit prepares one audit entry of a user action (the ID and time are
// chosen here, in the shell) and adds it to the transaction.
func appendAudit(ctx context.Context, tx *db.Tx, r safety.Redactor, action, subject string, detail map[string]any) error {
	raw, err := json.Marshal(detail)
	if err != nil {
		return fmt.Errorf("encode audit detail: %w", err)
	}
	e, err := audit.Prepare(core.AuditEntry{Actor: "user", Action: action, Subject: subject, Detail: raw},
		r, core.AuditID(db.NewID("aud")), time.Now())
	if err != nil {
		return err
	}
	return tx.AppendAudit(ctx, e)
}

func findProject(ctx context.Context, store *db.Store, name string) (core.Project, bool, error) {
	all, err := store.ListProjects(ctx)
	if err != nil {
		return core.Project{}, false, err
	}
	for _, p := range all {
		if p.Name == name {
			return p, true, nil
		}
	}
	return core.Project{}, false, nil
}

func findRepo(ctx context.Context, store *db.Store, project core.ProjectID, name, path string) (core.Repo, bool, error) {
	all, err := store.ListRepos(ctx, project)
	if err != nil {
		return core.Repo{}, false, err
	}
	for _, r := range all {
		if r.Name == name {
			if r.Path != path {
				return core.Repo{}, false, errors.New("a repo called " + name + " is already tracked at another path: " + r.Path)
			}
			return r, true, nil
		}
	}
	return core.Repo{}, false, nil
}
