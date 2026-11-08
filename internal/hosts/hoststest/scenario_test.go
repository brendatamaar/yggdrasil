package hoststest_test

import (
	"bytes"
	"context"
	"os"
	"path/filepath"
	"testing"

	"github.com/brendatamaar/yggdrasil/internal/builds/toolchains/maven"
	"github.com/brendatamaar/yggdrasil/internal/builds/toolchains/node"
	"github.com/brendatamaar/yggdrasil/internal/core"
	corebuilds "github.com/brendatamaar/yggdrasil/internal/core/builds"
	"github.com/brendatamaar/yggdrasil/internal/platform"
	"github.com/brendatamaar/yggdrasil/internal/safety"
)

// generateMultiRepo runs the demo generator's multirepo scenario. The generator
// is its own module, so it runs through platform.Start like any child process.
func generateMultiRepo(t *testing.T) string {
	t.Helper()
	if testing.Short() {
		t.Skip("runs the demo generator")
	}
	env, err := safety.NewEnvBuilder().Build(safety.EnvOptions{})
	if err != nil {
		t.Fatal(err)
	}
	goExe, err := platform.Resolve("go", "", env)
	if err != nil {
		t.Skipf("go is not installed: %v", err)
	}
	gen, err := filepath.Abs(filepath.Join("..", "..", "..", "fixtures", "demo-gen"))
	if err != nil {
		t.Fatal(err)
	}
	out := filepath.Join(t.TempDir(), "with space", "demo-project")
	var buf bytes.Buffer
	p, err := platform.Start(context.Background(), platform.Spec{
		Argv: []string{goExe, "run", ".", "-out", out, "-scenario", "multirepo"},
		Dir:  gen, Env: env, Stdout: &buf, Stderr: &buf,
	})
	if err != nil {
		t.Fatal(err)
	}
	if code, err := p.Wait(); err != nil || code != 0 {
		t.Fatalf("demo-gen exit %d, %v:\n%s", code, err, buf.String())
	}
	return filepath.Join(out, "acme-shop-platform")
}

// The Maven toolchain detects both Java repos, its Orderer reads the
// dependency, and OrderModules puts shop-core before shop-checkout; the Node
// toolchain detects the storefront as a pnpm repo.
func TestScenarioMultiRepoIsDetectedByToolchains(t *testing.T) {
	project := generateMultiRepo(t)
	mvn := maven.New()
	var mods []corebuilds.Module
	for _, name := range []string{"shop-checkout", "shop-core"} { // deliberately not in build order
		repo := os.DirFS(filepath.Join(project, name))
		det, ok := mvn.Detect(repo)
		if !ok || det.Runner != "mvnw" {
			t.Fatalf("%s: maven Detect = %+v, %v", name, det, ok)
		}
		ms, err := mvn.Modules(repo, det)
		if err != nil || len(ms) != 1 {
			t.Fatalf("%s: Modules = %+v, %v", name, ms, err)
		}
		ms[0].RepoID = core.RepoID(name)
		mods = append(mods, ms[0])
	}
	order, cycles := corebuilds.OrderModules(mods)
	if len(cycles) != 0 || len(order) != 2 || order[0] != "shop-core" || order[1] != "shop-checkout" {
		t.Fatalf("order = %v, cycles = %v; want [shop-core shop-checkout]", order, cycles)
	}

	det, ok := node.New().Detect(os.DirFS(filepath.Join(project, "shop-storefront")))
	if !ok || det.Runner != "pnpm" || det.Facts["locked"] != "true" {
		t.Fatalf("node Detect = %+v, %v", det, ok)
	}
	if _, ok := mvn.Detect(os.DirFS(filepath.Join(project, "shop-storefront"))); ok {
		t.Error("maven claims the Node repo")
	}
}
