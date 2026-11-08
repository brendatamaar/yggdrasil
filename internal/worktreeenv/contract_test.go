package worktreeenv_test

import (
	"context"
	"fmt"
	"net"
	"os"
	"path/filepath"
	"sync/atomic"
	"testing"

	"github.com/brendatamaar/yggdrasil/internal/core"
	"github.com/brendatamaar/yggdrasil/internal/db"
	"github.com/brendatamaar/yggdrasil/internal/db/dbtest"
	"github.com/brendatamaar/yggdrasil/internal/safety"
	"github.com/brendatamaar/yggdrasil/internal/worktreeenv"
	"github.com/brendatamaar/yggdrasil/internal/worktreeenv/worktreeenvtest"
)

func TestFakeStoreContract(t *testing.T) {
	worktreeenvtest.StoreContract(t, func(t *testing.T) worktreeenvtest.StoreEnv {
		s := worktreeenvtest.NewStore("rep_1")
		var n atomic.Int32
		return worktreeenvtest.StoreEnv{
			Ports: s, Settings: s, Repo: "rep_1",
			NewWorktree: func() core.WorktreeID { return core.WorktreeID(fmt.Sprintf("wt_%d", n.Add(1))) },
			Audits: func(subject string) int {
				c := 0
				for _, a := range s.Audits() {
					if a.Subject == subject {
						c++
					}
				}
				return c
			},
		}
	})
}

func newDBStore(t *testing.T) *db.Store {
	t.Helper()
	d, err := db.Open(context.Background(), filepath.Join(t.TempDir(), "ygg.db"))
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = d.Close() })
	return db.NewStore(d, safety.NewRedactor().Redact)
}

func TestSQLiteStoreContract(t *testing.T) {
	worktreeenvtest.StoreContract(t, func(t *testing.T) worktreeenvtest.StoreEnv {
		s := newDBStore(t)
		project := dbtest.Project(t, s, core.Project{Name: "acme-shop", Path: t.TempDir()})
		repo := dbtest.Repo(t, s, core.Repo{ProjectID: project.ID, Name: "shop-api", Path: filepath.Join(t.TempDir(), "shop-api")})
		store := worktreeenv.DBStore{S: s}
		var n atomic.Int32
		return worktreeenvtest.StoreEnv{
			Ports: store, Settings: store, Repo: repo.ID,
			NewWorktree: func() core.WorktreeID {
				task := dbtest.Task(t, s, core.Task{ProjectID: project.ID, Scope: core.ScopeRepo, Mode: core.ModeCode, RepoIDs: []core.RepoID{repo.ID}})
				name := fmt.Sprintf("wt%d", n.Add(1))
				return dbtest.TaskWorktree(t, s, task.ID, repo, name, "ygg/"+name).ID
			},
			Audits: func(subject string) int {
				got, err := s.ListAudit(context.Background(), subject, 100)
				if err != nil {
					t.Fatal(err)
				}
				return len(got)
			},
		}
	})
}

func TestFakeFileSystemContract(t *testing.T) {
	worktreeenvtest.FSContract(t, func(t *testing.T) worktreeenvtest.FSEnv {
		f := worktreeenvtest.NewFS()
		return worktreeenvtest.FSEnv{
			FS: f, Src: "/repos/shop-api", Dst: "/wt/amber-fox",
			Write: func(root, rel string, data []byte) { f.WriteFile(root, rel, data) },
			Read:  f.Read,
			Link:  func(root, rel string) bool { f.Link(root, rel); return true },
			Count: f.Count,
		}
	})
}

func TestOSFileSystemContract(t *testing.T) {
	worktreeenvtest.FSContract(t, func(t *testing.T) worktreeenvtest.FSEnv {
		base := t.TempDir()
		src, dst, outside := filepath.Join(base, "src repo"), filepath.Join(base, "dst wt"), filepath.Join(base, "outside")
		for _, d := range []string{src, dst, outside} {
			if err := os.MkdirAll(d, 0o750); err != nil {
				t.Fatal(err)
			}
		}
		if err := os.WriteFile(filepath.Join(outside, "secret.env"), []byte("top"), 0o600); err != nil {
			t.Fatal(err)
		}
		if err := os.WriteFile(filepath.Join(outside, "file.env"), []byte("top"), 0o600); err != nil {
			t.Fatal(err)
		}
		write := func(root, rel string, data []byte) {
			p := filepath.Join(root, filepath.FromSlash(rel))
			if err := os.MkdirAll(filepath.Dir(p), 0o750); err != nil {
				t.Fatal(err)
			}
			if err := os.WriteFile(p, data, 0o600); err != nil {
				t.Fatal(err)
			}
		}
		return worktreeenvtest.FSEnv{
			FS: worktreeenv.OSFileSystem{}, Src: src, Dst: dst,
			Write: write,
			Read: func(root, rel string) ([]byte, bool) {
				b, err := os.ReadFile(filepath.Join(root, filepath.FromSlash(rel)))
				return b, err == nil
			},
			Link: func(root, rel string) bool {
				target := outside
				if filepath.Ext(rel) != "" {
					target = filepath.Join(outside, "file.env")
				}
				return os.Symlink(target, filepath.Join(root, filepath.FromSlash(rel))) == nil
			},
			Count: func() int {
				n := 0
				for _, r := range []string{src, dst} {
					_ = filepath.WalkDir(r, func(string, os.DirEntry, error) error { n++; return nil })
				}
				return n
			},
		}
	})
}

func TestFakeProberContract(t *testing.T) {
	worktreeenvtest.ProberContract(t, func(t *testing.T) worktreeenvtest.ProberEnv {
		p := worktreeenvtest.NewProber()
		return worktreeenvtest.ProberEnv{
			Prober: p,
			Listen: func(*testing.T) int { p.Used[41000] = true; return 41000 },
			Free:   func(*testing.T) int { return 41001 },
		}
	})
}

func TestLoopbackProberContract(t *testing.T) {
	worktreeenvtest.ProberContract(t, func(t *testing.T) worktreeenvtest.ProberEnv {
		return worktreeenvtest.ProberEnv{
			Prober: worktreeenv.LoopbackProber{},
			Listen: func(t *testing.T) int {
				l, err := net.Listen("tcp", "127.0.0.1:0")
				if err != nil {
					t.Skipf("cannot listen on loopback: %v", err)
				}
				t.Cleanup(func() { _ = l.Close() })
				return l.Addr().(*net.TCPAddr).Port
			},
			Free: func(t *testing.T) int {
				l, err := net.Listen("tcp", "127.0.0.1:0")
				if err != nil {
					t.Skipf("cannot listen on loopback: %v", err)
				}
				port := l.Addr().(*net.TCPAddr).Port
				_ = l.Close()
				return port
			},
		}
	})
}

// seedRows makes a repo and a worktree row in the database.
func seedRows(t *testing.T, s *db.Store) (core.RepoID, core.Worktree) {
	t.Helper()
	project := dbtest.Project(t, s, core.Project{Name: "acme-shop", Path: t.TempDir()})
	repo := dbtest.Repo(t, s, core.Repo{ProjectID: project.ID, Name: "shop-api", Path: filepath.Join(t.TempDir(), "shop-api")})
	return repo.ID, dbtest.Worktree(t, s, repo)
}
