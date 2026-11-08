package db

import (
	"context"
	"errors"
	"reflect"
	"testing"
	"time"

	"github.com/brendatamaar/yggdrasil/internal/core"
	"github.com/brendatamaar/yggdrasil/internal/core/devservers"
)

var dsTime = time.Date(2026, 5, 6, 7, 8, 9, 0, time.UTC)

func TestDevServerSaveLoadEveryState(t *testing.T) {
	ctx := context.Background()
	s := NewStore(openTest(t), nil)
	_, repo := seed(t, s)
	key := devservers.Key{Repo: repo.ID, CommandKey: "dev"}
	b := core.PortBlock{Base: 20000, Size: 20}
	states := []devservers.Server{
		devservers.Stopped{},
		devservers.Stopped{Attempt: 1, Last: devservers.LastExited, Code: 2, At: dsTime},
		devservers.Starting{Attempt: 2, Since: dsTime, Block: b},
		devservers.Starting{Attempt: 2, Since: dsTime, Pid: 4},
		devservers.Running{Attempt: 2, Since: dsTime, Pid: 4, Block: b, Ports: []devservers.Port{3000, 20000}, Announced: []devservers.Port{20000}},
		devservers.Stopping{Attempt: 2, Cause: devservers.CauseRestart, KillAttempt: devservers.KillSecond, Since: dsTime, Pid: 4, Block: b},
		devservers.Failed{Attempt: 2, Kind: devservers.FailCrashed, Code: 9, At: dsTime},
		devservers.Orphaned{Attempt: 2, Pid: 4, SeenAt: dsTime},
	}
	version := NewDevServer
	for _, st := range states {
		row := devservers.RowOf(key, st)
		err := s.Commit(ctx, func(tx *Tx) error {
			v, err := tx.SaveDevServer(ctx, "dsv_1", row, version, dsTime)
			version = v
			return err
		})
		if err != nil {
			t.Fatalf("%#v: %v", st, err)
		}
		got, err := s.GetDevServer(ctx, "dsv_1")
		if err != nil {
			t.Fatal(err)
		}
		back, err := devservers.ServerFromRow(got.Row)
		if err != nil {
			t.Fatalf("%#v: %v", st, err)
		}
		if got.Version != version || !reflect.DeepEqual(back, devservers.Persisted(st)) {
			t.Errorf("stored %#v (version %d), got %#v (version %d)", st, version, back, got.Version)
		}
	}
}

func TestDevServerVersionConflictAndMissing(t *testing.T) {
	ctx := context.Background()
	s := NewStore(openTest(t), nil)
	_, repo := seed(t, s)
	row := devservers.RowOf(devservers.Key{Repo: repo.ID, CommandKey: "dev"}, devservers.Stopped{})
	save := func(expected int64) error {
		return s.Commit(ctx, func(tx *Tx) error {
			_, err := tx.SaveDevServer(ctx, "dsv_1", row, expected, dsTime)
			return err
		})
	}
	if err := save(NewDevServer); err != nil {
		t.Fatal(err)
	}
	if err := save(NewDevServer); !errors.Is(err, ErrConflict) {
		t.Errorf("second insert = %v, want ErrConflict", err)
	}
	if err := save(0); err != nil {
		t.Fatal(err)
	}
	if err := save(0); !errors.Is(err, ErrConflict) {
		t.Errorf("stale version = %v, want ErrConflict", err)
	}
	if err := s.Commit(ctx, func(tx *Tx) error {
		_, err := tx.SaveDevServer(ctx, "dsv_none", row, 5, dsTime)
		return err
	}); !errors.Is(err, ErrNotFound) {
		t.Errorf("missing = %v, want ErrNotFound", err)
	}
	if _, err := s.GetDevServer(ctx, "dsv_none"); !errors.Is(err, ErrNotFound) {
		t.Errorf("get missing = %v", err)
	}
	bad := row
	bad.Key.Repo = "rep_nope"
	if err := s.Commit(ctx, func(tx *Tx) error {
		_, err := tx.SaveDevServer(ctx, "dsv_2", bad, NewDevServer, dsTime)
		return err
	}); !errors.Is(err, ErrNotFound) {
		t.Errorf("unknown repo = %v, want ErrNotFound", err)
	}
}

func TestDevServerLiveListAndConstraints(t *testing.T) {
	ctx := context.Background()
	s := NewStore(openTest(t), nil)
	_, repo := seed(t, s)
	servers := map[string]devservers.Server{
		"a": devservers.Stopped{},
		"b": devservers.Starting{Attempt: 1, Since: dsTime},
		"c": devservers.Running{Attempt: 1, Since: dsTime, Pid: 3},
		"d": devservers.Failed{Attempt: 1, Kind: devservers.FailStartFailed, At: dsTime},
		"e": devservers.Orphaned{Attempt: 1, Pid: 3, SeenAt: dsTime},
		"f": devservers.Stopping{Attempt: 1, Cause: devservers.CauseUserStop, KillAttempt: 1, Since: dsTime, Pid: 3},
	}
	for id, st := range servers {
		key := devservers.Key{Repo: repo.ID, CommandKey: id}
		if err := s.Commit(ctx, func(tx *Tx) error {
			_, err := tx.SaveDevServer(ctx, "dsv_"+id, devservers.RowOf(key, st), NewDevServer, dsTime)
			return err
		}); err != nil {
			t.Fatal(err)
		}
	}
	live, err := s.ListLiveDevServers(ctx)
	if err != nil {
		t.Fatal(err)
	}
	var ids []string
	for _, r := range live {
		ids = append(ids, r.ID)
	}
	if !reflect.DeepEqual(ids, []string{"dsv_b", "dsv_c", "dsv_e", "dsv_f"}) {
		t.Errorf("live = %v", ids)
	}
	if all, err := s.ListDevServersByRepo(ctx, repo.ID); err != nil || len(all) != 6 {
		t.Errorf("by repo = %d, %v", len(all), err)
	}
	// The table refuses a running row without a pid (a corrupt write).
	bad := devservers.RowOf(devservers.Key{Repo: repo.ID, CommandKey: "x"}, devservers.Running{Attempt: 1, Since: dsTime, Pid: 3})
	bad.Pid = 0
	if err := s.Commit(ctx, func(tx *Tx) error {
		_, err := tx.SaveDevServer(ctx, "dsv_x", bad, NewDevServer, dsTime)
		return err
	}); err == nil {
		t.Error("a running row without a pid was stored")
	}
	if err := s.Commit(ctx, func(tx *Tx) error {
		_, err := tx.SaveDevServer(ctx, "dsv_x", bad, NewDevServer, time.Time{})
		return err
	}); err == nil {
		t.Error("a zero write time was accepted")
	}
}
