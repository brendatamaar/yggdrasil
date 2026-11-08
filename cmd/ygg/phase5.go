package main

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io/fs"
	"log/slog"
	"maps"
	"os"
	"path/filepath"
	"slices"
	"strings"
	"sync"
	"sync/atomic"
	"time"

	"github.com/brendatamaar/yggdrasil/internal/api"
	"github.com/brendatamaar/yggdrasil/internal/apperr"
	"github.com/brendatamaar/yggdrasil/internal/audit"
	"github.com/brendatamaar/yggdrasil/internal/builds"
	"github.com/brendatamaar/yggdrasil/internal/builds/toolchains"
	"github.com/brendatamaar/yggdrasil/internal/core"
	ct "github.com/brendatamaar/yggdrasil/internal/core/terminal"
	wenv "github.com/brendatamaar/yggdrasil/internal/core/worktreeenv"
	"github.com/brendatamaar/yggdrasil/internal/db"
	"github.com/brendatamaar/yggdrasil/internal/devservers"
	"github.com/brendatamaar/yggdrasil/internal/platform"
	"github.com/brendatamaar/yggdrasil/internal/safety"
	"github.com/brendatamaar/yggdrasil/internal/tasks"
	"github.com/brendatamaar/yggdrasil/internal/terminal"
	"github.com/brendatamaar/yggdrasil/internal/worktreeenv"
)

// Wiring of the Phase 5 services (DV-01 to DV-05, WE-01, WE-02): dev servers,
// the terminal and the worktree environment. Each service is built over the
// store and the platform package; the adapters here only translate between
// the services and the API interfaces, and decide nothing.

// phase5Hooks lets tests swap the process and port functions of the platform.
// Production leaves it nil.
type phase5Hooks struct {
	start platform.StartFunc
	ports func(pid int) ([]int, error)
	pty   terminal.PTYStarter
	// resolve replaces platform.Resolve (no real npm on the test machine).
	resolve func(name, dir string, env []string) (string, error)
}

// errNotTracked says a repo or worktree is not (or no longer) tracked.
var errNotTracked = errors.New("not a tracked repo or worktree")

// trackedDirs resolves IDs to folders of tracked repos and of their active
// worktrees (invariant 6, DV-05). A path is never taken from a request.
// Folders that platform.CheckPath refuses (\\wsl$ and device paths) are
// refused here too.
type trackedDirs struct{ store *db.Store }

// RepoDir is the main checkout of a tracked repo.
func (d trackedDirs) RepoDir(ctx context.Context, repo core.RepoID) (string, error) {
	r, err := d.store.GetRepo(ctx, repo)
	if errors.Is(err, db.ErrNotFound) {
		return "", errNotTracked
	}
	if err != nil {
		return "", fmt.Errorf("read repo %s: %w", repo, err)
	}
	if err := platform.CheckPath(r.Path); err != nil {
		return "", fmt.Errorf("repo %s: %w", repo, &unsupportedPathError{err})
	}
	return r.Path, nil
}

// WorktreeDir is the folder of an active worktree of the repo.
func (d trackedDirs) WorktreeDir(ctx context.Context, repo core.RepoID, wt core.WorktreeID) (string, error) {
	if _, err := d.RepoDir(ctx, repo); err != nil {
		return "", err
	}
	w, err := d.store.GetWorktree(ctx, wt)
	if errors.Is(err, db.ErrNotFound) {
		return "", errNotTracked
	}
	if err != nil {
		return "", fmt.Errorf("read worktree %s: %w", wt, err)
	}
	if w.RepoID != repo || w.Status != core.WorktreeActive {
		return "", errNotTracked
	}
	if err := platform.CheckPath(w.Path); err != nil {
		return "", fmt.Errorf("worktree %s: %w", wt, &unsupportedPathError{err})
	}
	return w.Path, nil
}

// Dir is the folder of a repo (empty worktree) or one of its worktrees.
func (d trackedDirs) Dir(ctx context.Context, repo core.RepoID, wt core.WorktreeID) (string, error) {
	if wt == "" {
		return d.RepoDir(ctx, repo)
	}
	return d.WorktreeDir(ctx, repo, wt)
}

// RunDir is Dir for a folder a program is about to start in (a dev server or a
// terminal): the leaf must not be a symlink or junction, which could point
// outside the tracked folders (invariant 6).
func (d trackedDirs) RunDir(ctx context.Context, repo core.RepoID, wt core.WorktreeID) (string, error) {
	dir, err := d.Dir(ctx, repo, wt)
	if err != nil {
		return "", err
	}
	link, err := platform.IsReparsePoint(dir)
	switch {
	case errors.Is(err, fs.ErrNotExist):
		return "", errNotTracked
	case err != nil:
		return "", fmt.Errorf("check the folder of %s: %w", repo, err)
	case link:
		return "", &unsupportedPathError{plainError("This folder is a link (a symlink or junction), so Yggdrasil will not start a program in it. Track the real folder instead.")}
	}
	return dir, nil
}

// plainError is an error whose text is written for the user.
type plainError string

func (e plainError) Error() string { return string(e) }

// unsupportedPathError wraps a platform.CheckPath refusal, whose text is
// written for the user.
type unsupportedPathError struct{ err error }

func (e *unsupportedPathError) Error() string    { return e.err.Error() }
func (e *unsupportedPathError) Unwrap() error    { return e.err }
func (e *unsupportedPathError) UserText() string { return e.err.Error() }

// ---- dev servers ----------------------------------------------------------

// devRepos implements devservers.Repos.
type devRepos struct{ dirs trackedDirs }

func (r devRepos) Dir(ctx context.Context, t devservers.Target) (string, error) {
	dir, err := r.dirs.RunDir(ctx, t.Repo, t.Worktree)
	return dir, devRefusal(err)
}

// devRefusal turns the resolver's errors into classified ones.
func devRefusal(err error) error {
	var up *unsupportedPathError
	switch {
	case err == nil:
		return nil
	case errors.Is(err, errNotTracked):
		return apperr.New(apperr.NotFound, "That repo or worktree is not tracked any more, so no dev server can start there.", err)
	case errors.As(err, &up):
		return apperr.New(apperr.Invalid, up.UserText(), err)
	}
	return err
}

// devTemplates implements devservers.Templates from the toolchains' dev-server
// steps. The first step offered is "dev"; further ones are named after the
// step. (No per-repo dev-server override exists in the build settings yet.)
type devTemplates struct{ dirs trackedDirs }

func (t devTemplates) DevServer(ctx context.Context, target devservers.Target, command string) ([]string, map[string]string, error) {
	dir, err := t.dirs.Dir(ctx, target.Repo, target.Worktree)
	if err != nil {
		return nil, nil, devRefusal(err)
	}
	step, ok := devSteps(dir)[command]
	if !ok {
		return nil, nil, apperr.New(apperr.Invalid, "This repo has no dev server command by that name. Choose one of the commands offered for it.", nil)
	}
	envMap := map[string]string{}
	for _, kv := range step.Env {
		k, v, _ := strings.Cut(kv, "=")
		envMap[k] = v
	}
	return slices.Clone(step.Argv), envMap, nil
}

// devSteps lists the dev-server steps the detected toolchains of dir offer,
// by command key.
func devSteps(dir string) map[string]builds.Step {
	out := map[string]builds.Step{}
	for _, m := range toolchains.DetectAll(os.DirFS(dir)) {
		for _, s := range m.Toolchain.Commands(m.Detection).DevServer {
			key := "dev"
			if len(out) > 0 {
				key = commandKey(s.Name)
			}
			base := key
			for n := 2; ; n++ {
				if _, taken := out[key]; !taken {
					break
				}
				key = fmt.Sprintf("%s-%d", base, n)
			}
			out[key] = s
		}
	}
	return out
}

// commandKey makes a command key from a step name: lower-case letters,
// digits and '-'.
func commandKey(name string) string {
	var b strings.Builder
	for _, r := range strings.ToLower(name) {
		switch {
		case r >= 'a' && r <= 'z', r >= '0' && r <= '9':
			b.WriteRune(r)
		case b.Len() > 0 && !strings.HasSuffix(b.String(), "-"):
			b.WriteByte('-')
		}
	}
	k := strings.Trim(b.String(), "-")
	if k == "" || len(k) > 48 {
		return "dev-server"
	}
	return k
}

// devBlocks implements devservers.BlockSource: a worktree gets its port block
// (allocated on first use); a main checkout gets none, so nothing is injected.
type devBlocks struct{ env *worktreeenv.Service }

func (b devBlocks) BlockFor(ctx context.Context, t devservers.Target) (core.PortBlock, error) {
	if t.Worktree == "" {
		return core.PortBlock{}, nil
	}
	blk, err := b.env.BlockFor(ctx, t.Worktree)
	if err != nil {
		return core.PortBlock{}, fmt.Errorf("port block of %s: %w", t.Worktree, err)
	}
	return blk, nil
}

// devServerAPI implements api.DevServerService.
type devServerAPI struct {
	*devservers.Service
	store *db.Store
}

var _ api.DevServerService = devServerAPI{}

// Servers lists the servers of every tracked repo, main checkout and worktrees.
// A detected dev command that was never started has no row; it is listed as a
// stopped placeholder so the UI can start it (the start request names the repo,
// worktree and command).
func (a devServerAPI) Servers(ctx context.Context) ([]devservers.View, error) {
	projects, err := a.store.ListProjects(ctx)
	if err != nil {
		return nil, fmt.Errorf("list projects: %w", err)
	}
	out := []devservers.View{}
	for _, p := range projects {
		repos, err := a.store.ListRepos(ctx, p.ID)
		if err != nil {
			return nil, fmt.Errorf("list repos of %s: %w", p.ID, err)
		}
		worktrees, err := a.activeWorktrees(ctx, p.ID)
		if err != nil {
			return nil, err
		}
		for _, r := range repos {
			views, err := a.List(ctx, r.ID)
			if err != nil {
				return nil, fmt.Errorf("list dev servers of %s: %w", r.ID, err)
			}
			out = append(out, views...)
			have := map[string]bool{}
			for _, v := range views {
				have[v.ID] = true
			}
			targets := []devservers.Target{{Repo: r.ID}}
			for _, w := range worktrees[r.ID] {
				targets = append(targets, devservers.Target{Repo: r.ID, Worktree: w})
			}
			for _, t := range targets {
				dir, err := (trackedDirs{a.store}).Dir(ctx, t.Repo, t.Worktree)
				if err != nil {
					continue // not usable (moved, refused path): nothing to offer
				}
				steps := devSteps(dir)
				for _, command := range slices.Sorted(maps.Keys(steps)) {
					key := t.Key(command)
					if !have[devservers.IDOf(key)] {
						out = append(out, a.Placeholder(key))
					}
				}
			}
		}
	}
	return out, nil
}

// activeWorktrees lists the active worktrees of a project's tasks by repo.
func (a devServerAPI) activeWorktrees(ctx context.Context, project core.ProjectID) (map[core.RepoID][]core.WorktreeID, error) {
	tasks, err := a.store.ListTasks(ctx, project)
	if err != nil {
		return nil, fmt.Errorf("list tasks of %s: %w", project, err)
	}
	out := map[core.RepoID][]core.WorktreeID{}
	for _, t := range tasks {
		wts, err := a.store.ListWorktrees(ctx, t.ID)
		if err != nil {
			return nil, fmt.Errorf("list worktrees of %s: %w", t.ID, err)
		}
		for _, w := range wts {
			if w.Status == core.WorktreeActive {
				out[w.RepoID] = append(out[w.RepoID], w.ID)
			}
		}
	}
	return out, nil
}

// RepoName reads the store only. It must not call the dev-server service:
// PublishServer calls it after a commit.
func (a devServerAPI) RepoName(ctx context.Context, id core.RepoID) string {
	r, err := a.store.GetRepoIncludingUntracked(ctx, id)
	if err != nil {
		return ""
	}
	return r.Name
}

func newDevServers(ctx context.Context, cfg config, store *db.Store, tsk *tasks.Service, envSvc *worktreeenv.Service, hub devservers.Publisher, red safety.Redactor, log *slog.Logger) (*devservers.Service, error) {
	dirs := trackedDirs{store}
	start, ports, resolve := platform.StartFunc(platform.Start), platform.ListeningPorts, platform.Resolve
	if h := cfg.phase5; h != nil {
		if h.start != nil {
			start = h.start
		}
		if h.ports != nil {
			ports = h.ports
		}
		if h.resolve != nil {
			resolve = h.resolve
		}
	}
	svc, err := devservers.New(ctx, devservers.Deps{
		Store: devservers.NewSQLiteStore(store, red), Repos: devRepos{dirs},
		Trust: trustSource{svc: tsk}, Templates: devTemplates{dirs}, Blocks: devBlocks{envSvc},
		Start: start, KillTask: platform.KillTask, Resolve: resolve, Ports: ports,
		Orphans: devservers.RealOrphans(cfg.dataDir),
		Env:     safety.NewEnvBuilder(), Redactor: red, Hub: hub,
		Clock: devservers.RealClock{}, IDs: db.NewID, Log: log, DataDir: cfg.dataDir,
	})
	if err != nil {
		return nil, fmt.Errorf("start dev servers: %w", err)
	}
	return svc, nil
}

// ---- worktree environment -------------------------------------------------

// lateEnvHub forwards "profile copy done" to the API server, which is built
// after the worktree environment service (tasks need the service first).
type lateEnvHub struct {
	fn atomic.Pointer[func(core.WorktreeID)]
}

func (h *lateEnvHub) Set(f func(core.WorktreeID)) { h.fn.Store(&f) }

func (h *lateEnvHub) Publish(w core.WorktreeID) {
	if f := h.fn.Load(); f != nil {
		(*f)(w)
	}
}

// newWorktreeEnv builds the port-block and profile service over the store.
func newWorktreeEnv(store *db.Store, red safety.Redactor, hub *lateEnvHub, log *slog.Logger) (*worktreeenv.Service, error) {
	dirs := trackedDirs{store}
	st := worktreeenv.DBStore{S: store, Redactor: red}
	svc, err := worktreeenv.New(worktreeenv.Config{
		Ports: st, Settings: st, Audit: st, Prober: worktreeenv.PlatformProber{}, FS: worktreeenv.OSFileSystem{},
		RepoDir: func(ctx context.Context, repo core.RepoID) (string, error) { return dirs.RepoDir(ctx, repo) },
		Now:     func() time.Time { return time.Now().UTC() }, NewID: db.NewID,
		Logger: log, OnProfileDone: hub.Publish,
	})
	if err != nil {
		return nil, fmt.Errorf("start the worktree environment: %w", err)
	}
	return svc, nil
}

// releaseRemoved gives back the port blocks of worktrees that were removed
// (WE-02). Idempotent; a failure is logged and tried again at the next sweep.
func releaseRemoved(ctx context.Context, store *db.Store, svc *worktreeenv.Service, log *slog.Logger) {
	live, err := store.ListLiveWorktreePorts(ctx)
	if err != nil {
		log.Warn("could not list port blocks", "err", err)
		return
	}
	for _, rec := range live {
		w, err := store.GetWorktree(ctx, rec.Worktree)
		if err != nil || w.Status != core.WorktreeRemoved {
			continue
		}
		if err := svc.Release(ctx, rec.Worktree); err != nil && ctx.Err() == nil {
			log.Warn("could not release a port block", "worktree", rec.Worktree, "err", err)
		}
	}
}

// worktreeEnvAPI implements api.WorktreeEnvService. The last profile-copy
// results are kept in memory only (lost on restart); compose is "off" until
// compose exists (task E2).
type worktreeEnvAPI struct {
	svc   *worktreeenv.Service
	store *db.Store
	dbs   worktreeenv.DBStore
	now   func() time.Time

	mu   sync.Mutex
	last map[core.WorktreeID]copyResult
}

type copyResult struct {
	entries []api.ProfileEntryResult
	at      time.Time
}

var _ api.WorktreeEnvService = (*worktreeEnvAPI)(nil)

func newWorktreeEnvAPI(svc *worktreeenv.Service, store *db.Store, red safety.Redactor) *worktreeEnvAPI {
	return &worktreeEnvAPI{svc: svc, store: store, dbs: worktreeenv.DBStore{S: store, Redactor: red},
		now: func() time.Time { return time.Now().UTC() }, last: map[core.WorktreeID]copyResult{}}
}

// envRefusal classifies the errors of the worktree environment.
func envRefusal(err error) error {
	var we *worktreeenv.Error
	switch {
	case err == nil:
		return nil
	case errors.Is(err, errNotTracked), errors.Is(err, db.ErrNotFound), errors.Is(err, worktreeenv.ErrNotFound):
		return apperr.New(apperr.NotFound, "That repo or worktree is not tracked any more.", err)
	case errors.As(err, &we):
		return apperr.New(apperr.Invalid, we.UserMessage(), err)
	case errors.Is(err, worktreeenv.ErrRangeFull):
		return apperr.New(apperr.Unavailable, "All ports in the configured range are taken. Widen the range or remove a worktree.", err)
	}
	return err
}

func (a *worktreeEnvAPI) repoDTO(ctx context.Context, repo core.RepoID) (api.RepoEnvDTO, error) {
	if _, err := (trackedDirs{a.store}).RepoDir(ctx, repo); err != nil {
		return api.RepoEnvDTO{}, envRefusal(err)
	}
	e, err := a.dbs.RepoEnv(ctx, repo)
	if err != nil {
		return api.RepoEnvDTO{}, envRefusal(err)
	}
	paths := make([]string, len(e.Profile))
	for i, p := range e.Profile {
		paths[i] = string(p)
	}
	return api.RepoEnvDTO{RepoID: repo, ProfilePaths: paths, AgentCopy: e.AgentCopy}, nil
}

func (a *worktreeEnvAPI) RepoEnv(ctx context.Context, repo core.RepoID) (api.RepoEnvDTO, error) {
	return a.repoDTO(ctx, repo)
}

func (a *worktreeEnvAPI) SetRepoEnv(ctx context.Context, repo core.RepoID, in api.RepoEnvDTO) (api.RepoEnvDTO, error) {
	if _, err := (trackedDirs{a.store}).RepoDir(ctx, repo); err != nil {
		return api.RepoEnvDTO{}, envRefusal(err)
	}
	// Compose is not built yet (E2). Nothing of it is stored, so a request that
	// sets any compose field is refused, never half applied.
	if in.ComposeEnabled || in.ComposeFile != "" {
		return api.RepoEnvDTO{}, apperr.New(apperr.Unavailable, "Docker compose is not available yet, so its settings cannot be saved.", nil)
	}
	if err := a.svc.SetProfile(ctx, repo, in.ProfilePaths, in.AgentCopy); err != nil {
		return api.RepoEnvDTO{}, envRefusal(err)
	}
	return a.repoDTO(ctx, repo)
}

func (a *worktreeEnvAPI) WorktreeEnv(ctx context.Context, id core.WorktreeID) (api.WorktreeEnvDTO, error) {
	w, err := a.store.GetWorktree(ctx, id)
	if err != nil {
		return api.WorktreeEnvDTO{}, envRefusal(err)
	}
	if _, err := (trackedDirs{a.store}).WorktreeDir(ctx, w.RepoID, id); err != nil {
		return api.WorktreeEnvDTO{}, envRefusal(err)
	}
	return a.dto(ctx, id)
}

func (a *worktreeEnvAPI) dto(ctx context.Context, id core.WorktreeID) (api.WorktreeEnvDTO, error) {
	out := api.WorktreeEnvDTO{WorktreeID: id, Profile: []api.ProfileEntryResult{}, Compose: api.ComposeOff}
	blocks, err := a.dbs.LoadBlocks(ctx)
	if err != nil {
		return api.WorktreeEnvDTO{}, envRefusal(err)
	}
	if b, ok := blocks[id]; ok {
		out.BlockBase, out.BlockSize = b.Base, b.Size
	}
	a.mu.Lock()
	if r, ok := a.last[id]; ok {
		out.Profile, out.UpdatedAt = slices.Clone(r.entries), r.at
	}
	a.mu.Unlock()
	return out, nil
}

// CopyProfile copies the repo's profile into the worktree as a user action
// (WE-01). The per-entry result is derived from the files before and after,
// since the service reports only success or failure.
func (a *worktreeEnvAPI) CopyProfile(ctx context.Context, id core.WorktreeID) (api.WorktreeEnvDTO, error) {
	w, err := a.store.GetWorktree(ctx, id)
	if err != nil {
		return api.WorktreeEnvDTO{}, envRefusal(err)
	}
	if _, err := (trackedDirs{a.store}).WorktreeDir(ctx, w.RepoID, id); err != nil {
		return api.WorktreeEnvDTO{}, envRefusal(err)
	}
	settings, err := a.dbs.RepoEnv(ctx, w.RepoID)
	if err != nil {
		return api.WorktreeEnvDTO{}, envRefusal(err)
	}
	exists := func(rel wenv.RelPath) bool {
		_, err := os.Lstat(filepath.Join(w.Path, filepath.FromSlash(string(rel))))
		return err == nil
	}
	before := map[wenv.RelPath]bool{}
	for _, p := range settings.Profile {
		before[p] = exists(p)
	}
	if err := a.svc.CopyProfile(ctx, w, wenv.TriggerUserConfirmed); err != nil {
		return api.WorktreeEnvDTO{}, envRefusal(err)
	}
	res := make([]api.ProfileEntryResult, 0, len(settings.Profile))
	for _, p := range settings.Profile {
		switch {
		case before[p]:
			res = append(res, api.ProfileEntryResult{Path: string(p), Result: "skipped", Reason: "The file already exists in the worktree, so it was left alone."})
		case exists(p):
			res = append(res, api.ProfileEntryResult{Path: string(p), Result: "copied"})
		default:
			res = append(res, api.ProfileEntryResult{Path: string(p), Result: "skipped", Reason: "The file was not copied. It may be missing in the repo or not allowed."})
		}
	}
	a.mu.Lock()
	a.last[id] = copyResult{entries: res, at: a.now()}
	a.mu.Unlock()
	return a.dto(ctx, id)
}

// ---- terminal -------------------------------------------------------------

// terminalFolders implements terminal.Folders over the tracked folders.
type terminalFolders struct{ dirs trackedDirs }

func (f terminalFolders) Resolve(ctx context.Context, t ct.Target) (ct.Folder, error) {
	folder := ct.Folder{Repo: t.Repo}
	if t.Worktree != "" {
		w, err := f.dirs.store.GetWorktree(ctx, t.Worktree)
		if errors.Is(err, db.ErrNotFound) {
			return ct.Folder{}, terminal.ErrNotTracked
		}
		if err != nil {
			return ct.Folder{}, fmt.Errorf("read worktree %s: %w", t.Worktree, err)
		}
		folder = ct.Folder{Repo: w.RepoID, Worktree: w.ID}
	}
	if _, err := f.Dir(ctx, folder); err != nil {
		return ct.Folder{}, err
	}
	return folder, nil
}

// Dir re-checks that the folder is still tracked every time it is asked.
func (f terminalFolders) Dir(ctx context.Context, folder ct.Folder) (string, error) {
	dir, err := f.dirs.RunDir(ctx, folder.Repo, folder.Worktree)
	var up *unsupportedPathError
	if errors.Is(err, errNotTracked) || errors.As(err, &up) {
		return "", terminal.ErrNotTracked
	}
	return dir, err
}

// terminalAudit implements terminal.AuditSink: one audit row per open, close
// or failed open, with IDs, cause and exit code only (never a path, command
// line or keystroke).
type terminalAudit struct {
	store *db.Store
	red   safety.Redactor
}

func (a terminalAudit) Record(ctx context.Context, e terminal.AuditEntry) error {
	detail, err := json.Marshal(map[string]any{
		"repo": e.Folder.Repo, "worktree": e.Folder.Worktree, "cause": e.Cause, "code": e.Code,
	})
	if err != nil {
		return fmt.Errorf("encode terminal audit detail: %w", err)
	}
	id := core.AuditID(db.NewID("aud"))
	p, err := audit.Prepare(core.AuditEntry{
		ID: id, At: e.At, Actor: "user", Action: string(e.Action),
		Subject: "terminal:" + string(e.Session), Detail: detail,
	}, a.red, id, e.At)
	if err != nil {
		return fmt.Errorf("prepare terminal audit entry: %w", err)
	}
	return a.store.Commit(ctx, func(tx *db.Tx) error { return tx.AppendAudit(ctx, p) })
}

func newTerminal(cfg config, store *db.Store, red safety.Redactor, log *slog.Logger) (*terminal.Service, error) {
	var start terminal.PTYStarter
	if cfg.phase5 != nil {
		start = cfg.phase5.pty
	}
	svc, err := terminal.New(terminal.Config{
		Start:   start,
		Folders: terminalFolders{trackedDirs{store}}, Env: safety.NewEnvBuilder(),
		Audit: terminalAudit{store: store, red: red}, Log: log,
	})
	if err != nil {
		return nil, fmt.Errorf("start the terminal: %w", err)
	}
	return svc, nil
}

// terminalCloseTimeout bounds the wait for terminal sessions at shutdown.
const terminalCloseTimeout = 10 * time.Second

// closePhase5 stops the dev servers and the terminal sessions. It runs before
// the store closes.
func (a *app) closePhase5() {
	ctx := context.Background()
	if a.devservers != nil {
		a.devservers.Close(ctx)
	}
	if a.terminal != nil {
		// A shell that will not end must not hold up the shutdown for good.
		tctx, cancel := context.WithTimeout(ctx, terminalCloseTimeout)
		defer cancel()
		if err := a.terminal.Close(tctx); err != nil {
			a.log.Warn("could not close the terminals cleanly", "err", err)
		}
	}
}
