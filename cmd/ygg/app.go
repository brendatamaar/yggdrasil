package main

import (
	"context"
	"errors"
	"fmt"
	"io"
	"io/fs"
	"log/slog"
	"net"
	"os"
	"path/filepath"
	"time"

	"golang.org/x/sync/errgroup"

	"github.com/brendatamaar/yggdrasil/internal/agents"
	"github.com/brendatamaar/yggdrasil/internal/agents/claudecode"
	"github.com/brendatamaar/yggdrasil/internal/agents/mock"
	"github.com/brendatamaar/yggdrasil/internal/api"
	"github.com/brendatamaar/yggdrasil/internal/builds"
	"github.com/brendatamaar/yggdrasil/internal/builds/toolchains"
	"github.com/brendatamaar/yggdrasil/internal/core"
	"github.com/brendatamaar/yggdrasil/internal/db"
	"github.com/brendatamaar/yggdrasil/internal/git"
	"github.com/brendatamaar/yggdrasil/internal/platform"
	"github.com/brendatamaar/yggdrasil/internal/repos"
	"github.com/brendatamaar/yggdrasil/internal/safety"
)

// app is the running Yggdrasil: the database, the audit log, the agents and
// the HTTP server, wired together.
type app struct {
	cfg   config
	log   *slog.Logger
	db    *db.DB
	store *db.Store
	srv   *api.Server
	// status is the repo-status read model (WS-02). Its owner goroutine runs
	// next to the server in run.
	status *repos.StatusService
	// builds runs the build queue (BL-03). Like status, it runs next to the
	// server; it must be running before builds are served.
	builds *builds.Service
}

// newLogger writes redacted text logs: no token reaches a log (invariant 4).
func newLogger(w io.Writer, level slog.Level, r safety.Redactor) *slog.Logger {
	return slog.New(safety.NewLogHandler(slog.NewTextHandler(w, &slog.HandlerOptions{Level: level}), r))
}

// openStore opens the database in the data folder and returns what the
// commands share.
func openStore(ctx context.Context, cfg config, r safety.Redactor) (*db.DB, *db.Store, error) {
	if err := os.MkdirAll(cfg.dataDir, 0o700); err != nil {
		return nil, nil, fmt.Errorf("create the data folder %s: %w", cfg.dataDir, err)
	}
	d, err := db.Open(ctx, filepath.Join(cfg.dataDir, "ygg.db"))
	if err != nil {
		return nil, nil, fmt.Errorf("open the database in %s: %w", cfg.dataDir, err)
	}
	store := db.NewStore(d, r.Redact)
	return d, store, nil
}

// newApp wires the application. assets is the embedded frontend.
func newApp(ctx context.Context, cfg config, assets fs.FS, logOut io.Writer) (*app, error) {
	redactor := safety.NewRedactor()
	log := newLogger(logOut, cfg.level, redactor)

	d, store, err := openStore(ctx, cfg, redactor)
	if err != nil {
		return nil, err
	}

	self, err := os.Executable()
	if err != nil {
		_ = d.Close()
		return nil, fmt.Errorf("find the ygg executable (the Claude Code hook runs it): %w", err)
	}
	runs := filepath.Join(cfg.dataDir, "runs")
	if err := os.MkdirAll(runs, 0o700); err != nil {
		_ = d.Close()
		return nil, fmt.Errorf("create the runs folder: %w", err)
	}
	adapters := map[string]agents.Adapter{
		// The demo agent replays a recorded session with nested sub-agents. It
		// starts no process and reads nothing, so it is always on.
		"mock":        mock.New(mock.Options{Fixture: "subagents", Delay: cfg.mockDelay}),
		"claude-code": claudecode.New(claudecode.Options{HookExecutable: self, TempDir: runs}),
	}

	gitEnv, err := safety.NewEnvBuilder().Build(safety.EnvOptions{})
	if err != nil {
		_ = d.Close()
		return nil, fmt.Errorf("prepare the git environment: %w", err)
	}
	g, err := git.New(gitEnv)
	if err != nil {
		_ = d.Close()
		return nil, fmt.Errorf("prepare git: %w", err)
	}
	// The status service publishes through the server, and the server serves
	// the status: build the service first, give it the publisher after New.
	status := repos.NewStatusService(store, repos.NewGitReader(g), func() time.Time { return time.Now().UTC() }, nil, repos.WithLogger(log))
	reposPub := &lateReposPublisher{}
	tracking, err := repos.NewTrackingService(repos.Deps{Store: store, Redactor: redactor, Status: status, Log: log, Publisher: reposPub})
	if err != nil {
		_ = d.Close()
		return nil, fmt.Errorf("start repo tracking: %w", err)
	}

	buildsPub := &lateBuildsPublisher{}
	buildSvc, err := builds.NewService(builds.Deps{
		Store: builds.NewSQLiteStore(store), Start: platform.Start,
		Locator: buildLocator{store}, Env: safety.NewEnvBuilder(),
		Toolchains: toolchains.All(), DataDir: cfg.dataDir,
		Redactor: redactor, Publisher: buildsPub, Status: status, Log: log,
	})
	if err != nil {
		_ = d.Close()
		return nil, fmt.Errorf("start builds: %w", err)
	}

	srv, err := api.New(api.Deps{
		Store: store, Adapters: adapters, Env: safety.NewEnvBuilder(), Log: log,
		Redactor: redactor, Version: version, Assets: assets,
		Tracking: tracking, Status: status, RepoDetail: repos.NewDetailService(store, g),
		Builds: buildSvc,
	})
	if err != nil {
		_ = d.Close()
		return nil, fmt.Errorf("start the server: %w", err)
	}
	// Set before run starts any goroutine that could publish.
	status.SetPublisher(statusPublisher{srv})
	reposPub.srv = srv
	buildsPub.srv = srv
	return &app{cfg: cfg, log: log, db: d, store: store, srv: srv, status: status, builds: buildSvc}, nil
}

// statusPublisher sends repo-status changes to the browser.
type statusPublisher struct{ srv *api.Server }

func (p statusPublisher) RepoChanged(id core.RepoID) { p.srv.PublishRepoStatus(id) }
func (p statusPublisher) CountsChanged()             { p.srv.PublishRepoCounts() }

// run serves on ln until ctx is cancelled. On the way out the server stops
// every task, then the database is closed. Closing the process-wide job (so no
// child outlives ygg) is main's job: it happens once, when the process ends.
func (a *app) run(ctx context.Context, ln net.Listener) error {
	a.log.Info("Yggdrasil is running", "url", "http://"+ln.Addr().String(), "version", version, "data", a.cfg.dataDir)
	group, gctx := errgroup.WithContext(ctx)
	group.Go(func() error { return a.status.Start(gctx) })
	group.Go(func() error { return a.builds.Start(gctx) })
	group.Go(func() error { return a.srv.Run(gctx, ln) })
	serveErr := group.Wait()
	closeErr := a.db.Close()
	return errors.Join(serveErr, closeErr)
}

// lateReposPublisher forwards tracking's "repos changed" to the server, which
// is built after the tracking service. srv is set once, before run starts.
type lateReposPublisher struct{ srv *api.Server }

func (p *lateReposPublisher) PublishReposChanged() {
	if p.srv != nil {
		p.srv.PublishReposChanged()
	}
}
