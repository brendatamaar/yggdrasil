package newrepos

import (
	"context"
	"time"

	"github.com/brendatamaar/yggdrasil/internal/api"
	"github.com/brendatamaar/yggdrasil/internal/core"
	"github.com/brendatamaar/yggdrasil/internal/db"
	"github.com/brendatamaar/yggdrasil/internal/git"
	"github.com/brendatamaar/yggdrasil/internal/hosts"
	"github.com/brendatamaar/yggdrasil/internal/platform"
)

// Consumer-local ports of the new-repo service. Each has a fake and a contract
// suite run against the live implementation (docs/AGENTS.md 5.11); the live
// ones are named after the package that provides them.

// ReservationStore persists operations, their receipts and the outbox. It is
// implemented by *db.Store: reads are Get*/List*, every write is a Tx method
// inside Commit together with its audit entry. Reads of the reserved repo use
// the narrow GetReservedRepo; every ordinary read excludes reserved repos.
type ReservationStore interface {
	GetReservation(ctx context.Context, op core.OperationID) (db.ReservationRecord, error)
	GetReservationByRepo(ctx context.Context, repo core.RepoID) (db.ReservationRecord, error)
	ListUnsettledReservations(ctx context.Context) ([]db.ReservationRecord, error)
	ListReservations(ctx context.Context, project core.ProjectID) ([]db.ReservationRecord, error)
	ListEffectsByOperation(ctx context.Context, op core.OperationID) ([]db.EffectRecord, error)
	GetReservedRepo(ctx context.Context, id core.RepoID) (core.Repo, error)
	Commit(ctx context.Context, fn func(*db.Tx) error) error
}

// SafeInitFS creates the new folder and files (platform implementation).
type SafeInitFS = platform.SafeInitFS

// InitGit runs the local git stages (git implementation).
type InitGit = git.RepoInitializer

// Origin adds and reads the origin remote (git implementation).
type Origin = git.OriginManager

// RemoteCreator creates and finds the remote repository (hosts implementation).
type RemoteCreator = hosts.RepoHost

// PublishTransport reads and pushes the first branch through the in-process
// transport (git implementation); it needs a git.Authorizer, which the hosts
// service provides, so no token is ever given to a process.
type PublishTransport = git.PublishTransport

// MainLock is the shared per-repo main-checkout lock. A stage holds it only
// while it mutates the checkout and never while waiting for another
// lock-taking service (internal/mainlock implements it).
type MainLock interface {
	Lock(ctx context.Context, repo core.RepoID) (release func(), err error)
}

// Trust and enforcement gates for the first prompt in direct-edit mode are the
// existing tasks.Service admission (AG-08, SF-06, SF-09); this package starts
// no task and calls no agent.

// Deps is the aggregate the new-repo task builds the service from. The service
// is bound to the API with api.Server.SetPhase6Services after the server
// exists.
type Deps struct {
	Store     ReservationStore
	FS        SafeInitFS
	Git       InitGit
	Origin    Origin
	Remote    RemoteCreator
	Transport PublishTransport
	Lock      MainLock
	Hub       api.Phase6Hub
	// Clock and IDs are the shell's time and randomness; the core never makes one.
	Clock func() time.Time
	IDs   func(prefix string) string
}

// *db.Store is the live ReservationStore.
var _ ReservationStore = (*db.Store)(nil)
