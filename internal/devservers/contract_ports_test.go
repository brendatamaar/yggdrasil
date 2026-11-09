package devservers_test

import (
	"net"
	"os"
	"path/filepath"
	"testing"

	"github.com/brendatamaar/yggdrasil/internal/devservers"
	"github.com/brendatamaar/yggdrasil/internal/devservers/devserverstest"
	"github.com/brendatamaar/yggdrasil/internal/platform"
)

// ---- Repos: the fake. The live adapter (the tracked-folder resolver over the
// database) runs the same suite in cmd/ygg/phase5_contract_repos_test.go. ----

func TestFakeReposContract(t *testing.T) {
	devserverstest.ReposContract(t, func(t *testing.T) devserverstest.ReposFixture {
		r := devserverstest.NewRepos()
		main := devservers.Target{Repo: "rep_a"}
		wt := devservers.Target{Repo: "rep_a", Worktree: "wt_a"}
		r.Track(main, filepath.Join(t.TempDir(), "shop api"))
		r.Track(wt, filepath.Join(t.TempDir(), "shop api wt"))
		return devserverstest.ReposFixture{
			Repos: r, Main: main, Worktree: wt,
			Unknown:   devservers.Target{Repo: "rep_zz"},
			UnknownWT: devservers.Target{Repo: "rep_a", Worktree: "wt_zz"},
			WrongRepo: devservers.Target{Repo: "rep_b", Worktree: "wt_a"},
			Untrack:   func() { r.Untrack(main); r.Untrack(wt) },
		}
	})
}

// ---- Templates: the fake and the real Catalog (over fixture manifests). ----

func TestFakeTemplatesContract(t *testing.T) {
	devserverstest.TemplatesContract(t, func(t *testing.T) devserverstest.TemplatesFixture {
		tp := &devserverstest.Templates{}
		tp.Set("dev", devserverstest.Command{Argv: []string{"npm", "run", "dev"}, Env: map[string]string{"A": "b"}})
		return devserverstest.TemplatesFixture{
			Templates: tp, Target: devservers.Target{Repo: "rep_a"}, Command: "dev",
			WantArgv: []string{"npm", "run", "dev"}, Unknown: "nope",
		}
	})
}

func TestCatalogTemplatesContract(t *testing.T) {
	devserverstest.TemplatesContract(t, func(t *testing.T) devserverstest.TemplatesFixture {
		dir := filepath.Join(t.TempDir(), "shop web")
		if err := os.MkdirAll(dir, 0o755); err != nil {
			t.Fatal(err)
		}
		if err := os.WriteFile(filepath.Join(dir, "package.json"), []byte(`{"name":"shop-web","scripts":{"dev":"vite"}}`), 0o600); err != nil {
			t.Fatal(err)
		}
		repos := devserverstest.NewRepos()
		tg := devservers.Target{Repo: "rep_a"}
		repos.Track(tg, dir)
		cat := devservers.NewCatalog(repos, devserverstest.NewClock())
		cmds, err := cat.Commands(t.Context(), tg)
		if err != nil || len(cmds) == 0 {
			t.Fatalf("the fixture repo offers no dev command: %v %v", cmds, err)
		}
		other := devservers.Target{Repo: "rep_zz"}
		return devserverstest.TemplatesFixture{
			Templates: cat, Target: tg, Command: "dev", WantArgv: cmds[0].Argv, Unknown: "nope", Untracked: &other,
		}
	})
}

// ---- Blocks: the fake. The live adapter runs the suite in cmd/ygg. ----

func TestFakeBlocksContract(t *testing.T) {
	devserverstest.BlocksContract(t, func(t *testing.T) devserverstest.BlocksFixture {
		return devserverstest.BlocksFixture{
			Blocks: &devserverstest.Blocks{},
			Main:   devservers.Target{Repo: "rep_a"},
			WT1:    devservers.Target{Repo: "rep_a", Worktree: "wt_1"},
			WT2:    devservers.Target{Repo: "rep_a", Worktree: "wt_2"},
		}
	})
}

// ---- Ports: the fake and platform.ListeningPorts. ----

func TestFakePortsContract(t *testing.T) {
	devserverstest.PortsContract(t, func(t *testing.T) devserverstest.PortsHarness {
		f := &devserverstest.PortsFake{}
		return devserverstest.PortsHarness{
			Ports: f.Lister,
			Listen: func(t *testing.T) (int, int, func()) {
				f.Bind(77, 20001)
				return 77, 20001, func() { f.Unbind(77, 20001) }
			},
		}
	})
}

func TestRealPortsContract(t *testing.T) {
	if _, err := platform.ListeningPorts(os.Getpid()); err != nil {
		t.Skip("port detection is implemented on Windows and Linux only")
	}
	devserverstest.PortsContract(t, func(t *testing.T) devserverstest.PortsHarness {
		return devserverstest.PortsHarness{
			Ports: platform.ListeningPorts,
			Listen: func(t *testing.T) (int, int, func()) {
				ln, err := net.Listen("tcp", "127.0.0.1:0")
				if err != nil {
					t.Skip("no loopback listener possible")
				}
				t.Cleanup(func() { _ = ln.Close() })
				return os.Getpid(), ln.Addr().(*net.TCPAddr).Port, func() { _ = ln.Close() }
			},
		}
	})
}
