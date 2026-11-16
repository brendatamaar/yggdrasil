package platform_test

import (
	"bytes"
	"context"
	"errors"
	"math/rand/v2"
	"path/filepath"
	"strings"
	"testing"

	"github.com/brendatamaar/yggdrasil/internal/platform"
	"github.com/brendatamaar/yggdrasil/internal/platform/platformtest"
)

// Property tests of the invariant "no pre-existing byte is ever changed or
// removed" (docs/design/phase-6-newrepos.md TestPropInitNeverOverwrites and
// phase-6-noise.md). Random existing content, checked after every call, against
// the real implementation and the fake.

var tokens = []string{"a", "b", "node_modules/", "dist", "#c", " ", "\n", "\r\n", "\r", "\n\n", "é", "\x00", "\xff", "*.log", "!keep"}

func randContent(r *rand.Rand) []byte {
	var b bytes.Buffer
	for range r.IntN(12) {
		b.WriteString(tokens[r.IntN(len(tokens))])
	}
	return b.Bytes()
}

var entryPool = []string{"a", "b", "node_modules/", "dist", "#c", "*.log", "!keep", "ünï/", "build", "x y"}

func randEntries(r *rand.Rand) []string {
	out := make([]string, 1+r.IntN(4))
	for i := range out {
		out[i] = entryPool[r.IntN(len(entryPool))]
	}
	return out
}

func lines(b []byte) map[string]bool {
	m := map[string]bool{}
	for _, l := range strings.Split(string(b), "\n") {
		m[strings.TrimSuffix(l, "\r")] = true
	}
	return m
}

type implFactory struct {
	name string
	init func(t *testing.T) (platform.SafeInitFS, string)
	ign  func(t *testing.T) (platform.IgnoreWriter, string)
}

func factories() []implFactory {
	return []implFactory{
		{
			name: "real",
			init: func(t *testing.T) (platform.SafeInitFS, string) {
				return platformtest.OSSafeInitFS(), platformtest.CanonicalTempDir(t)
			},
			ign: func(t *testing.T) (platform.IgnoreWriter, string) {
				return platformtest.OSIgnoreWriter(), platformtest.CanonicalTempDir(t)
			},
		},
		{
			name: "fake",
			init: func(*testing.T) (platform.SafeInitFS, string) {
				d := platformtest.NewFakeDisk()
				p, _ := filepath.Abs(filepath.FromSlash("/work/p"))
				d.Mkdir(p)
				return platformtest.NewFakeSafeInitFS(d), p
			},
			ign: func(*testing.T) (platform.IgnoreWriter, string) {
				d := platformtest.NewFakeDisk()
				p, _ := filepath.Abs(filepath.FromSlash("/work/p"))
				d.Mkdir(p)
				return platformtest.NewFakeIgnoreWriter(d), p
			},
		},
	}
}

func TestPropAppendIgnoreNeverChangesExistingBytes(t *testing.T) {
	ctx := context.Background()
	for _, f := range factories() {
		t.Run(f.name, func(t *testing.T) {
			w, root := f.ign(t)
			fx := w.(platformtest.FixtureProvider).Fixture()
			path := filepath.Join(root, ".gitignore")
			r := rand.New(rand.NewPCG(7, 11))
			for i := range 150 {
				seed := randContent(r)
				fx.WriteFile(t, path, seed)
				entries := randEntries(r)
				expect := platform.IgnoreExpectation{Hash: platform.HashHex(seed)}
				stale := r.IntN(4) == 0
				if stale {
					expect.Hash = platform.HashHex(append([]byte("stale"), seed...))
				}
				out, snap, err := w.AppendIgnore(ctx, root, entries, expect)
				got, _ := fx.ReadFile(t, path)
				if !bytes.HasPrefix(got, seed) {
					t.Fatalf("#%d: existing bytes changed: %q -> %q", i, seed, got)
				}
				if stale {
					if !errors.Is(err, platform.ErrChanged) || !bytes.Equal(got, seed) {
						t.Fatalf("#%d: stale hash: %v, bytes %q", i, err, got)
					}
					continue
				}
				if err != nil {
					t.Fatalf("#%d: %v (seed %q entries %q)", i, err, seed, entries)
				}
				if snap.Hash != platform.HashHex(got) {
					t.Fatalf("#%d: snapshot does not match the bytes", i)
				}
				have := lines(got)
				for _, e := range entries {
					if !have[e] {
						t.Fatalf("#%d: entry %q missing from %q", i, e, got)
					}
				}
				if (out == platform.IgnoreAlreadyPresent) != bytes.Equal(got, seed) {
					t.Fatalf("#%d: outcome %q but bytes changed=%v", i, out, !bytes.Equal(got, seed))
				}
				// Idempotent: the same call on the result writes nothing.
				out2, _, err := w.AppendIgnore(ctx, root, entries, platform.IgnoreExpectation{Hash: snap.Hash})
				again, _ := fx.ReadFile(t, path)
				if err != nil || out2 != platform.IgnoreAlreadyPresent || !bytes.Equal(again, got) {
					t.Fatalf("#%d: repeat wrote: %q %v", i, out2, err)
				}
			}
		})
	}
}

func TestPropCreateNeverTouchesExisting(t *testing.T) {
	ctx := context.Background()
	for _, f := range factories() {
		t.Run(f.name, func(t *testing.T) {
			s, parent := f.init(t)
			fx := s.(platformtest.FixtureProvider).Fixture()
			r := rand.New(rand.NewPCG(3, 5))
			id, err := s.CreateChildDir(ctx, parent, "repo")
			if err != nil {
				t.Fatal(err)
			}
			for i := range 100 {
				seed := randContent(r)
				path := filepath.Join(parent, "repo", "f.txt")
				if i == 0 {
					fx.WriteFile(t, path, seed)
				}
				before, _ := fx.ReadFile(t, path)
				if _, err := s.CreateFile(ctx, parent, "repo", id, "f.txt", randContent(r)); !errors.Is(err, platform.ErrExists) {
					t.Fatalf("#%d: CreateFile over an existing file: %v", i, err)
				}
				if got, _ := fx.ReadFile(t, path); !bytes.Equal(got, before) {
					t.Fatalf("#%d: file changed %q -> %q", i, before, got)
				}
				if _, err := s.CreateChildDir(ctx, parent, "repo"); !errors.Is(err, platform.ErrExists) {
					t.Fatalf("#%d: CreateChildDir over an existing folder: %v", i, err)
				}
				if got, _ := fx.ReadFile(t, path); !bytes.Equal(got, before) {
					t.Fatalf("#%d: content of an existing folder changed", i)
				}
				if err := s.VerifyChildDir(ctx, parent, "repo", id); err != nil {
					t.Fatalf("#%d: the folder identity changed: %v", i, err)
				}
			}
		})
	}
}
