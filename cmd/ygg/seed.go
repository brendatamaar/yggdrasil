package main

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"os"
	"path/filepath"

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

	d, store, aud, err := openStore(ctx, cfg, safety.NewRedactor())
	if err != nil {
		return err
	}
	defer d.Close()

	project, created, err := findOrCreateProject(ctx, store, projectName, filepath.Dir(abs))
	if err != nil {
		return err
	}
	if created {
		if err := aud.Write(ctx, core.AuditEntry{Actor: "user", Action: "project.track", Subject: "project:" + string(project.ID), Detail: detail(map[string]any{"name": project.Name})}); err != nil {
			return err
		}
	}
	repo, created, err := findOrCreateRepo(ctx, store, project.ID, filepath.Base(abs), abs)
	if err != nil {
		return err
	}
	if created {
		if err := aud.Write(ctx, core.AuditEntry{Actor: "user", Action: "repo.track", Subject: "repo:" + string(repo.ID), Detail: detail(map[string]any{"name": repo.Name})}); err != nil {
			return err
		}
	}
	fmt.Fprintf(w, "Project  %-20s %s\n", project.Name, project.ID)
	fmt.Fprintf(w, "Repo     %-20s %s\n", repo.Name, repo.ID)
	fmt.Fprintln(w)
	fmt.Fprintln(w, `Paste these IDs into the "Project ID" and "Repo ID" fields on the start page.`)
	return nil
}

func detail(v map[string]any) json.RawMessage {
	b, err := json.Marshal(v)
	if err != nil {
		return json.RawMessage("{}")
	}
	return b
}

func findOrCreateProject(ctx context.Context, store *db.Store, name, path string) (core.Project, bool, error) {
	all, err := store.ListProjects(ctx)
	if err != nil {
		return core.Project{}, false, err
	}
	for _, p := range all {
		if p.Name == name {
			return p, false, nil
		}
	}
	p, err := store.CreateProject(ctx, core.Project{Name: name, Path: path})
	return p, err == nil, err
}

func findOrCreateRepo(ctx context.Context, store *db.Store, project core.ProjectID, name, path string) (core.Repo, bool, error) {
	all, err := store.ListRepos(ctx, project)
	if err != nil {
		return core.Repo{}, false, err
	}
	for _, r := range all {
		if r.Name == name {
			if r.Path != path {
				return core.Repo{}, false, errors.New("a repo called " + name + " is already tracked at another path: " + r.Path)
			}
			return r, false, nil
		}
	}
	r, err := store.CreateRepo(ctx, core.Repo{ProjectID: project, Name: name, Path: path})
	return r, err == nil, err
}
