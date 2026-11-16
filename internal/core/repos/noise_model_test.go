package repos

// Model-based property test for NoiseStep (WS-09). The reference model below is
// written from docs/design/phase-6-noise.md, not from noise.go: it re-derives
// thresholds, evidence, nesting, escaping and selection membership on its own,
// and shares no helper with the production code (it does not call NoiseDirs,
// ValidNoisePath, classifyNoise, dropNested, nestedUnder or validNoiseSelection).

import (
	"fmt"
	"math/rand"
	"reflect"
	"regexp"
	"sort"
	"strings"
	"testing"
	"unicode/utf8"

	"github.com/brendatamaar/yggdrasil/internal/core"
)

// ---- independent oracle -----------------------------------------------------

var oracleAllow = map[string]bool{
	"node_modules": true, ".venv": true, "dist": true, "build": true,
	"target": true, "bin": true, "obj": true, "__pycache__": true,
}

// oraclePath states the rules of an exact, safe, relative file path component by
// component: nothing a git pathspec, a Windows API or a terminal could read as
// something other than one literal file.
func oraclePath(p string) bool {
	if p == "" || len(p) > 4096 || !utf8.ValidString(p) {
		return false
	}
	for _, r := range p {
		if r < 0x20 || (r >= 0x7f && r <= 0x9f) || r == '\\' || r == ':' {
			return false
		}
	}
	for _, comp := range strings.Split(p, "/") {
		switch {
		case comp == "", comp == ".", comp == "..":
			return false
		case strings.HasSuffix(comp, "."), strings.HasSuffix(comp, " "):
			return false
		case strings.ToLower(comp) == ".git":
			return false
		}
	}
	return true
}

func oracleFactsValid(f NoiseFacts, repo core.RepoID) bool {
	if f.Repo != repo || f.UntrackedCount < 0 || len(f.Sample) > 5000 {
		return false
	}
	if len(f.Sample) > f.UntrackedCount && !f.Truncated {
		return false
	}
	seen := map[string]bool{}
	for _, p := range f.Sample {
		if !oraclePath(p) || seen[p] {
			return false
		}
		seen[p] = true
	}
	return true
}

// oracleEscape builds the anchored gitignore line of one directory.
func oracleEscape(dir string) string {
	var b strings.Builder
	b.WriteByte('/')
	for _, r := range dir {
		if strings.ContainsRune(`\*?[]!# `, r) {
			b.WriteByte('\\')
		}
		b.WriteRune(r)
	}
	b.WriteByte('/')
	return b.String()
}

// oracleState re-derives the analysis state of valid facts.
func oracleState(f NoiseFacts) NoiseState {
	if f.UntrackedCount < 100 || len(f.Sample) < 50 {
		return NoSuggestion{}
	}
	count := map[string]int{}
	for _, file := range f.Sample {
		parts := strings.Split(file, "/")
		for i := 0; i < len(parts)-1; i++ {
			if oracleAllow[parts[i]] {
				count[strings.Join(parts[:i+1], "/")]++
			}
		}
	}
	qualifies := map[string]bool{}
	for dir, c := range count {
		if c >= 50 && 2*c >= len(f.Sample) {
			qualifies[dir] = true
		}
	}
	ev := NoiseEvidence{Total: f.UntrackedCount, Sampled: len(f.Sample)}
	incomplete := f.Truncated || len(f.Sample) < f.UntrackedCount
	var out []NoiseCandidate
	for dir := range qualifies {
		nested := false
		for outer := range qualifies {
			if strings.HasPrefix(dir, outer+"/") {
				nested = true
			}
		}
		if nested {
			continue
		}
		e := ev
		e.UnderDirectory = count[dir]
		out = append(out, NoiseCandidate{Directory: dir, SampleCount: count[dir], Pattern: oracleEscape(dir), Evidence: e, Incomplete: incomplete})
	}
	if len(out) == 0 {
		return NoSuggestion{}
	}
	sort.Slice(out, func(i, j int) bool { return out[i].Directory < out[j].Directory })
	return Suggested{Candidates: out, Evidence: ev}
}

var oracleHash = regexp.MustCompile(`^[0-9a-f]{64}$`)

func oracleID(s string) bool { return s != "" && !strings.ContainsAny(s, " \t\r\n/\\") }

// oracleStep is the reference transition function of the design's table.
func oracleStep(n Noise, in NoiseInput) (Noise, []NoiseCommand) {
	switch x := in.(type) {
	case NoiseScopeRemoved:
		return Noise{Repo: n.Repo, State: NoSuggestion{}}, nil
	case NoiseObserved:
		switch n.State.(type) {
		case NoSuggestion, Suggested:
			if oracleFactsValid(x.Facts, n.Repo) {
				return Noise{Repo: n.Repo, State: oracleState(x.Facts)}, nil
			}
		case AwaitingConfirmation: // a pending proposal is immutable
		}
	case NoiseSelected:
		s, ok := n.State.(Suggested)
		if !ok || len(x.Entries) < 1 || len(x.Entries) > 32 || !oracleID(string(x.Proposal)) || !oracleID(string(x.RequestID)) {
			return n, nil
		}
		if x.Absent != (x.CurrentHash == "") || (!x.Absent && !oracleHash.MatchString(x.CurrentHash)) {
			return n, nil
		}
		patterns := map[string]bool{}
		for _, c := range s.Candidates {
			patterns[c.Pattern] = true
		}
		seen := map[string]bool{}
		size := 0
		for _, e := range x.Entries {
			if seen[e] || !patterns[e] {
				return n, nil
			}
			seen[e] = true
			size += len(e) + 1
		}
		if size > 4096 {
			return n, nil
		}
		entries := append([]string(nil), x.Entries...)
		sort.Strings(entries)
		return Noise{Repo: n.Repo, State: AwaitingConfirmation{Proposal: x.Proposal}},
			[]NoiseCommand{NoiseCreateIgnoreProposal{Proposal: x.Proposal, RequestID: x.RequestID, Entries: entries, ExpectHash: x.CurrentHash, ExpectAbsent: x.Absent}}
	case NoiseProposalDecided:
		a, ok := n.State.(AwaitingConfirmation)
		terminal := x.Status == core.ProposalExecuted || x.Status == core.ProposalRejected || x.Status == core.ProposalFailed
		if ok && a.Proposal == x.Proposal && terminal {
			return Noise{Repo: n.Repo, State: NoSuggestion{}}, []NoiseCommand{NoiseRefreshStatus{Repo: n.Repo}}
		}
	}
	return n, nil
}

// ---- generators -------------------------------------------------------------

var (
	genDirsA     = []string{"node_modules", "web/node_modules", "dist", "a b/build", "[x]/target", "日本語/bin", "obj", ".venv", "w*?/__pycache__", "#!/dist"}
	genDirsB     = []string{"dist", "build", "web/node_modules/x/build", "lib", "src", "bin"}
	genHostile   = []string{"", "/abs/dist/f", "../dist/f", "C:/dist/f", `a\dist\f`, "a/b:ads/f", "dist/f\n", "dist/f\r", ".git/hooks/x", ".GIT/x", "a /f", "a./f", "dist//f", "dist/./f", "x\x00y", string([]byte{0xff, '/', 'f'})}
	genSizes     = []int{0, 1, 49, 50, 51, 99, 100, 101, 102, 150}
	genStatuses  = []core.ProposalStatus{core.ProposalPending, core.ProposalConfirmed, core.ProposalRejected, core.ProposalExecuted, core.ProposalFailed, "bogus", ""}
	genPropIDs   = []core.ProposalID{"prp_1", "prp_2", "prp_ü"}
	genReqIDs    = []core.RequestID{"req_1", "req_2"}
	genBadIDs    = []string{"", "bad space", "a/b", `a\b`, "a\nb"}
	genEmptyHash = "e3b0c44298fc1c149afbf4c8996fb92427ae41e4649b934ca495991b7852b855"
)

func genFacts(r *rand.Rand, repo core.RepoID) NoiseFacts {
	total := genSizes[r.Intn(len(genSizes))]
	a := genDirsA[r.Intn(len(genDirsA))]
	b := genDirsB[r.Intn(len(genDirsB))]
	var inA int
	switch r.Intn(6) {
	case 0:
		inA = total
	case 1:
		inA = total / 2
	case 2:
		inA = (total + 1) / 2
	case 3:
		inA = total/2 - 1
	case 4:
		inA = 49 + r.Intn(3)
	default:
		inA = r.Intn(total + 1)
	}
	inA = max(0, min(inA, total))
	inB := 0
	if total-inA > 0 {
		inB = r.Intn(total - inA + 1)
	}
	f := NoiseFacts{Repo: repo}
	for i := 0; i < total; i++ {
		switch {
		case i < inA:
			f.Sample = append(f.Sample, fmt.Sprintf("%s/f%d", a, i))
		case i < inA+inB:
			f.Sample = append(f.Sample, fmt.Sprintf("%s/g%d", b, i))
		default:
			f.Sample = append(f.Sample, fmt.Sprintf("src/s%d", i))
		}
	}
	f.UntrackedCount = []int{total, total + 1, 99, 100, 101, 5000, total - 1}[r.Intn(7)]
	if f.UntrackedCount < 0 {
		f.UntrackedCount = 0
	}
	f.Truncated = r.Intn(3) == 0
	switch r.Intn(14) {
	case 0:
		f.Sample = append(f.Sample, genHostile[r.Intn(len(genHostile))])
	case 1:
		f.Repo = "rep_other"
	case 2:
		if len(f.Sample) > 0 {
			f.Sample = append(f.Sample, f.Sample[r.Intn(len(f.Sample))]) // duplicate
		}
	}
	r.Shuffle(len(f.Sample), func(i, j int) { f.Sample[i], f.Sample[j] = f.Sample[j], f.Sample[i] })
	return f
}

func genSelected(r *rand.Rand, n Noise) NoiseSelected {
	x := NoiseSelected{Proposal: genPropIDs[r.Intn(len(genPropIDs))], RequestID: genReqIDs[r.Intn(len(genReqIDs))]}
	if s, ok := n.State.(Suggested); ok {
		for _, c := range s.Candidates {
			if r.Intn(4) != 0 {
				x.Entries = append(x.Entries, c.Pattern)
			}
		}
	}
	switch r.Intn(10) {
	case 0:
		x.Entries = append(x.Entries, "/*")
	case 1:
		if len(x.Entries) > 0 {
			x.Entries = append(x.Entries, x.Entries[0])
		}
	case 2:
		x.Entries = nil
	case 3:
		if len(x.Entries) > 0 {
			x.Entries[0] = strings.TrimSuffix(x.Entries[0], "/")
		}
	case 4:
		x.Proposal = core.ProposalID(genBadIDs[r.Intn(len(genBadIDs))])
	case 5:
		x.RequestID = core.RequestID(genBadIDs[r.Intn(len(genBadIDs))])
	}
	switch r.Intn(8) {
	case 0, 1, 2:
		x.Absent = true
	case 3, 4:
		x.CurrentHash = strings.Repeat("a", 64)
	case 5:
		x.CurrentHash = genEmptyHash
	case 6:
		x.CurrentHash = []string{strings.Repeat("A", 64), strings.Repeat("a", 63), strings.Repeat("a", 65), strings.Repeat("g", 64), ""}[r.Intn(5)]
	case 7:
		x.Absent, x.CurrentHash = true, strings.Repeat("b", 64)
	}
	return x
}

func genInput(r *rand.Rand, n Noise) NoiseInput {
	switch k := r.Intn(12); {
	case k < 5:
		return NoiseObserved{Facts: genFacts(r, n.Repo)}
	case k < 8:
		return genSelected(r, n)
	case k < 11:
		return NoiseProposalDecided{Proposal: genPropIDs[r.Intn(len(genPropIDs))], Status: genStatuses[r.Intn(len(genStatuses))]}
	default:
		return NoiseScopeRemoved{}
	}
}

func cloneInput(in NoiseInput) NoiseInput {
	switch x := in.(type) {
	case NoiseSelected:
		x.Entries = append([]string(nil), x.Entries...)
		return x
	case NoiseObserved:
		x.Facts.Sample = append([]string(nil), x.Facts.Sample...)
		return x
	case NoiseProposalDecided, NoiseScopeRemoved:
	}
	return in
}

// ---- the property -----------------------------------------------------------

// noiseViolation replays inputs through NoiseStep and the oracle and returns
// the first disagreement or broken invariant, or "".
func noiseViolation(inputs []NoiseInput) string {
	n := Noise{Repo: "rep_1", State: NoSuggestion{}}
	m := n
	for i, raw := range inputs {
		in := cloneInput(raw)
		before := n
		var cs []NoiseCommand
		n, cs = NoiseStep(n, in)
		want, wantCs := oracleStep(m, raw)
		m = want
		at := fmt.Sprintf("step %d (%T)", i, raw)
		if !reflect.DeepEqual(n, want) || !reflect.DeepEqual(cs, wantCs) && (len(cs) != 0 || len(wantCs) != 0) {
			return fmt.Sprintf("%s: got %+v %+v, oracle %+v %+v", at, n, cs, want, wantCs)
		}
		// Invariants stated directly, so a defect shared with the model's reading
		// of the design would still have to satisfy them.
		if len(cs) > 1 {
			return at + ": more than one command"
		}
		_, observed := raw.(NoiseObserved)
		_, removed := raw.(NoiseScopeRemoved)
		if (observed || removed) && len(cs) != 0 {
			return at + ": an observation or scope removal produced a command"
		}
		for _, c := range cs {
			switch c := c.(type) {
			case NoiseCreateIgnoreProposal:
				s, ok := before.State.(Suggested)
				a, ok2 := n.State.(AwaitingConfirmation)
				if !ok || !ok2 || a.Proposal != c.Proposal {
					return at + ": proposal command outside Suggested->Awaiting"
				}
				if len(c.Entries) == 0 || len(c.Entries) > 32 || c.ExpectAbsent == (c.ExpectHash != "") || !sort.StringsAreSorted(c.Entries) {
					return at + fmt.Sprintf(": malformed command %+v", c)
				}
				size, seen := 0, map[string]bool{}
				for _, e := range c.Entries {
					size += len(e) + 1
					found := false
					for _, cand := range s.Candidates {
						found = found || cand.Pattern == e
					}
					if !found || seen[e] {
						return at + ": entry is not a unique current candidate"
					}
					seen[e] = true
				}
				if size > 4096 {
					return at + ": selection over 4 KiB"
				}
				// The caller mutating its own slice must not reach the command.
				snapshot := append([]string(nil), c.Entries...)
				if sel, ok := in.(NoiseSelected); ok {
					for j := range sel.Entries {
						sel.Entries[j] = "mutated"
					}
				}
				if !reflect.DeepEqual(snapshot, c.Entries) {
					return at + ": command aliases the caller's slice"
				}
			case NoiseRefreshStatus:
				a, ok := before.State.(AwaitingConfirmation)
				d, ok2 := raw.(NoiseProposalDecided)
				if !ok || !ok2 || a.Proposal != d.Proposal || c.Repo != n.Repo {
					return at + ": refresh without a matching decided proposal"
				}
			default:
				return at + fmt.Sprintf(": unknown command %T", c)
			}
		}
		if _, waiting := before.State.(AwaitingConfirmation); waiting {
			if _, scope := raw.(NoiseScopeRemoved); !scope && len(cs) == 0 && !reflect.DeepEqual(before, n) {
				return at + ": a pending proposal changed without a decision or scope removal"
			}
		}
	}
	return ""
}

func shrinkNoise(inputs []NoiseInput) []NoiseInput {
	for i := 0; i < len(inputs); {
		short := append(append([]NoiseInput{}, inputs[:i]...), inputs[i+1:]...)
		if noiseViolation(short) != "" {
			inputs, i = short, 0
		} else {
			i++
		}
	}
	return inputs
}

func TestPropNoiseStepMatchesIndependentModel(t *testing.T) {
	var suggested, awaiting, created, refreshed, inert int
	const seeds, steps = 250, 60
	for seed := int64(1); seed <= seeds; seed++ {
		r := rand.New(rand.NewSource(seed))
		n := Noise{Repo: "rep_1", State: NoSuggestion{}}
		var inputs []NoiseInput
		for i := 0; i < steps; i++ {
			in := genInput(r, n)
			inputs = append(inputs, in)
			next, cs := oracleStep(n, in)
			switch next.State.(type) {
			case Suggested:
				suggested++
			case AwaitingConfirmation:
				awaiting++
			case NoSuggestion:
			}
			for _, c := range cs {
				switch c.(type) {
				case NoiseCreateIgnoreProposal:
					created++
				case NoiseRefreshStatus:
					refreshed++
				}
			}
			if reflect.DeepEqual(next, n) && len(cs) == 0 {
				inert++
			}
			n = next
		}
		if v := noiseViolation(inputs); v != "" {
			min := shrinkNoise(inputs)
			t.Fatalf("seed %d: %s\nminimal failing sequence (%d inputs): %+v\nminimal violation: %s", seed, v, len(min), min, noiseViolation(min))
		}
	}
	// A property that never reaches the interesting states proves nothing.
	if suggested < 400 || awaiting < 100 || created < 50 || refreshed < 20 || inert < 1000 {
		t.Fatalf("generator is too weak: suggested=%d awaiting=%d created=%d refreshed=%d inert=%d", suggested, awaiting, created, refreshed, inert)
	}
	t.Logf("coverage: suggested=%d awaiting=%d created=%d refreshed=%d inert=%d", suggested, awaiting, created, refreshed, inert)
}

// ---- boundary parser: independent differential oracle -----------------------

func TestValidNoisePathMatchesIndependentOracle(t *testing.T) {
	segs := []string{"dist", "a", ".git", ".GIT", ".Git", "a.", "a ", " a", ".", "..", "", "x:y", `x\y`, "日本", "a\nb", "a\tb", "a\x7fb", "a\u0085b", "a\u2028b", "CON", "git~1", "..a", "a..", "-", "a b"}
	r := rand.New(rand.NewSource(7))
	accepted, rejected := 0, 0
	for i := 0; i < 20000; i++ {
		parts := make([]string, 1+r.Intn(4))
		for j := range parts {
			parts[j] = segs[r.Intn(len(segs))]
		}
		p := strings.Join(parts, "/")
		if got, want := ValidNoisePath(p), oraclePath(p); got != want {
			t.Fatalf("ValidNoisePath(%q) = %v, independent oracle says %v", p, got, want)
		}
		if oraclePath(p) {
			accepted++
		} else {
			rejected++
		}
	}
	for _, p := range []string{strings.Repeat("x", 4096), strings.Repeat("x", 4097), "a/" + strings.Repeat("é", 2048), "a/" + strings.Repeat("é", 2047), string([]byte{'a', 0xc3}), "a\x00"} {
		if ValidNoisePath(p) != oraclePath(p) {
			t.Errorf("ValidNoisePath(len %d) disagrees with the oracle", len(p))
		}
	}
	if accepted < 500 || rejected < 500 {
		t.Fatalf("generator is lopsided: %d accepted, %d rejected", accepted, rejected)
	}
}

func FuzzValidNoisePathOracle(f *testing.F) {
	for _, p := range []string{"dist/a", "a[?*]/build/f", "../x", "C:/a", "a\n", ".GIT/x", "a /b", "a./b", "a/b\x00", string([]byte{0xff}), "日本語/dist/f", `a\b`, "a//b"} {
		f.Add(p)
	}
	f.Fuzz(func(t *testing.T, p string) {
		if got, want := ValidNoisePath(p), oraclePath(p); got != want {
			t.Fatalf("ValidNoisePath(%q) = %v, oracle %v", p, got, want)
		}
	})
}

// A fuzzed sample is either rejected by the independent rules and inert, or
// analysed exactly like the model analyses it.
func FuzzNoiseObservedMatchesOracle(f *testing.F) {
	var seed []string
	for i := 0; i < 60; i++ {
		seed = append(seed, fmt.Sprintf("web/node_modules/f%d", i))
	}
	f.Add(strings.Join(seed, "\n"), 100, false)
	f.Add(strings.Join(seed, "\n"), 59, true)
	f.Add("dist/a\ndist/a", 5, false)
	f.Add("../x\ndist/a", 150, true)
	f.Fuzz(func(t *testing.T, raw string, count int, truncated bool) {
		facts := NoiseFacts{Repo: "rep_1", UntrackedCount: count, Truncated: truncated}
		if raw != "" {
			facts.Sample = strings.Split(raw, "\n")
		}
		start := Noise{Repo: "rep_1", State: NoSuggestion{}}
		got, cs := NoiseStep(start, NoiseObserved{Facts: facts})
		want, _ := oracleStep(start, NoiseObserved{Facts: facts})
		if len(cs) != 0 || !reflect.DeepEqual(got, want) {
			t.Fatalf("step = %+v %v, oracle %+v", got, cs, want)
		}
		if (facts.Validate() == nil) != oracleFactsValid(facts, "rep_1") {
			t.Fatalf("Validate disagrees with the oracle for %q count=%d truncated=%v", raw, count, truncated)
		}
	})
}
