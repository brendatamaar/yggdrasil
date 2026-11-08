package devservers

import (
	"context"
	"log/slog"
	"time"

	"github.com/brendatamaar/yggdrasil/internal/core"
	"github.com/brendatamaar/yggdrasil/internal/core/guard"
	"github.com/brendatamaar/yggdrasil/internal/platform"
	"github.com/brendatamaar/yggdrasil/internal/safety"
)

// Repos resolves a target to the folder of a tracked repo or of one of its
// active worktrees (invariant 6, DV-05). It returns an apperr NotFound for an
// untracked repo, or a removed or unknown worktree. The folder is never taken
// from a request.
type Repos interface {
	Dir(ctx context.Context, t Target) (string, error)
}

// TrustSource reports the live trust view of a repo (SF-09): the same view
// bootstrap and verification read. A dev server runs the repo's own scripts,
// so an untrusted repo does not start one.
type TrustSource interface {
	TrustView(ctx context.Context, repo core.RepoID) (guard.TrustView, error)
}

// Templates gives the command of a dev server: the repo's override, or the
// toolchain's DevServer step (argv with {port} placeholders and extra env).
// A command that is not offered is an apperr Invalid.
type Templates interface {
	DevServer(ctx context.Context, t Target, command string) (argv []string, env map[string]string, err error)
}

// BlockSource gives the port block of a worktree (WE-02), allocating one if
// needed. A main checkout (empty Worktree) gets an empty block.
type BlockSource interface {
	BlockFor(ctx context.Context, t Target) (core.PortBlock, error)
}

// Publisher tells subscribers about changes (hub topics "devservers" and
// "devserver:<id>"). Neither call carries a path or a token. A slow client
// is the hub's problem, not the service's.
type Publisher interface {
	PublishServer(id string, view View)
	PublishLog(id string, line string)
}

// Orphans are the orphan functions of internal/platform with the data
// directory bound (RealOrphans). On Windows List returns nothing.
type Orphans struct {
	Record func(id string, pid int) error
	Forget func(id string) error
	List   func() ([]platform.Orphan, error)
	Stop   func(id string) error
}

// RealOrphans binds the platform functions to dataDir.
func RealOrphans(dataDir string) Orphans {
	return Orphans{
		Record: func(id string, pid int) error { return platform.RecordProcess(dataDir, id, pid) },
		Forget: func(id string) error { return platform.ForgetProcess(dataDir, id) },
		List:   func() ([]platform.Orphan, error) { return platform.ListOrphans(dataDir) },
		Stop:   func(id string) error { return platform.StopOrphan(dataDir, id) },
	}
}

// Deps are what the service needs. Everything but Hub, Clock, IDs, Log,
// DrainTimeout, LogLines and LogFileBytes is required.
type Deps struct {
	Store     Store
	Repos     Repos
	Trust     TrustSource
	Templates Templates
	Blocks    BlockSource

	// Start, KillTask, Resolve and Ports are the platform functions
	// (platform.Start, platform.KillTask, platform.Resolve,
	// platform.ListeningPorts) or their fakes.
	Start    platform.StartFunc
	KillTask platform.KillTaskFunc
	Resolve  func(name, dir string, env []string) (string, error)
	Ports    func(pid int) ([]int, error)
	Orphans  Orphans

	Env      safety.EnvBuilder
	Redactor safety.Redactor

	Hub   Publisher
	Clock Clock
	IDs   func(prefix string) string
	Log   *slog.Logger

	// DataDir holds the log files (<DataDir>/logs/devservers).
	DataDir string
	// DrainTimeout bounds how long Close waits for processes. Default 5 s.
	DrainTimeout time.Duration
	// LogLines is the ring size per server (default 1000); LogFileBytes the
	// rotation size (default 5 MiB).
	LogLines     int
	LogFileBytes int64
}
