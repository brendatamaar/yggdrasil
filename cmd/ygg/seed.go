package main

import (
	"context"
	"fmt"
	"io"
	"path/filepath"
	"sort"

	"github.com/brendatamaar/yggdrasil/internal/apperr"
	"github.com/brendatamaar/yggdrasil/internal/core"
	corerepos "github.com/brendatamaar/yggdrasil/internal/core/repos"
	"github.com/brendatamaar/yggdrasil/internal/repos"
	"github.com/brendatamaar/yggdrasil/internal/safety"
)

// seed tracks the folder as a project with every git repo found under it, the
// same way the folder browser does (WS-01): ParseFolder, Scan, then
// TrackProject through the tracking service, so the decision, the rows and
// the audit entries are the same. It prints the project and repo IDs the
// start page asks for. It never changes a file under the folder.
func seed(ctx context.Context, w io.Writer, cfg config, folder, projectName string) error {
	abs, err := filepath.Abs(folder)
	if err != nil {
		return fmt.Errorf("read the folder %q: %w", folder, err)
	}
	path, err := repos.ParseFolder(abs)
	if err != nil {
		return err
	}
	scan, err := repos.FSScanner{}.Scan(ctx, path, repos.DefaultLimits)
	if err != nil {
		return fmt.Errorf("look for git repos in %s: %w", path, err)
	}
	if len(scan.Repos) == 0 {
		return fmt.Errorf("no git repos found in %s (looked for a .git folder up to %d levels down). Give the path of a repo, or of a folder that holds repos", path, repos.DefaultLimits.MaxDepth)
	}
	if projectName == "" {
		projectName = corerepos.DefaultRepoName(path, ".")
	}
	picks := make([]corerepos.Pick, 0, len(scan.Repos))
	for _, rel := range scan.Repos {
		picks = append(picks, corerepos.Pick{RelPath: rel, Name: corerepos.DefaultRepoName(path, rel)})
	}

	redactor := safety.NewRedactor()
	d, store, err := openStore(ctx, cfg, redactor)
	if err != nil {
		return err
	}
	defer d.Close()
	svc, err := repos.NewTrackingService(repos.Deps{Store: store, Redactor: redactor})
	if err != nil {
		return err
	}
	out, err := svc.Track(ctx, string(path), projectName, picks, "user")
	if err != nil {
		if msg, ok := apperr.MessageOf(err); ok {
			return fmt.Errorf("track the folder: %s", msg)
		}
		return fmt.Errorf("track the folder: %w", err)
	}
	var tracked core.ProjectID
	switch o := out.(type) {
	case corerepos.Tracked:
		tracked = o.ProjectID
	case corerepos.AlreadyTracked:
		tracked = o.ProjectID
		// tracked or already tracked: read it back below
	case corerepos.NameTaken:
		return fmt.Errorf("a project called %q is already tracked at another folder. Give this one another name: ygg seed %s <name>", o.Name, path)
	case corerepos.Overlaps:
		return fmt.Errorf("%s is inside, or around, a folder that is already tracked. Track a folder that doesn't overlap", path)
	default:
		return fmt.Errorf("track the folder: unexpected result %T", out)
	}

	projects, err := svc.Projects(ctx)
	if err != nil {
		return err
	}
	for _, p := range projects {
		if p.ID != tracked {
			continue
		}
		fmt.Fprintf(w, "Project  %-20s %s\n", p.Name, p.ID)
		rs := make([]corerepos.Repo, 0, len(p.Repos))
		for _, r := range p.Repos {
			rs = append(rs, r)
		}
		sort.Slice(rs, func(i, j int) bool { return rs[i].RelPath < rs[j].RelPath })
		for _, r := range rs {
			fmt.Fprintf(w, "Repo     %-20s %s\n", r.Name, r.ID)
		}
		fmt.Fprintln(w)
		fmt.Fprintln(w, `Paste these IDs into the "Project ID" and "Repo ID" fields on the start page.`)
		return nil
	}
	return fmt.Errorf("the project at %s was tracked but can't be read back", path)
}
