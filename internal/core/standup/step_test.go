package standup

import (
	"fmt"
	"math/rand"
	"reflect"
	"slices"
	"strings"
	"testing"
	"time"
	"unicode/utf8"

	"github.com/brendatamaar/yggdrasil/internal/core"
)

var (
	t0   = time.Date(2026, 11, 16, 0, 0, 0, 0, time.UTC)
	rng0 = Range{Project: "prj_1", From: t0, To: t0.Add(24 * time.Hour)}
)

func loading(t *testing.T) Standup {
	t.Helper()
	s, cmds := Step(Standup{State: Empty{}}, Requested{Range: rng0, Now: t0})
	if len(cmds) != 1 {
		t.Fatalf("cmds = %v", cmds)
	}
	return s
}

func commit(repo string, i int, at time.Time) Commit {
	return Commit{Repo: core.RepoID("rep_" + repo), RepoName: repo, OID: fmt.Sprintf("%040x", i), Subject: fmt.Sprintf("change %d", i), Author: "Ada Test", At: at}
}

func TestStepRequestedStartsRead(t *testing.T) {
	s, cmds := Step(Standup{State: Empty{}}, Requested{Range: rng0, Now: t0})
	l, ok := s.State.(Loading)
	if !ok || l.Attempt != 1 || s.Seq != 1 {
		t.Fatalf("state = %+v", s)
	}
	if want := []Command{ReadSources{Attempt: 1, Range: rng0}}; !reflect.DeepEqual(cmds, want) {
		t.Errorf("cmds = %v, want %v", cmds, want)
	}
	// A second request cancels the first and wins.
	s2, cmds := Step(s, Requested{Range: rng0, Now: t0})
	if s2.State.(Loading).Attempt != 2 || len(cmds) != 2 || cmds[0] != (CancelRead{Attempt: 1}) {
		t.Errorf("second request: %+v %v", s2, cmds)
	}
	// The first attempt's result is now stale.
	got, cmds := Step(s2, SourcesRead{Attempt: 1, Readable: 3})
	if !reflect.DeepEqual(got, s2) || len(cmds) != 0 {
		t.Errorf("stale result applied: %+v %v", got, cmds)
	}
}

func TestStepInvalidRangeUnchanged(t *testing.T) {
	s := Standup{State: Empty{}}
	for _, r := range []Range{{}, {Project: "prj_1", From: t0, To: t0}, {Project: "a b", From: t0, To: t0.Add(time.Hour)}, {Project: "prj_1", From: t0, To: t0.Add(40 * 24 * time.Hour)}} {
		got, cmds := Step(s, Requested{Range: r, Now: t0})
		if !reflect.DeepEqual(got, s) || len(cmds) != 0 {
			t.Errorf("range %+v: %+v %v", r, got, cmds)
		}
	}
}

func TestStandupDateRangeAndEmpty(t *testing.T) {
	s := loading(t)
	// Empty data is a Ready result, not a failure; every source says "none".
	got, cmds := Step(s, SourcesRead{Attempt: 1, Readable: 3})
	r, ok := got.State.(Ready)
	if !ok || len(cmds) != 1 || cmds[0] != (ReturnSummary{Attempt: 1}) {
		t.Fatalf("empty read: %+v %v", got, cmds)
	}
	for _, want := range []string{"Commits: none in this range.", "Merge requests: none in this range.", "Builds: none in this range."} {
		if !strings.Contains(r.Summary.Text, want) {
			t.Errorf("text lacks %q:\n%s", want, r.Summary.Text)
		}
	}
	// Records on the edges: From is included, To is excluded; outside is dropped.
	in := SourcesRead{Attempt: 1, Readable: 3, Commits: []Commit{
		commit("a", 1, rng0.From), commit("a", 2, rng0.To.Add(-time.Nanosecond)), commit("a", 3, rng0.To), commit("a", 4, rng0.From.Add(-time.Nanosecond)), commit("a", 5, time.Time{}),
	}}
	got, _ = Step(s, in)
	if n := len(got.State.(Ready).Summary.Commits); n != 2 {
		t.Errorf("commits in [from,to) = %d, want 2", n)
	}
}

func TestStepFailureAndCancel(t *testing.T) {
	s := loading(t)
	got, cmds := Step(s, ReadFailed{Attempt: 1, Reason: "disk\nunreadable"})
	f, ok := got.State.(Failed)
	if !ok || f.Reason != "disk unreadable" || !reflect.DeepEqual(cmds, []Command{Explain{Reason: "disk unreadable"}}) {
		t.Errorf("failed: %+v %v", got, cmds)
	}
	got, cmds = Step(s, Cancelled{Attempt: 1})
	if _, ok := got.State.(Empty); !ok || !reflect.DeepEqual(cmds, []Command{CancelRead{Attempt: 1}}) {
		t.Errorf("cancelled: %+v %v", got, cmds)
	}
	// All sources wholly unavailable is Failed with no summary.
	all := SourcesRead{Attempt: 1, Unavailable: []Unavailable{{Source: SourceCommits, Reason: "x"}, {Source: SourceMRs, Reason: "x"}, {Source: SourceBuilds, Reason: "x"}}}
	if got, _ := Step(s, all); !isFailed(got.State) {
		t.Errorf("all unavailable = %+v", got.State)
	}
	// A result whose Readable disagrees with its labels is refused, not trusted.
	if got, _ := Step(s, SourcesRead{Attempt: 1, Readable: 3, Unavailable: []Unavailable{{Source: SourceMRs, Reason: "x"}}}); !isFailed(got.State) {
		t.Errorf("inconsistent = %+v", got.State)
	}
	if got, _ := Step(s, SourcesRead{Attempt: 1, Readable: 3, Unavailable: []Unavailable{{Source: "bogus"}}}); !isFailed(got.State) {
		t.Errorf("bogus source = %+v", got.State)
	}
}

func isFailed(s State) bool { _, ok := s.(Failed); return ok }

func TestStepUnhandledPairsUnchanged(t *testing.T) {
	inputs := []Input{SourcesRead{Attempt: 1, Readable: 3}, ReadFailed{Attempt: 1}, Cancelled{Attempt: 1}, PolishRequested{RequestID: "r1"}}
	for _, st := range []State{Empty{}, Ready{}, Partial{}, Failed{Reason: "x"}} {
		for _, in := range inputs {
			if _, ok := st.(Ready); ok {
				if _, isPolish := in.(PolishRequested); isPolish {
					continue
				}
			}
			if _, ok := st.(Partial); ok {
				if _, isPolish := in.(PolishRequested); isPolish {
					continue
				}
			}
			s := Standup{Seq: 1, State: st}
			got, cmds := Step(s, in)
			if !reflect.DeepEqual(got, s) || len(cmds) != 0 {
				t.Errorf("%T with %T: %+v %v", st, in, got, cmds)
			}
		}
	}
}

func TestStepPolish(t *testing.T) {
	ready := Standup{Seq: 1, State: Ready{Summary: Summary{Text: "hello"}}}
	got, cmds := Step(ready, PolishRequested{RequestID: "r1"})
	if !reflect.DeepEqual(got, ready) || !reflect.DeepEqual(cmds, []Command{SubmitAskDraft{RequestID: "r1", Text: "hello"}}) {
		t.Errorf("polish = %+v %v", got, cmds)
	}
	if _, cmds := Step(ready, PolishRequested{RequestID: "a b"}); len(cmds) != 0 {
		t.Errorf("invalid request id polished: %v", cmds)
	}
	if _, cmds := Step(Standup{State: Ready{}}, PolishRequested{RequestID: "r1"}); len(cmds) != 0 {
		t.Errorf("empty text polished: %v", cmds)
	}
	if _, cmds := Step(loading(t), PolishRequested{RequestID: "r1"}); len(cmds) != 0 {
		t.Errorf("polish while loading: %v", cmds)
	}
}

// ---- generated facts ----

func genFacts(r *rand.Rand, in SourcesRead) SourcesRead {
	repos := 1 + r.Intn(4)
	n := r.Intn(30)
	for i := 0; i < n; i++ {
		repo := fmt.Sprintf("r%d", r.Intn(repos))
		at := rng0.From.Add(time.Duration(r.Intn(26*60)-60) * time.Minute) // some outside the range
		switch r.Intn(3) {
		case 0:
			in.Commits = append(in.Commits, commit(repo, r.Intn(20), at))
		case 1:
			in.MRs = append(in.MRs, MR{Repo: core.RepoID("rep_" + repo), RepoName: repo, Number: 1 + r.Intn(10), Title: "mr", State: "open", At: at})
		default:
			in.Builds = append(in.Builds, Build{Repo: core.RepoID("rep_" + repo), RepoName: repo, ID: core.BuildID(fmt.Sprintf("bld_%d", r.Intn(15))), Status: core.BuildSucceeded, At: at})
		}
	}
	return in
}

func shuffled(r *rand.Rand, in SourcesRead) SourcesRead {
	out := in
	out.Commits = slices.Clone(in.Commits)
	out.MRs = slices.Clone(in.MRs)
	out.Builds = slices.Clone(in.Builds)
	out.Unavailable = slices.Clone(in.Unavailable)
	r.Shuffle(len(out.Commits), func(i, j int) { out.Commits[i], out.Commits[j] = out.Commits[j], out.Commits[i] })
	r.Shuffle(len(out.MRs), func(i, j int) { out.MRs[i], out.MRs[j] = out.MRs[j], out.MRs[i] })
	r.Shuffle(len(out.Builds), func(i, j int) { out.Builds[i], out.Builds[j] = out.Builds[j], out.Builds[i] })
	r.Shuffle(len(out.Unavailable), func(i, j int) { out.Unavailable[i], out.Unavailable[j] = out.Unavailable[j], out.Unavailable[i] })
	return out
}

func genUnavailable(r *rand.Rand) ([]Unavailable, int) {
	var u []Unavailable
	whole := 0
	for _, k := range sourceOrder {
		switch r.Intn(4) {
		case 0:
			u = append(u, Unavailable{Source: k, Reason: "timed out"})
			whole++
		case 1:
			u = append(u, Unavailable{Source: k, Repo: core.RepoID(fmt.Sprintf("rep_r%d", r.Intn(3))), Reason: "history unavailable"})
		}
	}
	return u, whole
}

func TestPropSummaryDeterministic(t *testing.T) {
	for seed := int64(0); seed < 300; seed++ {
		r := rand.New(rand.NewSource(seed))
		un, whole := genUnavailable(r)
		if whole == 3 {
			continue
		}
		in := genFacts(r, SourcesRead{Attempt: 1, Readable: 3 - whole, Unavailable: un})
		// Duplicates must not change the result either.
		in.Commits = append(in.Commits, in.Commits...)
		a, ua, fa := BuildSummary(rng0, in)
		for k := 0; k < 4; k++ {
			b, ub, fb := BuildSummary(rng0, shuffled(r, in))
			if !reflect.DeepEqual(a, b) || !reflect.DeepEqual(ua, ub) || fa != fb || a.Text != b.Text {
				t.Fatalf("seed %d: order changed the summary:\n%s\n---\n%s", seed, a.Text, b.Text)
			}
		}
		if !slices.IsSortedFunc(a.Commits, cmpCommit) || !slices.IsSortedFunc(a.MRs, cmpMR) || !slices.IsSortedFunc(a.Builds, cmpBuild) {
			t.Fatalf("seed %d: summary lists are not in the documented order", seed)
		}
		// Format agrees with the summary's own order, whatever the slice order.
		sh := a
		sh.Commits = slices.Clone(a.Commits)
		slices.Reverse(sh.Commits)
		if Format(sh) != Format(a) {
			t.Fatalf("seed %d: Format depends on slice order", seed)
		}
	}
}

func TestPropPartialNeverFabricated(t *testing.T) {
	for seed := int64(0); seed < 300; seed++ {
		r := rand.New(rand.NewSource(seed))
		un, whole := genUnavailable(r)
		if whole == 3 {
			continue
		}
		// Supply data of every kind for every repo, including the unavailable ones.
		in := genFacts(r, SourcesRead{Attempt: 1, Readable: 3 - whole, Unavailable: un})
		sum, labels, failure := BuildSummary(rng0, in)
		if failure != "" {
			t.Fatalf("seed %d: %s", seed, failure)
		}
		if len(un) > 0 && len(labels) == 0 {
			t.Fatalf("seed %d: labels silently dropped", seed)
		}
		wholeK := map[SourceKind]bool{}
		for _, u := range labels {
			if u.Repo == "" {
				wholeK[u.Source] = true
			}
		}
		for _, c := range sum.Commits {
			if wholeK[SourceCommits] || hasLabel(labels, SourceCommits, c.Repo) {
				t.Fatalf("seed %d: commit of an unavailable source shown: %+v", seed, c)
			}
		}
		for _, m := range sum.MRs {
			if wholeK[SourceMRs] || hasLabel(labels, SourceMRs, m.Repo) {
				t.Fatalf("seed %d: MR of an unavailable source shown: %+v", seed, m)
			}
		}
		for _, b := range sum.Builds {
			if wholeK[SourceBuilds] || hasLabel(labels, SourceBuilds, b.Repo) {
				t.Fatalf("seed %d: build of an unavailable source shown: %+v", seed, b)
			}
		}
		// The text says so, and never claims "none" for a wholly missing source.
		for _, u := range labels {
			if !strings.Contains(sum.Text, u.Reason) {
				t.Fatalf("seed %d: text does not label %+v:\n%s", seed, u, sum.Text)
			}
		}
		for k := range wholeK {
			if strings.Contains(sum.Text, sourceTitle(k)+": none") {
				t.Fatalf("seed %d: text claims none for unavailable %s", seed, k)
			}
			if !strings.Contains(sum.Text, sourceTitle(k)+": not read.") {
				t.Fatalf("seed %d: text lacks 'not read' for %s", seed, k)
			}
		}
		// State: Partial exactly when something is labelled.
		st, _ := Step(loadingWith(seed), in)
		switch st.State.(type) {
		case Partial:
			if len(labels) == 0 {
				t.Fatalf("seed %d: Partial without labels", seed)
			}
		case Ready:
			if len(labels) != 0 {
				t.Fatalf("seed %d: Ready with labels", seed)
			}
		case Empty, Loading, Failed:
			t.Fatalf("seed %d: state %T", seed, st.State)
		}
	}
}

func loadingWith(int64) Standup {
	s, _ := Step(Standup{State: Empty{}}, Requested{Range: rng0, Now: t0})
	return s
}

func hasLabel(us []Unavailable, k SourceKind, repo core.RepoID) bool {
	for _, u := range us {
		if u.Source == k && u.Repo == repo {
			return true
		}
	}
	return false
}

func TestPropStandupWithinBounds(t *testing.T) {
	for seed := int64(0); seed < 40; seed++ {
		r := rand.New(rand.NewSource(seed))
		var in SourcesRead
		in.Attempt, in.Readable = 1, 3
		nRepos := 1 + r.Intn(MaxRepos+30)
		for i := 0; i < 700; i++ {
			repo := fmt.Sprintf("r%03d", r.Intn(nRepos))
			at := rng0.From.Add(time.Duration(r.Intn(24*60)) * time.Minute)
			long := strings.Repeat("é", r.Intn(400))
			c := commit(repo, i, at)
			c.Subject = long
			in.Commits = append(in.Commits, c)
			in.MRs = append(in.MRs, MR{Repo: core.RepoID("rep_" + repo), RepoName: repo, Number: i + 1, Title: long, State: "merged", At: at})
			in.Builds = append(in.Builds, Build{Repo: core.RepoID("rep_" + repo), RepoName: repo, ID: core.BuildID(fmt.Sprintf("bld_%d", i)), Status: core.BuildFailed, At: at})
		}
		sum, _, failure := BuildSummary(rng0, in)
		if failure != "" {
			t.Fatal(failure)
		}
		if len(sum.Commits) > MaxRecordsPerSource || len(sum.MRs) > MaxRecordsPerSource || len(sum.Builds) > MaxRecordsPerSource {
			t.Fatalf("seed %d: record cap exceeded: %d %d %d", seed, len(sum.Commits), len(sum.MRs), len(sum.Builds))
		}
		repos := map[core.RepoID]bool{}
		for _, c := range sum.Commits {
			repos[c.Repo] = true
		}
		for _, m := range sum.MRs {
			repos[m.Repo] = true
		}
		for _, b := range sum.Builds {
			repos[b.Repo] = true
		}
		if len(repos) > MaxRepos {
			t.Fatalf("seed %d: %d repos", seed, len(repos))
		}
		if len(sum.Text) > MaxTextBytes || !utf8.ValidString(sum.Text) {
			t.Fatalf("seed %d: text %d bytes valid=%v", seed, len(sum.Text), utf8.ValidString(sum.Text))
		}
		if len(sum.Truncated) == 0 {
			t.Fatalf("seed %d: nothing marked truncated despite 700 records per source", seed)
		}
		if !slices.IsSortedFunc(sum.Commits, cmpCommit) || !slices.IsSortedFunc(sum.MRs, cmpMR) || !slices.IsSortedFunc(sum.Builds, cmpBuild) {
			t.Fatalf("seed %d: not sorted", seed)
		}
	}
}

func TestTextLimitCutsWholeLinesAndSaysSo(t *testing.T) {
	in := SourcesRead{Attempt: 1, Readable: 3}
	for i := 0; i < 200; i++ {
		c := commit("big", i, rng0.From.Add(time.Duration(i)*time.Minute))
		c.Subject = strings.Repeat("x", 190)
		c.Author = strings.Repeat("y", 90)
		in.Commits = append(in.Commits, c)
		in.MRs = append(in.MRs, MR{Repo: "rep_big", RepoName: "big", Number: i + 1, Title: strings.Repeat("t", 190), State: "open", At: rng0.From.Add(time.Duration(i) * time.Minute)})
	}
	sum, _, _ := BuildSummary(rng0, in)
	if len(sum.Text) > MaxTextBytes {
		t.Fatalf("text = %d bytes", len(sum.Text))
	}
	if !strings.HasSuffix(sum.Text, textFooter) {
		t.Errorf("text does not say it was cut")
	}
	if !slices.Contains(sum.Truncated, SourceMRs) {
		t.Errorf("truncated = %v, want merge requests listed", sum.Truncated)
	}
}

// Invariants after every step of a random input sequence: the sequence number
// never decreases, a Loading state carries the latest attempt, SubmitAskDraft
// only follows PolishRequested from Ready or Partial, and a refresh input never
// produces one.
func TestPropPolishOnlyOnExplicitInput(t *testing.T) {
	for seed := int64(0); seed < 300; seed++ {
		r := rand.New(rand.NewSource(seed))
		s := Standup{State: Empty{}}
		for i := 0; i < 40; i++ {
			var in Input
			attempt := s.Seq - r.Intn(2)
			switch r.Intn(6) {
			case 0:
				in = Requested{Range: rng0, Now: t0}
			case 1:
				in = SourcesRead{Attempt: attempt, Readable: 3, Commits: genFacts(r, SourcesRead{}).Commits}
			case 2:
				in = ReadFailed{Attempt: attempt, Reason: "x"}
			case 3:
				in = Cancelled{Attempt: attempt}
			case 4:
				in = PolishRequested{RequestID: core.RequestID(fmt.Sprintf("req_%d", r.Intn(3)))}
			default:
				in = Requested{Range: Range{}, Now: t0}
			}
			before := s
			var cmds []Command
			s, cmds = Step(s, in)
			if s.Seq < before.Seq {
				t.Fatalf("seed %d: Seq decreased", seed)
			}
			if l, ok := s.State.(Loading); ok && l.Attempt != s.Seq {
				t.Fatalf("seed %d: Loading(%d) with Seq %d", seed, l.Attempt, s.Seq)
			}
			for _, c := range cmds {
				sub, ok := c.(SubmitAskDraft)
				if !ok {
					continue
				}
				if _, isPolish := in.(PolishRequested); !isPolish {
					t.Fatalf("seed %d: %T produced SubmitAskDraft", seed, in)
				}
				switch before.State.(type) {
				case Ready, Partial:
				case Empty, Loading, Failed:
					t.Fatalf("seed %d: SubmitAskDraft from %T", seed, before.State)
				}
				if sub.Text == "" || sub.RequestID.Validate() != nil {
					t.Fatalf("seed %d: bad draft %+v", seed, sub)
				}
			}
		}
	}
}

func FuzzParseRange(f *testing.F) {
	f.Add("prj_1", int64(0), int64(3600))
	f.Add("a b", int64(5), int64(1))
	f.Add("", int64(-1<<40), int64(1<<40))
	f.Fuzz(func(t *testing.T, project string, from, to int64) {
		a, b := time.Unix(from, 0), time.Unix(to, 0)
		r, err := ParseRange(core.ProjectID(project), a, b)
		if err != nil {
			return
		}
		// Accepted implies valid.
		if r.Project.Validate() != nil || !r.From.Before(r.To) || r.To.Sub(r.From) > MaxRangeDays*24*time.Hour ||
			r.From.Location() != time.UTC || r.To.Location() != time.UTC || !r.From.Equal(a) || !r.To.Equal(b) {
			t.Fatalf("accepted an invalid range: %+v", r)
		}
		// Round trip: a parsed range parses to itself.
		if again, err := ParseRange(r.Project, r.From, r.To); err != nil || again != r {
			t.Fatalf("round trip: %+v, %v", again, err)
		}
	})
}
