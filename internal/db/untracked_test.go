package db

import (
	"context"
	"errors"
	"testing"
	"time"

	"github.com/brendatamaar/yggdrasil/internal/core"
)

// The contract for repo tracking (SF-02): a row with untracked_at set is
// invisible to every read the API lists and task creation use. A new read on
// projects or repos that is used there must be added to this test.
func TestUntrackedRowsAreInvisibleToListsAndGets(t *testing.T) {
	d := openTest(t)
	s := NewStore(d, nil)
	ctx := context.Background()
	p, r := seed(t, s)
	other, err := s.CreateRepo(ctx, core.Repo{
		ID: "r-other", ProjectID: p.ID, Name: "shop-web", Path: p.Path + "/shop-web", CreatedAt: time.Unix(1, 0),
	})
	if err != nil {
		t.Fatal(err)
	}

	// Visible while tracked.
	if _, err := s.GetProject(ctx, p.ID); err != nil {
		t.Fatal(err)
	}
	if got, err := s.ListRepos(ctx, p.ID); err != nil || len(got) != 2 {
		t.Fatalf("ListRepos = %v, %v", got, err)
	}

	untrack := func(table, id string) {
		t.Helper()
		if _, err := d.write.ExecContext(ctx, `UPDATE `+table+` SET untracked_at = '2026-02-01T00:00:00.000Z' WHERE id = ?`, id); err != nil {
			t.Fatal(err)
		}
	}

	untrack("repos", string(r.ID))
	if _, err := s.GetRepo(ctx, r.ID); !errors.Is(err, ErrNotFound) {
		t.Errorf("GetRepo of an untracked repo = %v, want ErrNotFound", err)
	}
	got, err := s.ListRepos(ctx, p.ID)
	if err != nil || len(got) != 1 || got[0].ID != other.ID {
		t.Errorf("ListRepos after untracking one repo = %v, %v", got, err)
	}
	// History views can still read it.
	if hist, err := s.GetRepoIncludingUntracked(ctx, r.ID); err != nil || hist.ID != r.ID {
		t.Errorf("GetRepoIncludingUntracked = %+v, %v", hist, err)
	}
	if _, err := s.GetRepoIncludingUntracked(ctx, "missing"); !errors.Is(err, ErrNotFound) {
		t.Errorf("GetRepoIncludingUntracked of a missing repo = %v, want ErrNotFound", err)
	}

	untrack("projects", string(p.ID))
	if _, err := s.GetProject(ctx, p.ID); !errors.Is(err, ErrNotFound) {
		t.Errorf("GetProject of an untracked project = %v, want ErrNotFound", err)
	}
	if ps, err := s.ListProjects(ctx); err != nil || len(ps) != 0 {
		t.Errorf("ListProjects after untracking = %v, %v", ps, err)
	}
}

func TestCreateRepoDerivesRelPath(t *testing.T) {
	d := openTest(t)
	s := NewStore(d, nil)
	ctx := context.Background()
	p, _ := seed(t, s)
	_, err := s.CreateRepo(ctx, core.Repo{ID: "r-deep", ProjectID: p.ID, Name: "deep", Path: p.Path + "/services/deep", CreatedAt: time.Unix(1, 0)})
	if err != nil {
		t.Fatal(err)
	}
	var rel string
	if err := d.write.QueryRowContext(ctx, `SELECT rel_path FROM repos WHERE id = 'r-deep'`).Scan(&rel); err != nil || rel != "services/deep" {
		t.Fatalf("rel_path = %q, %v", rel, err)
	}
	// Two repos in one folder collide on (project_id, rel_path).
	_, err = s.CreateRepo(ctx, core.Repo{ID: "r-dup", ProjectID: p.ID, Name: "deep2", Path: p.Path + "/services/deep", CreatedAt: time.Unix(1, 0)})
	if !errors.Is(err, ErrConflict) {
		t.Fatalf("second repo in the same folder = %v, want ErrConflict", err)
	}
}

func TestRelPathOf(t *testing.T) {
	cases := []struct{ project, repo, name, want string }{
		{"/work/acme", "/work/acme/shop-api", "x", "shop-api"},
		{"/work/acme", "/work/acme", "x", "."},
		{"/work/acme/", "/work/acme/a/b/", "x", "a/b"},
		{`C:\Work\Acme`, `c:\work\acme\Shop`, "x", "Shop"},
		{"/work/acme", "/work/acme-other/x", "x", "x"}, // a sibling with the same prefix is outside
		{"/work/acme", "/elsewhere", "name", "name"},
		{"/", "/top", "x", "top"},
	}
	for _, c := range cases {
		if got := relPathOf(c.project, c.repo, c.name); got != c.want {
			t.Errorf("relPathOf(%q, %q) = %q, want %q", c.project, c.repo, got, c.want)
		}
	}
}

func TestSettings(t *testing.T) {
	s := NewStore(openTest(t), nil)
	ctx := context.Background()
	if _, err := s.GetSetting(ctx, "editor"); !errors.Is(err, ErrNotFound) {
		t.Fatalf("missing setting = %v, want ErrNotFound", err)
	}
	set := func(value string, version int64) (int64, error) {
		var v int64
		err := s.Commit(ctx, func(tx *Tx) (err error) {
			v, err = tx.SetSetting(ctx, "editor", value, version)
			return err
		})
		return v, err
	}
	if v, err := set("code", SettingNew); err != nil || v != 0 {
		t.Fatalf("create = %d, %v", v, err)
	}
	if _, err := set("zed", SettingNew); !errors.Is(err, ErrConflict) {
		t.Fatalf("create twice = %v, want ErrConflict", err)
	}
	if v, err := set("zed", 0); err != nil || v != 1 {
		t.Fatalf("update = %d, %v", v, err)
	}
	if _, err := set("vim", 0); !errors.Is(err, ErrConflict) {
		t.Fatalf("stale update = %v, want ErrConflict", err)
	}
	got, err := s.GetSetting(ctx, "editor")
	if err != nil || got.Value != "zed" || got.Version != 1 {
		t.Fatalf("setting = %+v, %v", got, err)
	}
	err = s.Commit(ctx, func(tx *Tx) error {
		_, err := tx.SetSetting(ctx, "other", "x", 0)
		return err
	})
	if !errors.Is(err, ErrNotFound) {
		t.Fatalf("update of a missing key = %v, want ErrNotFound", err)
	}
	err = s.Commit(ctx, func(tx *Tx) error {
		_, err := tx.SetSetting(ctx, "bad key", "x", SettingNew)
		return err
	})
	if err == nil {
		t.Fatal("key with a space accepted")
	}
}
