package db

import (
	"context"
	"database/sql"
	"encoding/json"
	"errors"
	"fmt"
	"time"

	"github.com/brendatamaar/yggdrasil/internal/core"
	"github.com/brendatamaar/yggdrasil/internal/core/devservers"
	"github.com/brendatamaar/yggdrasil/internal/db/gen"
)

// NewDevServer is the expected version SaveDevServer takes to insert a server
// that does not exist yet. Stored versions start at 0.
const NewDevServer int64 = -1

// DevServerRow is a dev_servers row. This package does not interpret it:
// internal/devservers parses Row into a core Server (devservers.ServerFromRow)
// and rejects inconsistent rows.
type DevServerRow struct {
	ID      string
	Row     devservers.Row
	Version int64
}

func devServerFrom(r gen.DevServer) (DevServerRow, error) {
	var ports, announced []devservers.Port
	if err := json.Unmarshal([]byte(r.Ports), &ports); err != nil {
		return DevServerRow{}, fmt.Errorf("dev server %s: bad ports: %w", r.ID, err)
	}
	if err := json.Unmarshal([]byte(r.Announced), &announced); err != nil {
		return DevServerRow{}, fmt.Errorf("dev server %s: bad announced ports: %w", r.ID, err)
	}
	since, err := parseNullTime(r.Since)
	if err != nil {
		return DevServerRow{}, fmt.Errorf("dev server %s: %w", r.ID, err)
	}
	row := devservers.Row{
		Key: devservers.Key{
			Repo: core.RepoID(r.RepoID), Worktree: core.WorktreeID(r.WorktreeID.String), CommandKey: r.CommandKey,
		},
		Status: r.Status, Attempt: int(r.Attempt), Pid: int(r.Pid.Int64), Ports: ports, Announced: announced,
		Block:    core.PortBlock{Base: int(r.BlockBase), Size: int(r.BlockSize)},
		LastKind: devservers.LastKind(r.LastKind), FailKind: devservers.FailKind(r.FailKind.String),
		ExitCode: int(r.ExitCode), StopCause: devservers.StopCause(r.StopCause.String),
		KillAttempt: devservers.KillAttempt(r.KillAttempt),
	}
	if since != nil {
		row.Since = *since
	}
	return DevServerRow{ID: r.ID, Row: row, Version: r.Version}, nil
}

func devServersFrom(rows []gen.DevServer) ([]DevServerRow, error) {
	out := make([]DevServerRow, 0, len(rows))
	for _, r := range rows {
		v, err := devServerFrom(r)
		if err != nil {
			return nil, err
		}
		out = append(out, v)
	}
	return out, nil
}

// GetDevServer returns one dev server row, or ErrNotFound.
func (s *Store) GetDevServer(ctx context.Context, id string) (DevServerRow, error) {
	r, err := s.db.reader().GetDevServer(ctx, id)
	if err := found("get dev server "+id, err); err != nil {
		return DevServerRow{}, err
	}
	return devServerFrom(r)
}

// ListDevServersByRepo returns the dev servers of a repo (its main checkout
// and worktrees), by ID.
func (s *Store) ListDevServersByRepo(ctx context.Context, repo core.RepoID) ([]DevServerRow, error) {
	rows, err := s.db.reader().ListDevServersByRepo(ctx, string(repo))
	if err != nil {
		return nil, fmt.Errorf("list dev servers of %s: %w", repo, err)
	}
	return devServersFrom(rows)
}

// ListLiveDevServers returns exactly the starting, running, stopping and
// orphaned servers.
func (s *Store) ListLiveDevServers(ctx context.Context) ([]DevServerRow, error) {
	rows, err := s.db.reader().ListLiveDevServers(ctx)
	if err != nil {
		return nil, fmt.Errorf("list live dev servers: %w", err)
	}
	return devServersFrom(rows)
}

// LoadDevServer is GetDevServer inside a write transaction.
func (tx *Tx) LoadDevServer(ctx context.Context, id string) (DevServerRow, error) {
	r, err := tx.q.GetDevServer(ctx, id)
	if err := found("load dev server "+id, err); err != nil {
		return DevServerRow{}, err
	}
	return devServerFrom(r)
}

func nullPid(pid int) sql.NullInt64 {
	return sql.NullInt64{Int64: int64(pid), Valid: pid != 0}
}


// SaveDevServer inserts (expected == NewDevServer; ErrConflict when the ID
// exists, ErrNotFound when the repo or worktree does not) or updates a server
// that is still at expected (ErrConflict if the version moved, ErrNotFound if
// it is gone). now is the write time. It returns the new version (0 for an
// insert). Call it with AppendAudit in the same Store.Commit (invariant 8).
func (tx *Tx) SaveDevServer(ctx context.Context, id string, row devservers.Row, expected int64, now time.Time) (int64, error) {
	if err := needTime("dev server", now); err != nil {
		return 0, err
	}
	if id == "" {
		return 0, errors.New("save dev server: the server has no ID")
	}
	ports, err := json.Marshal(nonNilPorts(row.Ports))
	if err != nil {
		return 0, fmt.Errorf("save dev server %s: %w", id, err)
	}
	announced, err := json.Marshal(nonNilPorts(row.Announced))
	if err != nil {
		return 0, fmt.Errorf("save dev server %s: %w", id, err)
	}
	var since *time.Time
	if !row.Since.IsZero() {
		since = &row.Since
	}
	what := "save dev server " + id
	last := row.LastKind
	if last == "" {
		last = devservers.LastNever
	}
	if expected == NewDevServer {
		err := tx.q.InsertDevServer(ctx, gen.InsertDevServerParams{
			ID: id, RepoID: string(row.Key.Repo), WorktreeID: nullText(string(row.Key.Worktree)), CommandKey: row.Key.CommandKey,
			Status: row.Status, Attempt: int64(row.Attempt), Pid: nullPid(row.Pid), Ports: string(ports), Announced: string(announced),
			BlockBase: int64(row.Block.Base), BlockSize: int64(row.Block.Size), LastKind: string(last),
			FailKind: nullText(string(row.FailKind)), ExitCode: int64(row.ExitCode), StopCause: nullText(string(row.StopCause)),
			KillAttempt: int64(row.KillAttempt), Since: fmtNullTime(since), UpdatedAt: fmtTime(now),
		})
		if err != nil {
			return 0, fmt.Errorf("%s: %w", what, err)
		}
		return 0, nil
	}
	n, err := tx.q.UpdateDevServer(ctx, gen.UpdateDevServerParams{
		Status: row.Status, Attempt: int64(row.Attempt), Pid: nullPid(row.Pid), Ports: string(ports), Announced: string(announced),
		BlockBase: int64(row.Block.Base), BlockSize: int64(row.Block.Size), LastKind: string(last),
		FailKind: nullText(string(row.FailKind)), ExitCode: int64(row.ExitCode), StopCause: nullText(string(row.StopCause)),
		KillAttempt: int64(row.KillAttempt), Since: fmtNullTime(since), UpdatedAt: fmtTime(now), ID: id, Version: expected,
	})
	return bumped(what, n, err, expected, func() error {
		_, err := tx.q.GetDevServer(ctx, id)
		return err
	})
}

func nonNilPorts(p []devservers.Port) []devservers.Port {
	if p == nil {
		return []devservers.Port{}
	}
	return p
}
