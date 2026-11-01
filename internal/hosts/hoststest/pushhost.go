package hoststest

import (
	"bytes"
	"context"
	"errors"
	"os"
	"path/filepath"
	"strconv"
	"strings"
	"sync"
	"testing"

	"github.com/brendatamaar/yggdrasil/internal/platform"
	"github.com/brendatamaar/yggdrasil/internal/safety"
)

// LinkMarker is the token a merge request description template carries where
// Yggdrasil fills in links to the other merge requests of a change set
// (docs/design/changesets.md).
const LinkMarker = "<!-- ygg:links -->"

// pushLog and mrCount are files inside the bare remote that the post-receive
// hook writes. The hook is plain POSIX sh, so git's bundled sh runs it on
// Windows too.
const (
	pushLog = "ygg-push-log"
	mrCount = "ygg-mr-count"
)

const hookTemplate = `#!/bin/sh
# Test host hook: records push options and prints a fake "View merge request" line.
base='@BASE@'
log=@LOG@
count=@COUNT@
create=0
{
echo PUSH
n=${GIT_PUSH_OPTION_COUNT:-0}
i=0
while [ "$i" -lt "$n" ]; do
  eval "v=\${GIT_PUSH_OPTION_$i}"
  printf 'OPT %s\n' "$v"
  case "$v" in merge_request.create) create=1;; esac
  i=$((i+1))
done
} >> "$log"
while read -r old new ref; do
  printf 'REF %s\n' "$ref" >> "$log"
  case "$new" in *[!0]*) ;; *) continue;; esac
  case "$ref" in refs/heads/*) ;; *) continue;; esac
  if [ "$create" = 1 ]; then
    branch=${ref#refs/heads/}
    num=0
    [ -f "$count" ] && read -r num < "$count"
    num=$((num+1))
    echo "$num" > "$count"
    echo "View merge request for $branch:"
    echo "  $base/-/merge_requests/$num"
  fi
done
exit 0
`

// PushHost is a local bare remote that behaves like a git host for pushes: it
// advertises push options, records them and prints a fake merge request URL.
type PushHost struct {
	// Dir is the bare repository.
	Dir string
	// WebBase is the base of the URLs the hook prints, with no trailing slash.
	WebBase string

	t   testing.TB
	env []string
	exe string
}

// PushRecord is one push the hook saw.
type PushRecord struct {
	// Options are the -o values in the order given.
	Options []string
	// Refs are the refs the push updated (or deleted).
	Refs []string
}

// Option returns the value of a "key=value" option; a bare option ("key")
// returns "" and true.
func (p PushRecord) Option(key string) (string, bool) {
	for _, o := range p.Options {
		if o == key {
			return "", true
		}
		if k, v, ok := strings.Cut(o, "="); ok && k == key {
			return v, true
		}
	}
	return "", false
}

// NewPushHost creates the bare remote under a folder whose name has a space
// (so every caller exercises that Windows case). webBase is the fictional
// project URL the printed merge request links start with, such as
// "https://git.acme-shop.example/acme-shop/shop-api".
func NewPushHost(t testing.TB, webBase string) *PushHost {
	t.Helper()
	if strings.ContainsAny(webBase, "'\r\n") || strings.HasSuffix(webBase, "/") {
		t.Fatalf("hoststest: bad web base %q", webBase)
	}
	base := filepath.Join(t.TempDir(), "test host")
	if err := os.MkdirAll(base, 0o750); err != nil {
		t.Fatal(err)
	}
	cfg := filepath.Join(base, "gitconfig")
	if err := os.WriteFile(cfg, []byte("[user]\n\tname = Ada Test\n\temail = ada@example.test\n[init]\n\tdefaultBranch = main\n"), 0o600); err != nil {
		t.Fatal(err)
	}
	env, err := safety.NewEnvBuilder().Build(safety.EnvOptions{Extra: map[string]string{
		"GIT_CONFIG_GLOBAL": cfg, "GIT_CONFIG_NOSYSTEM": "1",
	}})
	if err != nil {
		t.Fatal(err)
	}
	exe, err := platform.Resolve("git", "", env)
	if err != nil {
		t.Skipf("git is not installed: %v", err)
	}
	h := &PushHost{Dir: filepath.Join(base, "remote.git"), WebBase: webBase, t: t, env: env, exe: exe}
	h.MustGit(base, "init", "-q", "--bare", "-b", "main", h.Dir)
	h.MustGit(h.Dir, "config", "receive.advertisePushOptions", "true")
	hook := strings.NewReplacer("@BASE@", webBase, "@LOG@", pushLog, "@COUNT@", mrCount).Replace(hookTemplate)
	hookPath := filepath.Join(h.Dir, "hooks", "post-receive")
	if err := os.WriteFile(hookPath, []byte(hook), 0o755); err != nil { //nolint:gosec // hooks must be executable
		t.Fatal(err)
	}
	return h
}

// URL is the remote URL to push to (a plain path, valid for git on every OS).
func (h *PushHost) URL() string { return h.Dir }

// Git runs git in dir with the test's own config and returns stdout and stderr
// combined, in the order they arrived.
func (h *PushHost) Git(dir string, args ...string) (string, error) {
	var out syncBuf
	p, err := platform.Start(context.Background(), platform.Spec{
		Argv: append([]string{h.exe, "-c", "core.longpaths=true"}, args...), Dir: dir, Env: h.env, Stdout: &out, Stderr: &out,
	})
	if err != nil {
		return "", err
	}
	code, err := p.Wait()
	if err != nil {
		return out.String(), err
	}
	if code != 0 {
		return out.String(), errors.New("git " + strings.Join(args, " ") + ": exit status " + strconv.Itoa(code) + ": " + out.String())
	}
	return out.String(), nil
}

// MustGit is Git that fails the test on error.
func (h *PushHost) MustGit(dir string, args ...string) string {
	h.t.Helper()
	out, err := h.Git(dir, args...)
	if err != nil {
		h.t.Fatal(err)
	}
	return out
}

// NewWork creates a work repo named name (under a folder with a space) on
// branch main with one commit, whose origin is the host, and pushes main.
func (h *PushHost) NewWork(name string) string {
	h.t.Helper()
	dir := filepath.Join(filepath.Dir(h.Dir), "work space", name)
	if err := os.MkdirAll(dir, 0o750); err != nil {
		h.t.Fatal(err)
	}
	h.MustGit(dir, "init", "-q", "-b", "main")
	h.WriteFile(dir, "README.md", "# "+name+"\n")
	h.MustGit(dir, "add", "README.md")
	h.MustGit(dir, "commit", "-q", "-m", "init")
	h.MustGit(dir, "remote", "add", "origin", h.URL())
	h.MustGit(dir, "push", "-q", "origin", "main")
	return dir
}

// WriteFile writes a file in a work repo.
func (h *PushHost) WriteFile(dir, rel, content string) {
	h.t.Helper()
	p := filepath.Join(dir, filepath.FromSlash(rel))
	if err := os.MkdirAll(filepath.Dir(p), 0o750); err != nil {
		h.t.Fatal(err)
	}
	if err := os.WriteFile(p, []byte(content), 0o600); err != nil {
		h.t.Fatal(err)
	}
}

// Pushes returns the pushes the hook recorded, oldest first.
func (h *PushHost) Pushes() []PushRecord {
	h.t.Helper()
	b, err := os.ReadFile(filepath.Join(h.Dir, pushLog))
	if errors.Is(err, os.ErrNotExist) {
		return nil
	}
	if err != nil {
		h.t.Fatal(err)
	}
	return parsePushLog(string(b))
}

func parsePushLog(s string) []PushRecord {
	var out []PushRecord
	for _, line := range strings.Split(strings.ReplaceAll(s, "\r\n", "\n"), "\n") {
		switch {
		case line == "PUSH":
			out = append(out, PushRecord{})
		case strings.HasPrefix(line, "OPT ") && len(out) > 0:
			out[len(out)-1].Options = append(out[len(out)-1].Options, strings.TrimPrefix(line, "OPT "))
		case strings.HasPrefix(line, "REF ") && len(out) > 0:
			out[len(out)-1].Refs = append(out[len(out)-1].Refs, strings.TrimPrefix(line, "REF "))
		}
	}
	return out
}

// syncBuf lets stdout and stderr share one buffer.
type syncBuf struct {
	mu sync.Mutex
	b  bytes.Buffer
}

func (s *syncBuf) Write(p []byte) (int, error) {
	s.mu.Lock()
	defer s.mu.Unlock()
	return s.b.Write(p)
}

func (s *syncBuf) String() string {
	s.mu.Lock()
	defer s.mu.Unlock()
	return s.b.String()
}
