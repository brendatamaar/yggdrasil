package devservers_test

import (
	"context"
	"errors"
	"fmt"
	"go/parser"
	"go/token"
	"os"
	"path/filepath"
	"slices"
	"strings"
	"testing"

	"github.com/brendatamaar/yggdrasil/internal/devservers/devserverstest"
	"github.com/brendatamaar/yggdrasil/internal/platform/platformtest"
)

// tokenEnv is a host environment full of credentials. None of it may reach a
// dev server's child process.
var tokenEnv = map[string]string{
	"GITHUB_TOKEN":          "ghp_aaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaa",
	"GITLAB_TOKEN":          "glpat-bbbbbbbbbbbbbbbbbbbb",
	"YGG_API_KEY":           "sk-cccccccccccccccccccc",
	"AWS_SECRET_ACCESS_KEY": "dddddddddddddddddddd",
	"NPM_TOKEN":             "eeeeeeeeeeeeeeeeeeee",
	"ANTHROPIC_API_KEY":     "sk-ant-ffffffffffffffffffff",
	"VIRTUAL_ENV":           "/venv-leak",
	"PYTHONPATH":            "/pp-leak",
	"CONDA_PREFIX":          "/conda-leak",
}

// Invariant 3: the dev server process gets the clean environment. The host's
// tokens are not in it, whatever the template or the port block adds.
func TestInvariant_3_dev_server_child_gets_no_token_vars(t *testing.T) {
	for k, v := range tokenEnv {
		t.Setenv(k, v)
	}
	r := newRig(t)
	r.tpls.Set("dev", devserverstest.Command{Argv: []string{"fake", "block"}, Env: map[string]string{"NODE_ENV": "development"}})
	r.toRunning(20001)
	specs := r.fake.Started()
	if len(specs) != 1 {
		t.Fatalf("started %d processes", len(specs))
	}
	if specs[0].Env == nil {
		t.Fatal("the process got a nil environment (it would inherit the host's)")
	}
	env := strings.Join(specs[0].Env, "\n")
	for k, v := range tokenEnv {
		if strings.Contains(env, v) || strings.Contains(strings.ToUpper(env), k+"=") {
			t.Errorf("%s reached the dev server", k)
		}
	}
	if !strings.Contains(env, "NODE_ENV=development") || !strings.Contains(env, "PORT=20000") {
		t.Errorf("the clean environment lost the template or port variables:\n%s", env)
	}
}

// Invariant 3: a template (a repo override or a toolchain default) cannot put a
// token-like variable into the child. The start is refused and no process runs.
func TestInvariant_3_template_env_with_a_token_name_never_starts(t *testing.T) {
	for _, name := range []string{"GITHUB_TOKEN", "NPM_TOKEN", "DB_PASSWORD", "STRIPE_KEY", "app_secret"} {
		r := newRig(t)
		r.tpls.Set("dev", devserverstest.Command{Argv: []string{"fake", "block"}, Env: map[string]string{name: "x"}})
		if _, err := r.svc.Start(context.Background(), r.target, "dev"); err == nil {
			t.Errorf("%s: the start was accepted", name)
		}
		if n := len(r.fake.Started()); n != 0 {
			t.Errorf("%s: %d processes started", name, n)
		}
	}
}

// Invariant 4: a token that a failing start, a log line or an audit entry
// could carry is redacted everywhere it is stored or published: the view, the
// hub, the stored rows and their audit entries.
func TestInvariant_4_dev_server_surfaces_hold_no_token(t *testing.T) {
	const tok = "ghp_abcdefghijklmnopqrstuvwxyz0123456789"
	r := newRig(t)
	r.resolve = func(string, string, []string) (string, error) {
		return "", errors.New("cannot run with " + tok + " in " + r.dir)
	}
	v, err := r.svc.Start(context.Background(), r.target, "dev")
	if err != nil {
		t.Fatal(err)
	}
	failed := r.waitStatus("failed")
	all := []string{failed.Message, strings.Join(r.hub.Statuses(), "\n")}
	for _, e := range r.store.Audit("") {
		all = append(all, string(e.Detail), e.Subject, e.Action)
	}
	row, err := r.store.Get(context.Background(), v.ID)
	if err != nil {
		t.Fatal(err)
	}
	all = append(all, fmt.Sprintf("%+v", row))
	joined := strings.Join(all, "\n")
	if strings.Contains(joined, tok) {
		t.Errorf("the token is on a dev server surface:\n%s", joined)
	}
	if strings.Contains(joined, r.dir) {
		t.Errorf("the folder path is on a dev server surface (audit and views carry IDs):\n%s", joined)
	}
}

// Invariant 4: a token in a process's stderr is redacted too, not just stdout.
func TestInvariant_4_stderr_is_redacted(t *testing.T) {
	const tok = "glpat-abcdefghijklmnopqrstu"
	r := newRig(t)
	r.fake.Push(platformtest.Script{Stderr: "auth failed for " + tok + "\n", Exit: 1})
	v, err := r.svc.Start(context.Background(), r.target, "dev")
	if err != nil {
		t.Fatal(err)
	}
	r.waitStatus("failed")
	all := strings.Join(logTexts(r.svc, r.target, -1), "\n")
	for _, l := range r.hub.LinesOf(v.ID) {
		all += "\n" + l.Line
	}
	data, err := os.ReadFile(filepath.Join(r.dataDir, "logs", "devservers", v.ID+".log"))
	if err != nil {
		t.Fatal(err)
	}
	all += string(data)
	if strings.Contains(all, tok) {
		t.Errorf("stderr leaked the token:\n%s", all)
	}
	if !strings.Contains(all, "auth failed for") {
		t.Errorf("the line was lost, not redacted:\n%s", all)
	}
}

// Invariant 6: dev servers (and the terminal) are for the human's UI. Nothing
// reachable from internal/mcp or internal/agents imports them, directly or
// through another package.
func TestInvariant_6_dev_servers_and_terminal_are_unreachable_from_mcp_and_agents(t *testing.T) {
	const mod = "github.com/brendatamaar/yggdrasil/"
	root := filepath.Join("..", "..")
	banned := []string{"internal/devservers", "internal/terminal", "internal/core/devservers", "internal/core/terminal"}
	for _, start := range []string{"internal/mcp", "internal/agents"} {
		closure, checked := importClosure(t, root, mod, start)
		if checked < 3 {
			t.Fatalf("only %d files under %s were read", checked, start)
		}
		for _, b := range banned {
			for pkg, via := range closure {
				if pkg == b || strings.HasPrefix(pkg, b+"/") {
					t.Errorf("%s reaches %s (through %s)", start, pkg, via)
				}
			}
		}
	}
	// The scanner itself must be able to see an import: it finds a known one.
	if c, _ := importClosure(t, root, mod, "internal/mcp"); !hasPrefix(c, "internal/core") {
		t.Fatal("the import scanner found nothing under internal/mcp; the check proves nothing")
	}
}

func hasPrefix(m map[string]string, p string) bool {
	for k := range m {
		if k == p || strings.HasPrefix(k, p+"/") {
			return true
		}
	}
	return false
}

// importClosure returns every module package (as "internal/...") that start
// imports directly or transitively, mapped to the package that imports it,
// reading non-test files of every build tag.
func importClosure(t *testing.T, root, mod, start string) (map[string]string, int) {
	t.Helper()
	seen := map[string]string{}
	var files int
	queue := []string{start}
	done := map[string]bool{}
	for len(queue) > 0 {
		pkg := queue[0]
		queue = queue[1:]
		if done[pkg] {
			continue
		}
		done[pkg] = true
		dirs := []string{filepath.Join(root, filepath.FromSlash(pkg))}
		if pkg == start { // the start folder includes its subfolders
			_ = filepath.WalkDir(dirs[0], func(p string, d os.DirEntry, err error) error {
				if err == nil && d.IsDir() && p != dirs[0] {
					dirs = append(dirs, p)
				}
				return nil
			})
		}
		for _, dir := range dirs {
			entries, err := os.ReadDir(dir)
			if err != nil {
				continue
			}
			for _, e := range entries {
				name := e.Name()
				if e.IsDir() || !strings.HasSuffix(name, ".go") || strings.HasSuffix(name, "_test.go") {
					continue
				}
				f, err := parser.ParseFile(token.NewFileSet(), filepath.Join(dir, name), nil, parser.ImportsOnly)
				if err != nil {
					t.Fatal(err)
				}
				files++
				rel, _ := filepath.Rel(root, dir)
				from := filepath.ToSlash(rel)
				for _, imp := range f.Imports {
					p, ok := strings.CutPrefix(strings.Trim(imp.Path.Value, `"`), mod)
					if !ok {
						continue
					}
					if _, dup := seen[p]; !dup {
						seen[p] = from
					}
					if !slices.Contains(queue, p) {
						queue = append(queue, p)
					}
				}
			}
		}
	}
	return seen, files
}
