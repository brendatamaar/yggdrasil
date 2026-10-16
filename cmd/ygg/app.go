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

	"github.com/brendatamaar/yggdrasil/internal/agents"
	"github.com/brendatamaar/yggdrasil/internal/agents/claudecode"
	"github.com/brendatamaar/yggdrasil/internal/agents/mock"
	"github.com/brendatamaar/yggdrasil/internal/api"
	"github.com/brendatamaar/yggdrasil/internal/audit"
	"github.com/brendatamaar/yggdrasil/internal/db"
	"github.com/brendatamaar/yggdrasil/internal/safety"
)

// app is the running Yggdrasil: the database, the audit log, the agents and
// the HTTP server, wired together.
type app struct {
	cfg   config
	log   *slog.Logger
	db    *db.DB
	store *db.Store
	audit audit.Writer
	srv   *api.Server
}

// newLogger writes redacted text logs: no token reaches a log (invariant 4).
func newLogger(w io.Writer, level slog.Level, r safety.Redactor) *slog.Logger {
	return slog.New(safety.NewLogHandler(slog.NewTextHandler(w, &slog.HandlerOptions{Level: level}), r))
}

// openStore opens the database in the data folder and returns what the
// commands share.
func openStore(ctx context.Context, cfg config, r safety.Redactor) (*db.DB, *db.Store, audit.Writer, error) {
	if err := os.MkdirAll(cfg.dataDir, 0o700); err != nil {
		return nil, nil, nil, fmt.Errorf("create the data folder %s: %w", cfg.dataDir, err)
	}
	d, err := db.Open(ctx, filepath.Join(cfg.dataDir, "ygg.db"))
	if err != nil {
		return nil, nil, nil, fmt.Errorf("open the database in %s: %w", cfg.dataDir, err)
	}
	store := db.NewStore(d)
	return d, store, audit.NewSQLiteWriter(store, r), nil
}

// newApp wires the application. assets is the embedded frontend.
func newApp(ctx context.Context, cfg config, assets fs.FS, logOut io.Writer) (*app, error) {
	redactor := safety.NewRedactor()
	log := newLogger(logOut, cfg.level, redactor)

	d, store, aud, err := openStore(ctx, cfg, redactor)
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

	srv, err := api.New(api.Deps{
		Store: store, Audit: aud, Adapters: adapters, Env: safety.NewEnvBuilder(), Log: log,
		Redactor: redactor, Version: version, Assets: assets,
	})
	if err != nil {
		_ = d.Close()
		return nil, fmt.Errorf("start the server: %w", err)
	}
	return &app{cfg: cfg, log: log, db: d, store: store, audit: aud, srv: srv}, nil
}

// run serves on ln until ctx is cancelled. On the way out the server stops
// every task, then the database is closed. Closing the process-wide job (so no
// child outlives ygg) is main's job: it happens once, when the process ends.
func (a *app) run(ctx context.Context, ln net.Listener) error {
	a.log.Info("Yggdrasil is running", "url", "http://"+ln.Addr().String(), "version", version, "data", a.cfg.dataDir)
	serveErr := a.srv.Run(ctx, ln)
	closeErr := a.db.Close()
	return errors.Join(serveErr, closeErr)
}
