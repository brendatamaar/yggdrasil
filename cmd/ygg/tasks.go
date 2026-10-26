package main

import (
	"context"
	"fmt"
	"log/slog"
	"path/filepath"
	"strings"
	"time"

	"github.com/brendatamaar/yggdrasil/internal/agents"
	"github.com/brendatamaar/yggdrasil/internal/api"
	"github.com/brendatamaar/yggdrasil/internal/core"
	"github.com/brendatamaar/yggdrasil/internal/db"
	"github.com/brendatamaar/yggdrasil/internal/git"
	"github.com/brendatamaar/yggdrasil/internal/mainlock"
	"github.com/brendatamaar/yggdrasil/internal/platform"
	"github.com/brendatamaar/yggdrasil/internal/safety"
	"github.com/brendatamaar/yggdrasil/internal/tasks"
	"github.com/brendatamaar/yggdrasil/internal/worktrees"
)

// adapterRegistry finds an adapter by the name a task stores.
type adapterRegistry map[string]agents.Adapter

func (r adapterRegistry) Get(name string) (agents.Adapter, bool) {
	a, ok := r[name]
	return a, ok
}

// taskLock adapts the shared main-checkout registry to tasks.MainLock and
// tasks.LockProber. Owners are strings "<kind>:<id>" ("task:tsk_...").
type taskLock struct{ reg *mainlock.Registry }

func (l taskLock) TryAcquire(repos []core.RepoID, owner string) (tasks.Release, string, bool) {
	kind, id, _ := strings.Cut(owner, ":")
	rel, h, ok := l.reg.TryAcquire(repos, mainlock.Owner{Kind: mainlock.OwnerKind(kind), ID: id})
	if !ok {
		return nil, holderString(h), false
	}
	return rel, "", true
}

// Held says who holds the first of repos, without taking anything.
func (l taskLock) Held(repos []core.RepoID) (string, bool) {
	if len(repos) == 0 {
		return "", false
	}
	h, ok := l.reg.Holder(repos[0])
	if !ok {
		return "", false
	}
	return holderString(h), true
}

func holderString(h mainlock.Holder) string { return string(h.Owner.Kind) + ":" + h.Owner.ID }

// newTasks builds the tasks service. hub is bound to the server after the
// server exists.
func newTasks(store *db.Store, adapters adapterRegistry, g *git.Git, hub *api.HubAdapter, redactor safety.Redactor, dataDir string, log *slog.Logger) (*tasks.Service, error) {
	now := func() time.Time { return time.Now().UTC() }
	ens, err := worktrees.New(worktrees.Config{
		Git: g, Store: worktrees.DBStore{S: store}, Rand: tasks.Rand{},
		Now: now, NewID: db.NewID, DataDir: filepath.Join(dataDir, "wt"),
	})
	if err != nil {
		return nil, fmt.Errorf("prepare worktrees: %w", err)
	}
	svc, err := tasks.New(tasks.Config{
		Store: store, Adapters: adapters, Worktrees: ens, MainLock: taskLock{reg: &mainlock.Registry{}},
		Scanner: safety.FSScanner{}, Snapshotter: g, Env: safety.NewEnvBuilder(), Redactor: redactor,
		Hub: hub, Machine: platform.Machine{}, Killer: platform.KillTask,
		Now: now, NewID: db.NewID, Log: log,
		Allowlist: newAllowlist(store, log),
		// Proposals arrive with their service; until then none are pending.
		PendingProposals: func(context.Context, core.TaskID) (int, error) { return 0, nil },
	})
	if err != nil {
		return nil, fmt.Errorf("start tasks: %w", err)
	}
	return svc, nil
}
