package worktrees_test

import (
	"errors"
	"math/rand/v2"
	"strings"
	"testing"

	"github.com/brendatamaar/yggdrasil/internal/worktrees"
)

type seeded struct{ r *rand.Rand }

func (s seeded) IntN(n int) int { return s.r.IntN(n) }

func TestGenerateRoundTripsAndFits(t *testing.T) {
	r := seeded{rand.New(rand.NewPCG(1, 2))}
	for i := range 2000 {
		n := worktrees.Generate(r, i%5)
		got, err := worktrees.ParseName(string(n))
		if err != nil || got != n {
			t.Fatalf("Generate gave %q, which does not parse: %v", n, err)
		}
		if len(n) > worktrees.MaxNameLen {
			t.Fatalf("%q is longer than %d", n, worktrees.MaxNameLen)
		}
		if !strings.HasPrefix(n.Branch(), "ygg/") {
			t.Fatalf("branch %q", n.Branch())
		}
	}
}

func TestParseNameRules(t *testing.T) {
	for _, tc := range []struct {
		name, in string
		ok       bool
	}{
		{"two words", "calm-pine", true},
		{"with digit", "calm-pine7", true},
		{"max length", "abcdefg-hijklm7", true},
		{"too long", "abcdefgh-ijklmnop", false},
		{"empty", "", false},
		{"no dash", "calmpine", false},
		{"three words", "a-b-c", false},
		{"empty first word", "-pine", false},
		{"empty second word", "calm-", false},
		{"digit only second word", "calm-7", false},
		{"upper case", "Calm-pine", false},
		{"two digits", "calm-pine77", false},
		{"digit in the middle", "ca1m-pine", false},
		{"space", "calm -pine", false},
		{"slash", "calm/pine", false},
		{"non-ascii", "cälm-pine", false},
	} {
		t.Run(tc.name, func(t *testing.T) {
			_, err := worktrees.ParseName(tc.in)
			if (err == nil) != tc.ok {
				t.Fatalf("ParseName(%q) err = %v, want ok=%v", tc.in, err, tc.ok)
			}
			if err != nil && !errors.Is(err, worktrees.ErrBadName) {
				t.Fatalf("error does not wrap ErrBadName: %v", err)
			}
		})
	}
}

func FuzzParseName(f *testing.F) {
	for _, s := range []string{"calm-pine", "calm-pine3", "", "-", "a-b", "A-b", "x-y9", "../../x-y"} {
		f.Add(s)
	}
	f.Fuzz(func(t *testing.T, s string) {
		n, err := worktrees.ParseName(s)
		if err != nil {
			return
		}
		// Accepted implies valid: short, safe as a folder and as a branch part.
		if len(n) > worktrees.MaxNameLen || strings.ContainsAny(string(n), `/\.: `) || strings.Count(string(n), "-") != 1 {
			t.Fatalf("accepted %q", s)
		}
		if again, err := worktrees.ParseName(string(n)); err != nil || again != n {
			t.Fatalf("round trip of %q failed", n)
		}
	})
}
