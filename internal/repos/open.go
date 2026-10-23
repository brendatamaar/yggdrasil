package repos

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"path/filepath"
	"time"

	"github.com/brendatamaar/yggdrasil/internal/apperr"
	"github.com/brendatamaar/yggdrasil/internal/audit"
	"github.com/brendatamaar/yggdrasil/internal/core"
	corerepos "github.com/brendatamaar/yggdrasil/internal/core/repos"
	"github.com/brendatamaar/yggdrasil/internal/db"
	"github.com/brendatamaar/yggdrasil/internal/platform"
	"github.com/brendatamaar/yggdrasil/internal/safety"
)

// Open in editor and show in folder (WS-08), and the editor setting. They are
// stateless: no step, no batch. The editor and the file explorer start outside
// Yggdrasil's job object, so they outlive it (PL-05).

// EditorSettingKey is the settings key of the chosen editor.
const EditorSettingKey = "editor"

// Opener starts editors and file explorers. platform.OpenEditor and
// platform.OpenFolder implement it (see PlatformOpener).
type Opener interface {
	OpenEditor(ctx context.Context, editor, path string, env []string) error
	OpenFolder(ctx context.Context, path string, env []string) error
}

// PlatformOpener is the live Opener.
type PlatformOpener struct{}

// OpenEditor implements Opener.
func (PlatformOpener) OpenEditor(ctx context.Context, editor, path string, env []string) error {
	return platform.OpenEditor(ctx, editor, path, env)
}

// OpenFolder implements Opener.
func (PlatformOpener) OpenFolder(ctx context.Context, path string, env []string) error {
	return platform.OpenFolder(ctx, path, env)
}

// OpenDeps are what NewOpenService needs. Store, Redactor and Env are
// required.
type OpenDeps struct {
	Store    *db.Store
	Redactor safety.Redactor
	Env      safety.EnvBuilder
	Opener   Opener
	// Exists says whether a program is on the PATH of env. Default: platform.Resolve.
	Exists func(program string, env []string) bool
	Clock  func() time.Time
	IDs    func(prefix string) string
}

// OpenService opens repos in an editor or the file explorer and keeps the
// editor setting. It implements part of api.GitActionsService.
//
// Wiring (cmd/ygg): see GitActions.
type OpenService struct{ d OpenDeps }

// GitActions is the batch service and the open service together. It
// implements api.GitActionsService.
//
// Wiring (cmd/ygg):
//
//	act, _ := repos.NewActionsService(repos.ActionsDeps{Store, Redactor, Git: g, Builds: buildSvc, Status: statusSvc, Publisher: apiServer})
//	open, _ := repos.NewOpenService(repos.OpenDeps{Store, Redactor, Env: envBuilder})
//	api.Deps.GitActions = repos.GitActions{ActionsService: act, OpenService: open}
//	errgroup: act.Run(ctx)
type GitActions struct {
	*ActionsService
	*OpenService
}

// NewOpenService returns the service.
func NewOpenService(d OpenDeps) (*OpenService, error) {
	switch {
	case d.Store == nil:
		return nil, errors.New("repos: open needs a store")
	case d.Redactor == nil:
		return nil, errors.New("repos: open needs a redactor (audit entries must be redacted)")
	case d.Env == nil:
		return nil, errors.New("repos: open needs an environment builder")
	}
	if d.Opener == nil {
		d.Opener = PlatformOpener{}
	}
	if d.Exists == nil {
		d.Exists = func(p string, env []string) bool { _, err := platform.Resolve(p, "", env); return err == nil }
	}
	if d.Clock == nil {
		d.Clock = func() time.Time { return time.Now().UTC() }
	}
	if d.IDs == nil {
		d.IDs = db.NewID
	}
	return &OpenService{d: d}, nil
}

func (s *OpenService) repoPath(ctx context.Context, id core.RepoID, file RelPath) (repo, target string, err error) {
	r, err := s.d.Store.GetRepo(ctx, id)
	if errors.Is(err, db.ErrNotFound) {
		return "", "", apperr.New(apperr.NotFound, "This repo isn't tracked (any more). Refresh the page.", err)
	}
	if err != nil {
		return "", "", fmt.Errorf("find repo %s: %w", id, err)
	}
	target = r.Path
	if file != "" {
		target = filepath.Join(r.Path, filepath.FromSlash(file.String()))
	}
	return r.Path, target, nil
}

func (s *OpenService) env() ([]string, error) {
	env, err := s.d.Env.Build(safety.EnvOptions{})
	if err != nil {
		return nil, fmt.Errorf("build environment: %w", err)
	}
	return env, nil
}

// EditorSetting is the chosen editor and what was found on this machine.
type EditorSetting struct {
	// Chosen is the saved editor, "" when none was chosen.
	Chosen Editor
	// Detected lists the supported editors found on the PATH.
	Detected []Editor
}

func (s *OpenService) detect(env []string) []Editor {
	found := []Editor{}
	for _, e := range Editors {
		if s.d.Exists(e.Program(), env) {
			found = append(found, e)
		}
	}
	return found
}

func (s *OpenService) chosen(ctx context.Context) (Editor, int64, error) {
	set, err := s.d.Store.GetSetting(ctx, EditorSettingKey)
	if errors.Is(err, db.ErrNotFound) {
		return "", db.SettingNew, nil
	}
	if err != nil {
		return "", 0, fmt.Errorf("read editor setting: %w", err)
	}
	e, perr := ParseEditor(set.Value)
	if perr != nil {
		e = "" // an old or unknown value counts as not chosen
	}
	return e, set.Version, nil
}

// Editor returns the editor setting.
func (s *OpenService) Editor(ctx context.Context) (EditorSetting, error) {
	e, _, err := s.chosen(ctx)
	if err != nil {
		return EditorSetting{}, err
	}
	env, err := s.env()
	if err != nil {
		return EditorSetting{}, err
	}
	return EditorSetting{Chosen: e, Detected: s.detect(env)}, nil
}

// SetEditor saves the editor ("" clears the choice). The change and its audit
// entry are committed together.
func (s *OpenService) SetEditor(ctx context.Context, e Editor, actor corerepos.Actor) error {
	if _, err := ParseEditor(string(e)); err != nil {
		return err
	}
	for attempt := 0; attempt < 3; attempt++ {
		old, version, err := s.chosen(ctx)
		if err != nil {
			return err
		}
		err = s.d.Store.Commit(ctx, func(tx *db.Tx) error {
			if _, err := tx.SetSetting(ctx, EditorSettingKey, string(e), version); err != nil {
				return err
			}
			d, _ := json.Marshal(map[string]string{"from": string(old), "to": string(e)})
			entry, err := audit.Prepare(core.AuditEntry{Actor: string(actor), Action: "settings.editor",
				Subject: "setting:" + EditorSettingKey, Detail: d}, s.d.Redactor, core.AuditID(s.d.IDs("aud")), s.d.Clock().UTC())
			if err != nil {
				return err
			}
			return tx.AppendAudit(ctx, entry)
		})
		if errors.Is(err, db.ErrConflict) {
			continue // changed meanwhile: read the version again
		}
		if err != nil {
			return fmt.Errorf("save editor: %w", err)
		}
		return nil
	}
	return apperr.New(apperr.Conflict, "The editor setting changed while you saved it. Try again.", db.ErrConflict)
}

// Open opens the repo, or a file of it, in an editor: the requested one, the
// saved one, or the first one found on the PATH.
//
// Open and Reveal write no audit entry on purpose: they change no state of
// Yggdrasil, a repo or a remote (decision, F1-G2).
func (s *OpenService) Open(ctx context.Context, id core.RepoID, req OpenRequest) error {
	_, target, err := s.repoPath(ctx, id, req.File)
	if err != nil {
		return err
	}
	env, err := s.env()
	if err != nil {
		return err
	}
	e := req.Editor
	if e == "" {
		if e, _, err = s.chosen(ctx); err != nil {
			return err
		}
	}
	if e == "" {
		if found := s.detect(env); len(found) > 0 {
			e = found[0]
		}
	}
	if e == "" {
		return apperr.New(apperr.Unavailable,
			"Yggdrasil found no supported editor (VS Code, Cursor or IntelliJ IDEA) on this computer. Install one, or pick one in the settings.",
			errors.New("no editor"))
	}
	if err := s.d.Opener.OpenEditor(ctx, e.Program(), target, env); err != nil {
		return apperr.New(apperr.Unavailable,
			"Yggdrasil could not start the editor. Check that it is installed and on your PATH.", err)
	}
	return nil
}

// Reveal shows the repo folder, or the folder of a file, in the file explorer.
func (s *OpenService) Reveal(ctx context.Context, id core.RepoID, file RelPath) error {
	_, target, err := s.repoPath(ctx, id, file)
	if err != nil {
		return err
	}
	if file != "" {
		target = filepath.Dir(target)
	}
	env, err := s.env()
	if err != nil {
		return err
	}
	if err := s.d.Opener.OpenFolder(ctx, target, env); err != nil {
		return apperr.New(apperr.Unavailable, "Yggdrasil could not open the folder.", err)
	}
	return nil
}
