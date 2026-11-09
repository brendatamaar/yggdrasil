package devservers

import (
	"context"
	"fmt"
	"os"
	"slices"
	"strings"
	"sync"
	"time"

	"github.com/brendatamaar/yggdrasil/internal/apperr"
	"github.com/brendatamaar/yggdrasil/internal/builds/toolchains"
	"github.com/brendatamaar/yggdrasil/internal/core"
)

// CatalogTTL is how long a folder's detected commands are reused. Edits to a
// manifest show up after this, or at once after Invalidate.
const CatalogTTL = 30 * time.Second

// DevCommand is one dev-server command a folder offers. Key is its stable
// name for the server ID: it is derived once, when the folder is scanned, and
// every reader of the same snapshot sees the same key.
type DevCommand struct {
	Key  string
	Name string
	Argv []string
	Env  map[string]string
}

// Detector lists the dev-server commands of a folder, in a fixed order. It
// reads files only and never runs a command.
type Detector func(dir string) []DevCommand

// Catalog lists the dev-server commands the toolchains detect for a target. It
// resolves the folder through Repos on every call, so a target that is no
// longer tracked, or whose folder is a link, offers nothing (invariant 6), and
// it keeps the scan of the manifests for CatalogTTL so listing the servers
// does not scan the tracked tree on every request. It implements Templates.
type Catalog struct {
	repos  Repos
	clock  Clock
	detect Detector

	mu    sync.Mutex
	cache map[Target]catalogEntry
}

type catalogEntry struct {
	dir  string
	at   time.Time
	cmds []DevCommand
}

// CatalogOption configures a Catalog.
type CatalogOption func(*Catalog)

// WithDetector replaces the toolchain detection (tests).
func WithDetector(d Detector) CatalogOption { return func(c *Catalog) { c.detect = d } }

// NewCatalog returns a catalog over repos. repos must resolve with the
// run-folder check (no symlink or junction leaf), the same Repos the service
// starts servers with.
func NewCatalog(repos Repos, clock Clock, opts ...CatalogOption) *Catalog {
	if clock == nil {
		clock = RealClock{}
	}
	c := &Catalog{repos: repos, clock: clock, detect: DetectDevCommands, cache: map[Target]catalogEntry{}}
	for _, o := range opts {
		o(c)
	}
	return c
}

// Commands returns the commands the target offers, sorted by key. An error
// from Repos (untracked, refused folder) is returned as it is.
func (c *Catalog) Commands(ctx context.Context, t Target) ([]DevCommand, error) {
	dir, err := c.repos.Dir(ctx, t)
	if err != nil {
		return nil, err
	}
	now := c.clock.Now()
	c.mu.Lock()
	e, ok := c.cache[t]
	c.mu.Unlock()
	if ok && e.dir == dir && now.Sub(e.at) < CatalogTTL && !now.Before(e.at) {
		return e.cmds, nil
	}
	cmds := slices.Clone(c.detect(dir))
	slices.SortFunc(cmds, func(a, b DevCommand) int { return strings.Compare(a.Key, b.Key) })
	c.mu.Lock()
	c.cache[t] = catalogEntry{dir: dir, at: now, cmds: cmds}
	c.mu.Unlock()
	return cmds, nil
}

// Invalidate forgets the scans of a repo and its worktrees: call it when the
// repo's files or settings changed.
func (c *Catalog) Invalidate(repo core.RepoID) {
	c.mu.Lock()
	defer c.mu.Unlock()
	for t := range c.cache {
		if t.Repo == repo {
			delete(c.cache, t)
		}
	}
}

// InvalidateAll forgets every scan (an explicit refresh).
func (c *Catalog) InvalidateAll() {
	c.mu.Lock()
	defer c.mu.Unlock()
	clear(c.cache)
}

// DevServer implements Templates from the same snapshot Commands lists.
func (c *Catalog) DevServer(ctx context.Context, t Target, command string) ([]string, map[string]string, error) {
	cmds, err := c.Commands(ctx, t)
	if err != nil {
		return nil, nil, err
	}
	for _, cmd := range cmds {
		if cmd.Key == command {
			env := make(map[string]string, len(cmd.Env))
			for k, v := range cmd.Env {
				env[k] = v
			}
			return slices.Clone(cmd.Argv), env, nil
		}
	}
	return nil, nil, apperr.New(apperr.Invalid, "This repo has no dev server command by that name. Choose one of the commands offered for it.", nil)
}

// DetectDevCommands is the default Detector: the dev-server steps of the
// toolchains that match dir. The first is keyed "dev" (so a stored row keeps
// its ID); the others by the slug of their name, with -2, -3 ... on a clash.
func DetectDevCommands(dir string) []DevCommand {
	var out []DevCommand
	taken := map[string]bool{}
	for _, m := range toolchains.DetectAll(os.DirFS(dir)) {
		for _, s := range m.Toolchain.Commands(m.Detection).DevServer {
			key := "dev"
			if len(out) > 0 {
				key = commandKey(s.Name)
			}
			base := key
			for n := 2; taken[key]; n++ {
				key = fmt.Sprintf("%s-%d", base, n)
			}
			taken[key] = true
			env := map[string]string{}
			for _, kv := range s.Env {
				k, v, _ := strings.Cut(kv, "=")
				env[k] = v
			}
			out = append(out, DevCommand{Key: key, Name: s.Name, Argv: slices.Clone(s.Argv), Env: env})
		}
	}
	return out
}

// commandKey makes a command key from a step name: lower-case letters,
// digits and '-'.
func commandKey(name string) string {
	var b strings.Builder
	for _, r := range strings.ToLower(name) {
		switch {
		case r >= 'a' && r <= 'z', r >= '0' && r <= '9':
			b.WriteRune(r)
		case b.Len() > 0 && !strings.HasSuffix(b.String(), "-"):
			b.WriteByte('-')
		}
	}
	k := strings.Trim(b.String(), "-")
	if k == "" || len(k) > 48 {
		return "dev-server"
	}
	return k
}
