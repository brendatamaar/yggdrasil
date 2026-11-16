package repos_test

// Boundary tests of the noise snapshot parser and of SnapshotFrom (WS-09). The
// oracles below are written from docs/design/phase-6-noise.md, not from the
// production validators.

import (
	"errors"
	"fmt"
	"math/rand"
	"reflect"
	"slices"
	"strings"
	"testing"
	"unicode/utf8"

	corerepos "github.com/brendatamaar/yggdrasil/internal/core/repos"
	"github.com/brendatamaar/yggdrasil/internal/git"
	"github.com/brendatamaar/yggdrasil/internal/repos"
)

// safePath is the independent statement of an exact, safe, relative file path.
func safePath(p string) bool {
	if p == "" || len(p) > 4096 || !utf8.ValidString(p) {
		return false
	}
	for _, r := range p {
		if r < 0x20 || (r >= 0x7f && r <= 0x9f) || r == '\\' || r == ':' {
			return false
		}
	}
	for _, c := range strings.Split(p, "/") {
		if c == "" || c == "." || c == ".." || strings.HasSuffix(c, ".") || strings.HasSuffix(c, " ") || strings.ToLower(c) == ".git" {
			return false
		}
	}
	return true
}

// expectedNoise says what ParseNoiseSnapshot must do with a snapshot: ok=false
// means refusal, otherwise the exact facts. Truncated is canonical: it is true
// when the reader said so or when fewer paths than the count are present.
func expectedNoise(snap corerepos.Snapshot) (corerepos.NoiseFacts, bool) {
	var sample []string
	if snap.UntrackedPaths != "" {
		sample = strings.Split(snap.UntrackedPaths, "\x00")
	}
	f := corerepos.NoiseFacts{Repo: "rep_1", UntrackedCount: snap.Untracked, Sample: sample, Truncated: snap.UntrackedTruncated || len(sample) < snap.Untracked}
	if snap.Untracked < 0 || len(sample) > 5000 || (len(sample) > snap.Untracked && !f.Truncated) {
		return corerepos.NoiseFacts{}, false
	}
	seen := map[string]bool{}
	for _, p := range sample {
		if !safePath(p) || seen[p] {
			return corerepos.NoiseFacts{}, false
		}
		seen[p] = true
	}
	return f, true
}

func encodeNoise(f corerepos.NoiseFacts) corerepos.Snapshot {
	return corerepos.Snapshot{Untracked: f.UntrackedCount, UntrackedPaths: strings.Join(f.Sample, "\x00"), UntrackedTruncated: f.Truncated}
}

// Each rejection rule has its own case, and every refusal is ErrNoiseUnavailable
// (the caller labels noise unavailable and keeps the ordinary status).
func TestParseNoiseSnapshotRejectsEachRule(t *testing.T) {
	many := func(n int) string {
		p := make([]string, n)
		for i := range p {
			p[i] = fmt.Sprintf("dist/f%d", i)
		}
		return strings.Join(p, "\x00")
	}
	for name, snap := range map[string]corerepos.Snapshot{
		"negative count":                 {Untracked: -1},
		"negative count with a sample":   {Untracked: -5, UntrackedPaths: "dist/a"},
		"sample larger than count":       {Untracked: 1, UntrackedPaths: "dist/a\x00dist/b"},
		"5001 paths":                     {Untracked: 9000, UntrackedPaths: many(5001), UntrackedTruncated: true},
		"6000 paths":                     {Untracked: 9000, UntrackedPaths: many(6000), UntrackedTruncated: true},
		"duplicate":                      {Untracked: 5, UntrackedPaths: "dist/a\x00dist/a"},
		"leading NUL (empty first path)": {Untracked: 5, UntrackedPaths: "\x00dist/a"},
		"trailing NUL (empty last path)": {Untracked: 5, UntrackedPaths: "dist/a\x00"},
		"double NUL (empty path)":        {Untracked: 5, UntrackedPaths: "dist/a\x00\x00dist/b"},
		"only a NUL":                     {Untracked: 5, UntrackedPaths: "\x00"},
		"absolute":                       {Untracked: 5, UntrackedPaths: "/dist/a"},
		"UNC":                            {Untracked: 5, UntrackedPaths: "//host/share/dist/a"},
		"drive letter":                   {Untracked: 5, UntrackedPaths: "C:/dist/a"},
		"drive letter, backslashes":      {Untracked: 5, UntrackedPaths: `C:\dist\a`},
		"backslash":                      {Untracked: 5, UntrackedPaths: `dist\a`},
		"alternate data stream":          {Untracked: 5, UntrackedPaths: "dist/a:stream"},
		"traversal":                      {Untracked: 5, UntrackedPaths: "dist/../../a"},
		"dot component":                  {Untracked: 5, UntrackedPaths: "dist/./a"},
		"empty component":                {Untracked: 5, UntrackedPaths: "dist//a"},
		"dot git component":              {Untracked: 5, UntrackedPaths: "dist/.git/a"},
		"dot git, other case":            {Untracked: 5, UntrackedPaths: "dist/.GIT/a"},
		"trailing dot component":         {Untracked: 5, UntrackedPaths: "dist./a"},
		"trailing space component":       {Untracked: 5, UntrackedPaths: "dist /a"},
		"line feed":                      {Untracked: 5, UntrackedPaths: "dist/a\nb"},
		"carriage return (CRLF)":         {Untracked: 5, UntrackedPaths: "dist/a\r\ndist/b"},
		"control":                        {Untracked: 5, UntrackedPaths: "dist/a\x1b[31m"},
		"C1 control":                     {Untracked: 5, UntrackedPaths: "dist/a\u009b"},
		"invalid UTF-8":                  {Untracked: 5, UntrackedPaths: "dist/\xff\xfe"},
		"path of 4097 bytes":             {Untracked: 5, UntrackedPaths: strings.Repeat("a", 4097)},
		"encoded sample too large":       {Untracked: 9000, UntrackedPaths: strings.Repeat("a", 5000*4097+1), UntrackedTruncated: true},
	} {
		t.Run(name, func(t *testing.T) {
			facts, err := repos.ParseNoiseSnapshot("rep_1", snap)
			if !errors.Is(err, repos.ErrNoiseUnavailable) {
				t.Fatalf("accepted: %+v, err %v", facts, err)
			}
			if facts.Sample != nil || facts.UntrackedCount != 0 || facts.Repo != "" {
				t.Fatalf("a refusal returned data: %+v", facts)
			}
		})
	}
}

// The upper bounds are accepted exactly, and safe unusual names are kept as is.
func TestParseNoiseSnapshotAcceptsBoundsAndKeepsExactBytes(t *testing.T) {
	five := make([]string, 5000)
	for i := range five {
		five[i] = fmt.Sprintf("dist/f%d", i)
	}
	long := strings.Repeat("a", 4096)
	for name, paths := range map[string][]string{
		"5000 paths":          five,
		"a 4096 byte path":    {long},
		"spaces":              {"Program Files/node_modules/a b"},
		"leading space":       {" lead/dist/x"},
		"unicode, composed":   {"caf\u00e9/dist/x"},
		"unicode, decomposed": {"cafe\u0301/dist/x"},
		"metacharacters":      {"a[1]*?!#/dist/x"},
		"unicode and long":    {"日本語/" + strings.Repeat("é", 1000) + "/dist/x"},
		"dot-prefixed names":  {".venv/lib/x", "..a/b", "a..x/b..x"},
	} {
		t.Run(name, func(t *testing.T) {
			raw := strings.Join(paths, "\x00")
			f, err := repos.ParseNoiseSnapshot("rep_1", corerepos.Snapshot{Untracked: len(paths), UntrackedPaths: raw})
			if err != nil {
				t.Fatal(err)
			}
			if !slices.Equal(f.Sample, paths) || strings.Join(f.Sample, "\x00") != raw {
				t.Fatal("the sample was changed")
			}
		})
	}
	// Composed and decomposed spellings are two different files; both stay.
	f, err := repos.ParseNoiseSnapshot("rep_1", corerepos.Snapshot{Untracked: 2, UntrackedPaths: "caf\u00e9/x\x00cafe\u0301/x"})
	if err != nil || len(f.Sample) != 2 {
		t.Fatalf("%+v %v", f, err)
	}
}

// Round trip and accepted-implies-valid over generated snapshots, against the
// independent oracle: accepted exactly when the oracle accepts, with the facts
// the oracle derives, and re-encoding the facts parses to the same facts.
func TestPropParseNoiseSnapshotRoundTripAndOracle(t *testing.T) {
	pool := []string{"dist/a", "node_modules/x/y", "a b/c", "日本語/d", "../x", "/abs", "C:/x", "a\nb", "x/.git/y", "dist/a", "z/", "", "a/b.", string([]byte{0xff}), "q[1]/*"}
	accepted, rejected, partial := 0, 0, 0
	for seed := int64(1); seed <= 600; seed++ {
		r := rand.New(rand.NewSource(seed))
		var paths []string
		for i, n := 0, r.Intn(6); i < n; i++ {
			if r.Intn(3) == 0 {
				paths = append(paths, pool[r.Intn(len(pool))])
			} else {
				paths = append(paths, fmt.Sprintf("dir%d/f%d", r.Intn(3), i))
			}
		}
		snap := corerepos.Snapshot{Untracked: r.Intn(9) - 1, UntrackedPaths: strings.Join(paths, "\x00"), UntrackedTruncated: r.Intn(3) == 0}
		want, ok := expectedNoise(snap)
		got, err := repos.ParseNoiseSnapshot("rep_1", snap)
		if ok != (err == nil) {
			t.Fatalf("seed %d snapshot %+v: oracle accepts=%v, parser err=%v", seed, snap, ok, err)
		}
		if !ok {
			rejected++
			if !errors.Is(err, repos.ErrNoiseUnavailable) {
				t.Fatalf("seed %d: %v", seed, err)
			}
			continue
		}
		accepted++
		if got.IsPartial() {
			partial++
		}
		if !reflect.DeepEqual(got, want) {
			t.Fatalf("seed %d: facts %+v, oracle %+v", seed, got, want)
		}
		again, err := repos.ParseNoiseSnapshot("rep_1", encodeNoise(got))
		if err != nil || !reflect.DeepEqual(again, got) {
			t.Fatalf("seed %d: round trip %+v %v", seed, again, err)
		}
		if got.Truncated != (snap.UntrackedTruncated || len(got.Sample) < snap.Untracked) {
			t.Fatalf("seed %d: Truncated is not canonical: %+v", seed, got)
		}
	}
	if accepted < 50 || rejected < 50 || partial < 10 {
		t.Fatalf("generator is lopsided: accepted=%d rejected=%d partial=%d", accepted, rejected, partial)
	}
}

func FuzzParseNoiseSnapshotOracle(f *testing.F) {
	f.Add("dist/a", 1, false)
	f.Add("dist/a\x00dist/b", 2, true)
	f.Add("dist/a\x00dist/a", 5, false)
	f.Add("../bad", 1, false)
	f.Add("a\x00\x00b", 3, false)
	f.Add(strings.Repeat("a\x00", 5001), 9000, true)
	f.Add("café/x", -3, true)
	f.Fuzz(func(t *testing.T, raw string, count int, truncated bool) {
		snap := corerepos.Snapshot{Untracked: count, UntrackedPaths: raw, UntrackedTruncated: truncated}
		want, ok := expectedNoise(snap)
		got, err := repos.ParseNoiseSnapshot("rep_1", snap)
		if ok != (err == nil) {
			t.Fatalf("oracle accepts=%v, parser err=%v for %q count=%d truncated=%v", ok, err, raw, count, truncated)
		}
		if ok {
			if !reflect.DeepEqual(got, want) {
				t.Fatalf("facts %+v, oracle %+v", got, want)
			}
			if strings.Join(got.Sample, "\x00") != raw {
				t.Fatal("the sample bytes changed")
			}
		}
	})
}

// SnapshotFrom keeps exactly the existing bounded untracked sample, in order,
// and only when the count calls for noise analysis; everything else is copied
// exactly. It is pure: the input status is never changed.
func TestPropSnapshotFromRetainsExactlyTheUntrackedSample(t *testing.T) {
	marks := []string{"?", "M", "A", "D", "R", "U", ".", "!"}
	kept, dropped := 0, 0
	for seed := int64(1); seed <= 400; seed++ {
		r := rand.New(rand.NewSource(seed))
		st := git.Status{
			Branch: "main", Upstream: "origin/main", HasUpstream: r.Intn(2) == 0,
			Ahead: r.Intn(5), Behind: r.Intn(5), Staged: r.Intn(5), Unstaged: r.Intn(5), Conflicted: r.Intn(3),
			Truncated: r.Intn(4) == 0,
		}
		untracked := []int{0, 1, 99, 100, 101, 150}[r.Intn(6)]
		st.Untracked = untracked
		var want []string
		for i := 0; i < 150; i++ {
			idx, wt := marks[r.Intn(len(marks))], marks[r.Intn(len(marks))]
			isU := idx == "?" || wt == "?"
			p := fmt.Sprintf("d%d/f%d", i%7, i)
			if isU {
				want = append(want, p)
			}
			st.Files = append(st.Files, git.FileChange{Path: p, Index: idx, Worktree: wt})
		}
		before := slices.Clone(st.Files)
		snap, err := repos.SnapshotFrom(st, nil)
		if err != nil {
			t.Fatalf("seed %d: %v", seed, err)
		}
		if !reflect.DeepEqual(before, st.Files) {
			t.Fatalf("seed %d: SnapshotFrom changed its input", seed)
		}
		wantRaw := ""
		if untracked >= 100 {
			wantRaw = strings.Join(want, "\x00")
			kept++
		} else {
			dropped++
		}
		if snap.UntrackedPaths != wantRaw || snap.Untracked != untracked || snap.UntrackedTruncated != st.Truncated ||
			snap.Ahead != st.Ahead || snap.Behind != st.Behind || snap.Staged != st.Staged || snap.Unstaged != st.Unstaged || snap.Conflicted != st.Conflicted ||
			snap.Branch != "main" || snap.Upstream != "origin/main" || snap.HasUpstream != st.HasUpstream || snap.LastCommit != nil {
			t.Fatalf("seed %d: snapshot %+v does not match the status", seed, snap)
		}
		for _, p := range strings.Split(snap.UntrackedPaths, "\x00") {
			if p != "" && !slices.Contains(want, p) {
				t.Fatalf("seed %d: %q is not an untracked file", seed, p)
			}
		}
	}
	if kept < 100 || dropped < 100 {
		t.Fatalf("generator is lopsided: kept=%d dropped=%d", kept, dropped)
	}
}

// Values git cannot produce are refused, each for its own reason, and ordinary
// values pass; the commit is carried over exactly.
func TestSnapshotFromRejectsEachImpossibleValue(t *testing.T) {
	ok := git.Status{Branch: "main"}
	if _, err := repos.SnapshotFrom(ok, nil); err != nil {
		t.Fatal(err)
	}
	for name, change := range map[string]func(*git.Status){
		"negative ahead":      func(s *git.Status) { s.Ahead = -1 },
		"negative behind":     func(s *git.Status) { s.Behind = -1 },
		"negative staged":     func(s *git.Status) { s.Staged = -1 },
		"negative unstaged":   func(s *git.Status) { s.Unstaged = -1 },
		"negative untracked":  func(s *git.Status) { s.Untracked = -1 },
		"negative conflicted": func(s *git.Status) { s.Conflicted = -1 },
		"detached on branch":  func(s *git.Status) { s.Detached = true },
	} {
		t.Run(name, func(t *testing.T) {
			st := ok
			change(&st)
			if _, err := repos.SnapshotFrom(st, nil); err == nil {
				t.Fatal("accepted")
			}
		})
	}
}

// A hostile untracked name never makes ordinary status unusable: the snapshot is
// still produced and Fresh; only the noise facts of that repo are unavailable.
func TestSnapshotWithHostileNameKeepsStatusAndFailsOnlyNoise(t *testing.T) {
	st := git.Status{Branch: "main", Untracked: 100}
	for i := 0; i < 99; i++ {
		st.Files = append(st.Files, git.FileChange{Path: fmt.Sprintf("dist/f%d", i), Worktree: "?"})
	}
	st.Files = append(st.Files, git.FileChange{Path: "dist/evil\r\n.gitignore", Worktree: "?"})
	snap, err := repos.SnapshotFrom(st, nil)
	if err != nil || snap.Untracked != 100 || snap.Branch != "main" {
		t.Fatalf("ordinary snapshot unusable: %+v %v", snap, err)
	}
	if _, err := repos.ParseNoiseSnapshot("rep_1", snap); !errors.Is(err, repos.ErrNoiseUnavailable) {
		t.Fatalf("hostile name accepted as noise evidence: %v", err)
	}
}
