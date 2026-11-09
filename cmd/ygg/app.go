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

	"github.com/brendatamaar/yggdrasil/internal/agents/claudecode"
	"github.com/brendatamaar/yggdrasil/internal/agents/mock"
	"github.com/brendatamaar/yggdrasil/internal/api"
	"github.com/brendatamaar/yggdrasil/internal/builds"
	"github.com/brendatamaar/yggdrasil/internal/builds/toolchains"
	"github.com/brendatamaar/yggdrasil/internal/changesets"
	"github.com/brendatamaar/yggdrasil/internal/db"
	"github.com/brendatamaar/yggdrasil/internal/devservers"
	"github.com/brendatamaar/yggdrasil/internal/git"
	"github.com/brendatamaar/yggdrasil/internal/hosts"
	"github.com/brendatamaar/yggdrasil/internal/hosts/hostclients"
	"github.com/brendatamaar/yggdrasil/internal/mainlock"
	"github.com/brendatamaar/yggdrasil/internal/platform"
	"github.com/brendatamaar/yggdrasil/internal/proposals"
	"github.com/brendatamaar/yggdrasil/internal/repos"
	"github.com/brendatamaar/yggdrasil/internal/safety"
	"github.com/brendatamaar/yggdrasil/internal/tasks"
	"github.com/brendatamaar/yggdrasil/internal/terminal"
	"github.com/brendatamaar/yggdrasil/internal/verify"
	"github.com/brendatamaar/yggdrasil/internal/worktreeenv"
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
	// hosts polls the git hosts for MR and pipeline status (GH-02).
	hosts *hosts.Service
	// actions runs user-initiated git actions (WS-06, WS-07).
	actions *repos.ActionsService
	// feeder gives the hosts service the repo set, off the status publish path.
	feeder *hostsFeeder
	// tasks runs agent tasks. It starts in run and is closed before the store.
	tasks *tasks.Service
	// proposals decides and runs proposals. It starts after tasks and closes
	// before them, and before the store.
	proposals *proposals.Service
	// mcp serves /mcp and issues per-run tokens.
	mcp *mcpService
	// verify runs automatic verification (VF-01); changesets runs change sets
	// and review notes; fanout hands proposal changes to changesets. All three
	// run in the errgroup of run, so they stop before the store closes.
	verify     *verify.Service
	changesets *changesets.Service
	fanout     *proposalFanout
	// devservers, terminal and envSvc are the Phase 5 services; run starts
	// them and closes the first two before the store closes.
	devservers *devservers.Service
	compose    *worktreeenv.ComposeService
	terminal   *terminal.Service
	envSvc     *worktreeenv.Service
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
	// Host tokens (GH-04) are loaded first, so the redactor knows them before
	// anything is logged (invariant 4). A missing .env file is fine.
	envPath := hosts.EnvPath(cfg.dataDir, nil)
	hostsCfg, err := hosts.LoadConfig(envPath, nil)
	if err != nil {
		// An unreadable .env must not stop Yggdrasil: start with the default
		// hosts and tell the user why in the settings (the error text has no
		// file content).
		hostsCfg = hosts.ParseConfig(map[string]hosts.Var{})
		hostsCfg.Problems = append(hostsCfg.Problems, hosts.Problem{Name: hosts.EnvFileName, Reason: "the file could not be read, so only github.com and gitlab.com are known. Check its permissions."})
	}
	redactor := safety.NewRedactor(hostsCfg.Secrets()...)
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
	adapters := adapterRegistry{
		// The demo agent replays a recorded session with nested sub-agents. It
		// starts no process and reads nothing, so it is always on.
		"mock":        mock.New(mock.Options{Fixture: "subagents", Delay: cfg.mockDelay, Enforcement: cfg.mockEnforcement}),
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

	// One main-checkout lock for tasks, builds and git actions (AG-11).
	checkoutLock := &mainlock.Registry{}
	buildsPub := &lateBuildsPublisher{}
	// A finished build also wakes the verify loop (VF-01).
	wake := &wakeOnBuild{next: buildsPub}
	buildSvc, err := builds.NewService(builds.Deps{
		Store: builds.NewSQLiteStore(store), Start: platform.Start,
		Locator: buildLocator{store}, Env: safety.NewEnvBuilder(),
		Toolchains: toolchains.All(), DataDir: cfg.dataDir,
		Redactor: redactor, Publisher: wake, Status: status, Log: log,
		MainLock: checkoutLock,
	})
	if err != nil {
		_ = d.Close()
		return nil, fmt.Errorf("start builds: %w", err)
	}

	batchPub := &lateBatchPublisher{}
	actions, err := repos.NewActionsService(repos.ActionsDeps{
		Store: store, Redactor: redactor, Git: g, Builds: buildSvc,
		Status: status, Publisher: batchPub, Log: log, MainLock: checkoutLock,
	})
	if err != nil {
		_ = d.Close()
		return nil, fmt.Errorf("start git actions: %w", err)
	}
	opener, err := repos.NewOpenService(repos.OpenDeps{Store: store, Redactor: redactor, Env: safety.NewEnvBuilder()})
	if err != nil {
		_ = d.Close()
		return nil, fmt.Errorf("start open in editor: %w", err)
	}

	hostsPub := &lateHostsPublisher{}
	hostSvc := hosts.New(hosts.Deps{
		Config: hostsCfg, EnvPath: envPath,
		Reload:  func() (hosts.Config, error) { return hosts.LoadConfig(envPath, nil) },
		Clients: hostclients.New(nil, nil), Remotes: g, Publish: hostsPub, Log: log,
		Secrets: redactor,
	})

	searchSvc, err := newSearchService(store, log)
	if err != nil {
		_ = d.Close()
		return nil, fmt.Errorf("start code search: %w", err)
	}

	propHub := &api.ProposalsHub{}
	fanout := newProposalFanout(propHub, log.Warn)
	live := &liveProbe{store: store, builds: buildSvc}
	propSvc, err := newProposals(store, g, checkoutLock, buildSvc, hostSvc, fanout, live, cfg.dataDir, redactor)
	if err != nil {
		_ = d.Close()
		return nil, err
	}

	mcpSvc, err := newMCP(store, g, buildSvc, hostSvc, actions, propSvc, redactor, version, cfg.dataDir)
	if err != nil {
		_ = d.Close()
		return nil, err
	}
	envHub := &lateEnvHub{}
	envSvc, err := newWorktreeEnv(store, redactor, envHub, log)
	if err != nil {
		_ = d.Close()
		return nil, err
	}
	taskHub := &api.HubAdapter{}
	taskSvc, err := newTasks(store, adapters, g, taskHub, redactor, checkoutLock, cfg.dataDir, cfg.writeModes, propSvc.PendingCount, mcpSvc, envSvc, log)
	if err != nil {
		_ = d.Close()
		return nil, err
	}

	verifyHub := &lateVerifyHub{}
	verifySvc, err := newVerifyService(store, buildSvc, taskSvc, g, redactor, verifyHub)
	if err != nil {
		_ = d.Close()
		return nil, err
	}
	csHub := &lateChangeSetHub{}
	csSvc, err := newChangeSetsService(store, propSvc, taskSvc, g, redactor, csHub, log)
	if err != nil {
		_ = d.Close()
		return nil, err
	}
	// Everything that needs the verify and change-set services is bound
	// before run starts any goroutine.
	wake.svc.Store(verifySvc)
	live.verify.Store(verifySvc)
	fanout.svc.Store(csSvc)

	if cfg.noSession {
		log.Warn("the UI session is switched off (YGG_INSECURE_NO_SESSION): any program on this computer can use the API; use this only for task dev")
	}
	if cfg.noSessionRefused {
		log.Warn("YGG_INSECURE_NO_SESSION is ignored because YGG_WRITE_MODES is on: the UI session stays required")
	}
	srv, err := api.New(api.Deps{
		InsecureNoSession: cfg.noSession,
		Store:             store, Log: log, Tasks: taskSvc, Worktrees: api.NewWorktreeServiceWithGit(store, g), WriteModes: cfg.writeModes,
		Redactor: redactor, Version: version, Assets: assets,
		Tracking: tracking, Status: status, RepoDetail: repos.NewDetailService(store, g),
		Builds: buildSvc, Hosts: hostSvc, Search: searchSvc,
		GitActions: repos.GitActions{ActionsService: actions, OpenService: opener},
		Proposals:  propSvc, Agents: newAgentService(adapters), MCP: mcpSvc,
		Composer: newComposer(store, adapters, taskSvc, g, redactor, log),
	})
	if err != nil {
		_ = d.Close()
		return nil, fmt.Errorf("start the server: %w", err)
	}
	// Set before run starts any goroutine that could publish.
	feeder := newHostsFeeder(status, hostSvc, log)
	status.SetPublisher(statusPublisher{srv: srv, feeder: feeder})
	reposPub.srv = srv
	buildsPub.srv = srv
	hostsPub.srv = srv
	batchPub.srv = srv
	taskHub.Bind(srv)
	propHub.Bind(srv)
	verifyHub.Set(srv)
	csHub.Set(srv)
	names := repoNamer(store)
	srv.SetPhase4Services(api.Phase4Services{
		Verification: api.NewVerificationService(verifySvc, names),
		Review:       api.NewReviewService(csSvc, store),
		ChangeSets:   api.NewChangeSetService(csSvc, names),
		Landing:      api.NewLandingService(store, g, propSvc),
		WorktreeDiff: api.NewWorktreeDiffer(store, g),
	})
	envHub.Set(srv.PublishWorktreeEnv)
	// The Phase 5 services publish to the server, so they come after it.
	devSvc, devCat, err := newDevServers(ctx, cfg, store, taskSvc, envSvc, srv, redactor, log)
	if err != nil {
		_ = d.Close()
		return nil, err
	}
	termSvc, err := newTerminal(cfg, store, redactor, log)
	if err != nil {
		devSvc.Close(ctx)
		_ = d.Close()
		return nil, err
	}
	envAPI, err := newWorktreeEnvAPI(ctx, envSvc, store, redactor, envHub, log)
	if err != nil {
		devSvc.Close(ctx)
		_ = d.Close()
		return nil, err
	}
	srv.SetPhase5Services(api.Phase5Services{
		DevServers:  devServerAPI{Service: devSvc, store: store, cat: devCat},
		WorktreeEnv: envAPI,
		Terminal:    termSvc,
	})
	fanout.after = func(ctx context.Context) { releaseRemoved(ctx, store, envSvc, log) }
	return &app{
		devservers: devSvc, terminal: termSvc, envSvc: envSvc, compose: envAPI.compose,
		cfg: cfg, log: log, db: d, store: store, srv: srv, status: status, builds: buildSvc, hosts: hostSvc, actions: actions,
		feeder: feeder, tasks: taskSvc, proposals: propSvc, mcp: mcpSvc,
		verify: verifySvc, changesets: csSvc, fanout: fanout,
	}, nil
}

// run serves on ln until ctx is cancelled. On the way out the server stops
// every task, then the database is closed. Closing the process-wide job (so no
// child outlives ygg) is main's job: it happens once, when the process ends.
func (a *app) run(ctx context.Context, ln net.Listener) error {
	a.mcp.setAddr(ln.Addr())
	a.log.Info("Yggdrasil is running", "url", "http://"+ln.Addr().String(), "version", version, "data", a.cfg.dataDir)
	// Recovery of tasks that were running at the last shutdown comes before
	// anything is served; Close below stops every run before the store closes.
	if err := a.tasks.Start(ctx); err != nil {
		a.closePhase5()
		_ = a.tasks.Close()
		return errors.Join(fmt.Errorf("start tasks: %w", err), a.db.Close())
	}
	// Proposals that were Confirmed at the last shutdown are reconciled here,
	// never executed again on their own.
	if err := a.proposals.Start(ctx); err != nil {
		a.proposals.Close()
		a.closePhase5()
		return errors.Join(fmt.Errorf("start proposals: %w", err), a.tasks.Close(), a.db.Close())
	}
	// Dev servers that were running at the last shutdown are reconciled
	// (stopped or marked orphaned) before anything is served (DV-03). A
	// failure is not fatal: the rows are tried again at the next start.
	if err := a.devservers.Reconcile(ctx); err != nil {
		a.log.Warn("could not reconcile the dev servers of the last run", "err", err)
	}
	releaseRemoved(ctx, a.store, a.envSvc, a.log)
	group, gctx := errgroup.WithContext(ctx)
	group.Go(func() error { return a.status.Start(gctx) })
	group.Go(func() error { return a.builds.Start(gctx) })
	group.Go(func() error { return a.actions.Run(gctx) })
	// The hosts service starts its own goroutines and returns at once; Close
	// stops them after the server is done.
	group.Go(func() error { return a.hosts.Start(gctx) })
	group.Go(func() error { return a.feeder.run(gctx) })
	startVerify(group, gctx, a.verify)
	startChangeSets(group, gctx, a.changesets)
	group.Go(func() error { return a.fanout.run(gctx) })
	group.Go(func() error { return a.srv.Run(gctx, ln) })
	serveErr := group.Wait()
	// Dev servers and terminals stop before anything the store serves them.
	a.closePhase5()
	a.proposals.Close()
	tasksErr := a.tasks.Close()
	a.hosts.Close()
	closeErr := a.db.Close()
	return errors.Join(serveErr, tasksErr, closeErr)
}

// lateReposPublisher forwards tracking's "repos changed" to the server, which
// is built after the tracking service. srv is set once, before run starts.
type lateReposPublisher struct{ srv *api.Server }

func (p *lateReposPublisher) PublishReposChanged() {
	if p.srv != nil {
		p.srv.PublishReposChanged()
	}
}
