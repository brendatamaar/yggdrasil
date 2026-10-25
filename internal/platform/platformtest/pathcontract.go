package platformtest

import (
	"path/filepath"
	"strings"
	"testing"

	"github.com/brendatamaar/yggdrasil/internal/platform"
)

// PathFixture describes the folders a PathResolver under test works on. The
// caller builds it: real temp dirs for the real resolver, a FakePaths with the
// same shape for the fake. Optional fields left empty skip their clause.
type PathFixture struct {
	// Root is an existing, absolute, canonical folder (resolving it gives it
	// back). Names with spaces are welcome.
	Root string
	// Long and Short are two spellings of one existing folder: its long name
	// and its 8.3 name. Short resolves to Long.
	Long, Short string
	// LinkOut is a link (symlink or junction) that exists inside Root and
	// points at Outside, an existing canonical folder that is not under Root.
	LinkOut, Outside string
}

const secretName = "secret-name-7431"

// PathResolverContract is the contract suite of platform.PathResolver, run
// against the fake and the real implementation (docs/AGENTS.md 5.11).
func PathResolverContract(t *testing.T, r platform.PathResolver, fx PathFixture) {
	t.Helper()
	resolve := func(t *testing.T, p string) string {
		t.Helper()
		got, err := r.ResolveRoot(p)
		if err != nil {
			t.Fatalf("ResolveRoot failed: %v", err)
		}
		return got
	}
	refuse := func(t *testing.T, p string) {
		t.Helper()
		got, err := r.ResolveRoot(p)
		if err == nil {
			t.Fatalf("ResolveRoot accepted a path it must refuse, returned %q", got)
		}
		if strings.Contains(err.Error(), secretName) || strings.Contains(err.Error(), fx.Root) {
			t.Errorf("error text holds the path: %v", err)
		}
	}

	t.Run("root resolves to itself", func(t *testing.T) {
		if got := resolve(t, fx.Root); got != fx.Root {
			t.Errorf("got %q, want %q", got, fx.Root)
		}
	})
	t.Run("dotdot collapses lexically", func(t *testing.T) {
		in := filepath.Join(fx.Root, "a", "b") + string(filepath.Separator) + ".." + string(filepath.Separator) + ".." + string(filepath.Separator) + "c"
		want := filepath.Join(fx.Root, "c")
		if got := resolve(t, in); got != want {
			t.Errorf("got %q, want %q", got, want)
		}
		up := filepath.Join(fx.Root, "..", "sibling")
		if got := resolve(t, up); strings.HasPrefix(got, fx.Root+string(filepath.Separator)) {
			t.Errorf("..\\sibling stayed inside the root: %q", got)
		}
	})
	t.Run("missing leaf resolves via deepest existing parent", func(t *testing.T) {
		got := resolve(t, filepath.Join(fx.Root, "no-such-dir", "file.txt"))
		want := filepath.Join(fx.Root, "no-such-dir", "file.txt")
		if got != want {
			t.Errorf("got %q, want %q", got, want)
		}
	})
	t.Run("idempotent", func(t *testing.T) {
		for _, in := range []string{fx.Root, filepath.Join(fx.Root, "x", "..", "y", "z.txt"), fx.Short, fx.LinkOut} {
			if in == "" {
				continue
			}
			once := resolve(t, in)
			if twice := resolve(t, once); twice != once {
				t.Errorf("not idempotent: %q then %q", once, twice)
			}
		}
	})
	t.Run("short name equals long form", func(t *testing.T) {
		if fx.Short == "" || fx.Long == "" {
			t.Skip("no 8.3 names here")
		}
		want := resolve(t, fx.Long)
		if got := resolve(t, fx.Short); got != want {
			t.Errorf("short %q != long %q", got, want)
		}
		if got := resolve(t, filepath.Join(fx.Short, "missing.txt")); got != filepath.Join(want, "missing.txt") {
			t.Errorf("short name with missing leaf: %q", got)
		}
	})
	t.Run("link out of the root resolves outside", func(t *testing.T) {
		if fx.LinkOut == "" || fx.Outside == "" {
			t.Skip("no link here")
		}
		got := resolve(t, filepath.Join(fx.LinkOut, "file.txt"))
		want := filepath.Join(fx.Outside, "file.txt")
		if got != want {
			t.Errorf("got %q, want %q", got, want)
		}
		if strings.HasPrefix(got, fx.Root+string(filepath.Separator)) {
			t.Errorf("the link target is reported inside the root: %q", got)
		}
	})
	t.Run("empty, relative and NUL input refused", func(t *testing.T) {
		for _, p := range []string{"", "   ", "relative", filepath.Join("a", "b"), "." + string(filepath.Separator) + "x", fx.Root + "\x00" + secretName} {
			refuse(t, p)
		}
	})
	t.Run("Windows name tricks refused", func(t *testing.T) {
		if !r.FoldCase() {
			t.Skip("these names are ordinary file names on this platform")
		}
		for _, name := range []string{
			secretName + ".txt:stream", secretName + ".txt::$DATA", "CON", "con.txt", "NUL", "aux", "COM1", "lpt9.log",
			"CONOUT$", secretName + ".", secretName + " ", "wild*card", "q?",
		} {
			refuse(t, filepath.Join(fx.Root, name))
			refuse(t, filepath.Join(fx.Root, name, "sub"))
		}
		for _, p := range []string{`\\wsl$\Ubuntu\home`, `\\WSL.LOCALHOST\Ubuntu`, `\\?\UNC\wsl$\Ubuntu`, `\\.\pipe\` + secretName, `\\?\GLOBALROOT\Device\X`, `\\?\Volume{1}\x`} {
			refuse(t, p)
		}
	})
	t.Run("case fold flag matches reality", func(t *testing.T) {
		if !r.FoldCase() {
			return
		}
		up := strings.ToUpper(fx.Root)
		if up == fx.Root {
			up = strings.ToLower(fx.Root)
		}
		if got := resolve(t, up); !strings.EqualFold(got, fx.Root) {
			t.Errorf("case variant resolved to a different folder: %q", got)
		}
	})
}
