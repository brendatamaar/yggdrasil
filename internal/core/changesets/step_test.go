package changesets

import (
	"math/rand"
	"reflect"
	"slices"
	"strings"
	"testing"

	"github.com/brendatamaar/yggdrasil/internal/core"
)

// The property generator must actually reach the interesting transitions, or
// the properties above would pass vacuously.
func TestGeneratorCoverage(t *testing.T) {
	seen := map[string]int{}
	forSeeds(t, func(t *testing.T, r *rand.Rand) {
		run(t, r, func(t *testing.T, before State, in Input, after State, cmds []Command) {
			if !changedState(before, after) {
				seen["no change"]++
				return
			}
			seen[reflect.TypeOf(before).Name()+"->"+reflect.TypeOf(after).Name()]++
			if count[ConfirmProposal](cmds) > 0 {
				seen["confirm via "+reflect.TypeOf(in).Name()]++
			}
			if h, ok := open(after).LastRun.(RunHalted); ok {
				seen["halted: "+h.Reason]++
			}
			if _, ok := open(after).LastRun.(RunCompleted); ok {
				seen["completed"]++
			}
		})
	})
	for _, k := range []string{"Open->Confirming", "Confirming->Confirming", "Confirming->Open", "Open->Shipped", "Open->Closed", "Shipped->Open", "Closed->Open", "confirm via MembersChanged", "confirm via ConfirmRun", "completed", "halted: a proposal failed", "halted: the run was stopped"} {
		if seen[k] == 0 {
			t.Errorf("generator never produced %q", k)
		}
	}
}

func hashOf(c string) string { return strings.Repeat(c, 64) }

func mem(id string, repo core.RepoID, kind core.ProposalKind, st core.ProposalStatus, h string) Member {
	return Member{ID: core.ProposalID(id), Repo: repo, Kind: kind, Status: st, Hash: hashOf(h)}
}

func setWith(order []core.RepoID, ms ...Member) Open {
	o := newSet(order...)
	o.Members = ms
	return o
}

func TestPlanOrderAndKinds(t *testing.T) {
	s := setWith([]core.RepoID{"b", "a", "c"},
		mem("a-mr", "a", core.ProposalMergeRequest, core.ProposalPending, "1"),
		mem("a-push", "a", core.ProposalPush, core.ProposalPending, "2"),
		mem("a-commit", "a", core.ProposalCommit, core.ProposalPending, "3"),
		mem("a-install", "a", core.ProposalInstall, core.ProposalPending, "4"),
		mem("b-commit", "b", core.ProposalCommit, core.ProposalPending, "5"),
		mem("b-done", "b", core.ProposalPush, core.ProposalExecuted, "6"),
		mem("c-local", "c", core.ProposalMergeLocal, core.ProposalPending, "7"),
		mem("c-wt", "c", core.ProposalRemoveWorktree, core.ProposalPending, "8"),
		mem("c-pub", "c", core.ProposalPublish, core.ProposalPending, "9"),
	)
	var ids []string
	for _, p := range Plan(s, ScopeAll{}) {
		ids = append(ids, string(p.ID))
	}
	want := []string{"b-commit", "a-install", "a-commit", "a-push", "a-mr"}
	if !slices.Equal(ids, want) {
		t.Fatalf("plan = %v, want %v", ids, want)
	}
	if got := Plan(s, ScopeRepo{Repo: "b"}); len(got) != 1 || got[0].ID != "b-commit" {
		t.Fatalf("repo plan = %v", got)
	}
	if got := Plan(s, nil); len(got) != 0 {
		t.Fatalf("nil scope plan = %v", got)
	}
}

func TestPlanRepoMissingFromOrderGoesLast(t *testing.T) {
	s := setWith(nil,
		mem("p1", "c", core.ProposalCommit, core.ProposalPending, "1"),
		mem("p2", "a", core.ProposalCommit, core.ProposalPending, "2"))
	s.Set.Order = []core.RepoID{"c", "b", "a"}
	if got := Plan(s, ScopeAll{}); got[0].ID != "p1" {
		t.Fatalf("plan = %v", got)
	}
	s.Set.Order = []core.RepoID{"a"} // invalid for StepSet, but Plan still sorts the unlisted last
	if got := Plan(s, ScopeAll{}); got[0].ID != "p2" || got[1].ID != "p1" {
		t.Fatalf("plan = %v", got)
	}
}

func seenFor(s State, sc Scope) map[core.ProposalID]string {
	out := map[core.ProposalID]string{}
	for _, p := range Plan(s, sc) {
		out[p.ID] = p.Hash
	}
	return out
}

func TestConfirmRunGuards(t *testing.T) {
	base := setWith([]core.RepoID{"a", "b", "c"},
		mem("p1", "a", core.ProposalCommit, core.ProposalPending, "1"),
		mem("p2", "a", core.ProposalPush, core.ProposalPending, "2"),
		mem("p3", "b", core.ProposalCommit, core.ProposalPending, "3"))
	good := func() ConfirmRun {
		return ConfirmRun{Scope: ScopeAll{}, Seen: seenFor(base, ScopeAll{}), Now: epoch}
	}
	tests := []struct {
		name string
		mut  func(*ConfirmRun)
		ok   bool
	}{
		{"all guards hold", func(*ConfirmRun) {}, true},
		{"hash differs", func(c *ConfirmRun) { c.Seen["p2"] = hashOf("f") }, false},
		{"member missing from seen", func(c *ConfirmRun) { delete(c.Seen, "p3") }, false},
		{"extra seen entry", func(c *ConfirmRun) { c.Seen["p9"] = hashOf("1") }, false},
		{"nil scope", func(c *ConfirmRun) { c.Scope = nil }, false},
		{"scope repo not in set", func(c *ConfirmRun) { c.Scope = ScopeRepo{Repo: "zz"} }, false},
		{"scope with an empty plan", func(c *ConfirmRun) { c.Scope = ScopeRepo{Repo: "c"}; c.Seen = map[core.ProposalID]string{} }, false},
		{"repo scope", func(c *ConfirmRun) {
			c.Scope = ScopeRepo{Repo: "b"}
			c.Seen = map[core.ProposalID]string{"p3": hashOf("3")}
		}, true},
		{"repo scope with all seen", func(c *ConfirmRun) { c.Scope = ScopeRepo{Repo: "b"} }, false},
	}
	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			in := good()
			tc.mut(&in)
			got, cmds := StepSet(base, in)
			_, running := got.(Confirming)
			if running != tc.ok {
				t.Fatalf("running = %v, want %v", running, tc.ok)
			}
			if !tc.ok && len(cmds) != 0 {
				t.Fatalf("refused run emitted %v", cmds)
			}
		})
	}
	t.Run("first confirm", func(t *testing.T) {
		got, cmds := StepSet(base, good())
		c := got.(Confirming)
		if c.Current != "p1" || len(c.Queue) != 2 || c.Queue[0].ID != "p2" {
			t.Fatalf("run = %+v", c)
		}
		if last, ok := cmds[len(cmds)-1].(ConfirmProposal); !ok || last.ID != "p1" || last.Hash != hashOf("1") {
			t.Fatalf("commands = %v", cmds)
		}
	})
}

func TestConfirmRunNeedsAck(t *testing.T) {
	// a depends on b, but the order puts a first.
	s := setWith([]core.RepoID{"a", "b", "c"},
		mem("p1", "a", core.ProposalCommit, core.ProposalPending, "1"),
		mem("p2", "b", core.ProposalCommit, core.ProposalPending, "2"))
	deps := []Edge{{Dependent: "a", Dependency: "b"}}
	in := ConfirmRun{Scope: ScopeAll{}, Seen: seenFor(s, ScopeAll{}), Deps: deps, Now: epoch}
	if w := Preflight(s, ScopeAll{}, deps); len(w) != 1 || w[0].Dependent != "a" || w[0].Dependency != "b" {
		t.Fatalf("warnings = %v", w)
	}
	if got, cmds := StepSet(s, in); !reflect.DeepEqual(got, State(s)) || len(cmds) != 0 {
		t.Fatalf("started without acknowledging the warning")
	}
	in.Ack = []WarningCode{WarningDependentBeforeDependency}
	got, cmds := StepSet(s, in)
	if _, ok := got.(Confirming); !ok {
		t.Fatal("did not start after acknowledging")
	}
	if a := cmds[0].(WriteAudit); !strings.Contains(a.Outcome, string(WarningDependentBeforeDependency)) {
		t.Fatalf("acknowledgement not audited: %+v", a)
	}
	// Correct order: no warning.
	s.Set.Order = []core.RepoID{"b", "a", "c"}
	if w := Preflight(s, ScopeAll{}, deps); len(w) != 0 {
		t.Fatalf("warnings = %v", w)
	}
}

func TestPreflightOutOfPlanDependency(t *testing.T) {
	s := setWith([]core.RepoID{"a", "b", "c"},
		mem("p1", "a", core.ProposalCommit, core.ProposalPending, "1"),
		mem("p2", "b", core.ProposalCommit, core.ProposalPending, "2"),
		mem("p3", "b", core.ProposalPush, core.ProposalExecuted, "3"))
	deps := []Edge{{Dependent: "a", Dependency: "b"}, {Dependent: "a", Dependency: "a"}, {Dependent: "a", Dependency: "zz"}, {Dependent: "a", Dependency: "b"}}
	if w := Preflight(s, ScopeRepo{Repo: "a"}, deps); len(w) != 1 {
		t.Fatalf("scope a: warnings = %v", w)
	}
	if w := Preflight(s, ScopeRepo{Repo: "b"}, deps); len(w) != 0 {
		t.Fatalf("scope b: warnings = %v", w)
	}
}

func TestRunProgress(t *testing.T) {
	s := setWith([]core.RepoID{"a", "b", "c"},
		mem("p1", "a", core.ProposalCommit, core.ProposalPending, "1"),
		mem("p2", "a", core.ProposalPush, core.ProposalPending, "2"))
	run1, _ := StepSet(s, ConfirmRun{Scope: ScopeAll{}, Seen: seenFor(s, ScopeAll{}), Now: epoch})
	step := func(st State, p1, p2 core.ProposalStatus) (State, []Command) {
		return StepSet(st, MembersChanged{Members: []Member{
			mem("p1", "a", core.ProposalCommit, p1, "1"), mem("p2", "a", core.ProposalPush, p2, "2")}, Now: epoch})
	}
	// Current confirmed: wait.
	waiting, cmds := step(run1, core.ProposalConfirmed, core.ProposalPending)
	if _, ok := waiting.(Confirming); !ok || count[ConfirmProposal](cmds) != 0 {
		t.Fatal("run did not wait for the current proposal")
	}
	// Same delivery again: no change.
	if again, cmds := step(waiting, core.ProposalConfirmed, core.ProposalPending); !reflect.DeepEqual(again, waiting) || len(cmds) != 0 {
		t.Fatal("duplicate delivery changed the run")
	}
	// Executed: confirm the next one.
	next, cmds := step(waiting, core.ProposalExecuted, core.ProposalPending)
	if c := next.(Confirming); c.Current != "p2" || len(c.Queue) != 0 {
		t.Fatalf("run = %+v", c)
	}
	if cp, ok := cmds[len(cmds)-1].(ConfirmProposal); !ok || cp.ID != "p2" || cp.Hash != hashOf("2") {
		t.Fatalf("commands = %v", cmds)
	}
	// Last one executed: complete, and with everything executed the set ships.
	done, _ := step(next, core.ProposalExecuted, core.ProposalExecuted)
	if sh, ok := done.(Shipped); !ok || sh.LastRun != (RunCompleted{}) {
		t.Fatalf("state = %#v", done)
	}
	// The next member was edited meanwhile: halt there.
	changed, cmds := StepSet(waiting, MembersChanged{Members: []Member{
		mem("p1", "a", core.ProposalCommit, core.ProposalExecuted, "1"), mem("p2", "a", core.ProposalPush, core.ProposalPending, "9")}, Now: epoch})
	if h, ok := changed.(Open).LastRun.(RunHalted); !ok || h.At != "p2" || count[ConfirmProposal](cmds) != 0 {
		t.Fatalf("state = %#v", changed)
	}
	// Current failed: halt, set stays open.
	failed, _ := step(waiting, core.ProposalFailed, core.ProposalPending)
	if h, ok := failed.(Open).LastRun.(RunHalted); !ok || h.At != "p1" {
		t.Fatalf("state = %#v", failed)
	}
	// Current vanished: halt.
	gone, _ := StepSet(waiting, MembersChanged{Members: []Member{mem("p2", "a", core.ProposalPush, core.ProposalPending, "2")}, Now: epoch})
	if _, ok := gone.(Open); !ok {
		t.Fatalf("state = %#v", gone)
	}
}

func TestStopAndRefused(t *testing.T) {
	s := setWith(nil, mem("p1", "a", core.ProposalCommit, core.ProposalPending, "1"))
	running, _ := StepSet(s, ConfirmRun{Scope: ScopeAll{}, Seen: seenFor(s, ScopeAll{}), Now: epoch})
	stopped, cmds := StepSet(running, StopRun{Now: epoch})
	if h := stopped.(Open).LastRun.(RunHalted); h.At != "p1" || h.Reason == "" || len(cmds) != 2 {
		t.Fatalf("stopped = %#v", stopped)
	}
	refused, _ := StepSet(running, ConfirmRefused{ID: "p1", Reason: "the hash\nchanged", Now: epoch})
	if h := refused.(Open).LastRun.(RunHalted); strings.ContainsAny(h.Reason, "\n") || h.Reason == "" {
		t.Fatalf("reason = %q", h.Reason)
	}
	other, cmds := StepSet(running, ConfirmRefused{ID: "p7", Reason: "x", Now: epoch})
	if !reflect.DeepEqual(other, running) || len(cmds) != 0 {
		t.Fatal("a refusal for another proposal stopped the run")
	}
	// Not running: stop and refusal do nothing.
	for _, in := range []Input{StopRun{}, ConfirmRefused{ID: "p1", Reason: "x"}} {
		if got, cmds := StepSet(s, in); !reflect.DeepEqual(got, State(s)) || len(cmds) != 0 {
			t.Fatalf("%T changed an idle set", in)
		}
	}
}

func TestSetOrderAndClose(t *testing.T) {
	s := setWith([]core.RepoID{"a", "b", "c"}, mem("p1", "a", core.ProposalCommit, core.ProposalPending, "1"))
	got, cmds := StepSet(s, SetOrder{Order: []core.RepoID{"c", "b", "a"}, Now: epoch})
	if !slices.Equal(got.(Open).Set.Order, []core.RepoID{"c", "b", "a"}) || len(cmds) != 2 {
		t.Fatalf("order = %v", got)
	}
	if same, cmds := StepSet(s, SetOrder{Order: s.Set.Order, Now: epoch}); !reflect.DeepEqual(same, State(s)) || len(cmds) != 0 {
		t.Fatal("unchanged order produced a change")
	}
	if kept, _ := StepSet(s, Close{Now: epoch}); !reflect.DeepEqual(kept, State(s)) {
		t.Fatal("closed a set with a pending member")
	}
	s.Members[0].Status = core.ProposalFailed
	closed, _ := StepSet(s, Close{Now: epoch})
	if _, ok := closed.(Closed); !ok {
		t.Fatalf("state = %#v", closed)
	}
	// A retried member reopens it; the same deliveries again change nothing.
	retry := []Member{mem("p1", "a", core.ProposalCommit, core.ProposalPending, "1")}
	reopened, _ := StepSet(closed, MembersChanged{Members: retry, Now: epoch})
	if _, ok := reopened.(Open); !ok {
		t.Fatalf("state = %#v", reopened)
	}
}

func TestMembersChangedRecompute(t *testing.T) {
	s := State(setWith(nil))
	m := func(st core.ProposalStatus, id string) Member { return mem(id, "a", core.ProposalCommit, st, "1") }
	tests := []struct {
		name string
		ms   []Member
		want string
	}{
		{"empty stays open", nil, "Open"},
		{"pending", []Member{m(core.ProposalPending, "p1")}, "Open"},
		{"one executed one rejected", []Member{m(core.ProposalExecuted, "p1"), m(core.ProposalRejected, "p2")}, "Shipped"},
		{"executed and failed", []Member{m(core.ProposalExecuted, "p1"), m(core.ProposalFailed, "p2")}, "Open"},
		{"all rejected", []Member{m(core.ProposalRejected, "p1")}, "Closed"},
		{"confirmed", []Member{m(core.ProposalExecuted, "p1"), m(core.ProposalConfirmed, "p2")}, "Open"},
	}
	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			got, _ := StepSet(s, MembersChanged{Members: tc.ms, Now: epoch})
			if name := reflect.TypeOf(got).Name(); name != tc.want {
				t.Fatalf("state = %s, want %s", name, tc.want)
			}
		})
	}
	invalid := map[string][]Member{
		"alien repo": {mem("p1", "zz", core.ProposalCommit, core.ProposalPending, "1")},
		"duplicate":  {m(core.ProposalPending, "p1"), m(core.ProposalPending, "p1")},
		"bad hash":   {{ID: "p1", Repo: "a", Kind: core.ProposalCommit, Status: core.ProposalPending, Hash: "x"}},
	}
	for name, ms := range invalid {
		t.Run(name, func(t *testing.T) {
			if got, cmds := StepSet(s, MembersChanged{Members: ms, Now: epoch}); !reflect.DeepEqual(got, s) || len(cmds) != 0 {
				t.Fatal("invalid members changed the set")
			}
		})
	}
	// A shipped set reopens when a new pending member appears.
	shipped, _ := StepSet(s, MembersChanged{Members: []Member{m(core.ProposalExecuted, "p1")}, Now: epoch})
	reopened, _ := StepSet(shipped, MembersChanged{Members: []Member{m(core.ProposalExecuted, "p1"), m(core.ProposalPending, "p2")}, Now: epoch})
	if _, ok := reopened.(Open); !ok {
		t.Fatalf("state = %#v", reopened)
	}
	// Links for repos outside the set are dropped.
	withLinks, _ := StepSet(s, MembersChanged{Members: []Member{m(core.ProposalPending, "p1")}, Links: map[core.RepoID]string{"a": "https://h/x", "zz": "https://h/y"}, Now: epoch})
	if l := withLinks.(Open).Links; len(l) != 1 || l["a"] == "" {
		t.Fatalf("links = %v", l)
	}
}

func TestStepNilAndInvalid(t *testing.T) {
	if got, cmds := StepSet(nil, Close{}); got != nil || cmds != nil {
		t.Fatal("nil state changed")
	}
	bad := newSet()
	bad.LastRun = nil
	if got, cmds := StepSet(bad, Close{}); !reflect.DeepEqual(got, State(bad)) || cmds != nil {
		t.Fatal("invalid state changed")
	}
}

func TestSuggestOrder(t *testing.T) {
	repos := []core.RepoID{"web", "api", "lib", "admin"}
	deps := []Edge{{Dependent: "web", Dependency: "lib"}, {Dependent: "api", Dependency: "lib"}, {Dependent: "admin", Dependency: "api"}, {Dependent: "x", Dependency: "web"}, {Dependent: "web", Dependency: "web"}}
	got, cycle := SuggestOrder(repos, deps, repos)
	want := []core.RepoID{"lib", "web", "api", "admin"}
	if !slices.Equal(got, want) || cycle != nil {
		t.Fatalf("order = %v cycle = %v, want %v", got, cycle, want)
	}
	again, _ := SuggestOrder(repos, deps, repos)
	if !slices.Equal(got, again) {
		t.Fatal("not deterministic")
	}
	// Duplicates are dropped; no edges keeps the fallback order.
	got, _ = SuggestOrder([]core.RepoID{"b", "a", "b"}, nil, []core.RepoID{"b", "a"})
	if !slices.Equal(got, []core.RepoID{"b", "a"}) {
		t.Fatalf("order = %v", got)
	}
	// A cycle falls back and reports the repos on it, not those downstream.
	cyc := []Edge{{Dependent: "a", Dependency: "b"}, {Dependent: "b", Dependency: "a"}, {Dependent: "c", Dependency: "a"}}
	got, cycle = SuggestOrder([]core.RepoID{"a", "b", "c", "d"}, cyc, []core.RepoID{"d", "c", "b", "a"})
	if !slices.Equal(got, []core.RepoID{"d", "c", "b", "a"}) || !slices.Equal(cycle, []core.RepoID{"b", "a"}) {
		t.Fatalf("order = %v cycle = %v", got, cycle)
	}
}

func TestSuggestOrderProperties(t *testing.T) {
	forSeeds(t, func(t *testing.T, r *rand.Rand) {
		pool := []core.RepoID{"a", "b", "c", "d", "e", "a"}
		var repos []core.RepoID
		for _, p := range pool {
			if r.Intn(4) > 0 {
				repos = append(repos, p)
			}
		}
		var deps []Edge
		for i := r.Intn(6); i > 0; i-- {
			deps = append(deps, Edge{Dependent: pool[r.Intn(len(pool))], Dependency: pool[r.Intn(len(pool))]})
		}
		order, cycle := SuggestOrder(repos, deps, pool)
		uniq := slices.Clone(repos)
		slices.Sort(uniq)
		uniq = slices.Compact(uniq)
		if !IsPermutation(order, uniq) {
			t.Fatalf("order %v is not a permutation of %v", order, uniq)
		}
		if cycle == nil {
			for _, e := range deps {
				i, j := slices.Index(order, e.Dependency), slices.Index(order, e.Dependent)
				if i >= 0 && j >= 0 && e.Dependent != e.Dependency && i > j {
					t.Fatalf("%v puts %s before its dependency %s", order, e.Dependent, e.Dependency)
				}
			}
		}
		if again, c2 := SuggestOrder(repos, deps, pool); !slices.Equal(order, again) || !slices.Equal(cycle, c2) {
			t.Fatal("not deterministic")
		}
	})
}

func TestRenderLinksFormat(t *testing.T) {
	order := []core.RepoID{"shop-api", "shop-web", "shop-admin"}
	branches := map[core.RepoID]string{"shop-api": "ygg/t1", "shop-web": "ygg/t1"}
	links := map[core.RepoID]string{"shop-api": "https://git.example.test/acme/shop-api/-/merge_requests/4", "shop-web": "https://evil.test/x"}
	got := RenderLinks("Part of a set: "+LinkMarker+" (bot)", order, branches, links, "GIT.example.test")
	if !strings.HasPrefix(got, "Part of a set: shop-api (ygg/t1): https://git.example.test/acme/shop-api/-/merge_requests/4 · shop-web (ygg/t1) · shop-admin") {
		t.Fatalf("got %q", got)
	}
	if !strings.HasSuffix(got, " (bot)") || strings.Contains(got, "evil") {
		t.Fatalf("got %q", got)
	}
	if out := RenderLinks("no marker", order, branches, links, "git.example.test"); out != "no marker" {
		t.Fatalf("out = %q", out)
	}
	if out := RenderLinks(LinkMarker+LinkMarker, order, branches, links, "git.example.test"); out != LinkMarker+LinkMarker {
		t.Fatalf("two markers changed: %q", out)
	}
	if out := RenderLinks("<!--"+LinkMarker+"-->", []core.RepoID{" ygg:links "}, nil, nil, "h"); out != "<!--"+LinkMarker+"-->" {
		t.Fatalf("rebuilt a marker: %q", out)
	}
}

func TestValidateLinkMarker(t *testing.T) {
	ok := []string{"", "plain", "see " + LinkMarker + " for links", LinkMarker, strings.Repeat("x", MaxDescriptionBytes)}
	for _, d := range ok {
		if err := ValidateLinkMarker(d); err != nil {
			t.Errorf("%.40q: %v", d, err)
		}
	}
	bad := map[string]string{
		"two markers":         LinkMarker + " " + LinkMarker,
		"line break":          "a\nb",
		"carriage return":     "a\rb",
		"unicode separator":   "a b",
		"rebuilt marker":      "<!-- ygg:li" + LinkMarker + "nks -->",
		"too long":            strings.Repeat("x", MaxDescriptionBytes+1),
		"marker counted full": LinkMarker + strings.Repeat("x", MaxDescriptionBytes-MaxRenderedLinks+1),
	}
	for name, d := range bad {
		if ValidateLinkMarker(d) == nil {
			t.Errorf("%s accepted", name)
		}
	}
}

func TestSafeLinkURL(t *testing.T) {
	good := []string{"https://git.example.test/a/b", "http://git.example.test:8080/x", "https://GIT.example.test/x?y=1"}
	for _, u := range good {
		if !linkURLOK(u, "git.example.test") {
			t.Errorf("%q refused", u)
		}
	}
	bad := []string{"", "git.example.test/x", "ftp://git.example.test/x", "https://u@git.example.test/x", "https://u:p@git.example.test/x",
		"https://other.test/x", "https://git.example.test/a b", "https://git.example.test/\x00", "https://git.example.test\\x", "javascript:alert(1)",
		"https://git.example.test/<x>", "https:///x", strings.Repeat("h", maxURLBytes+1)}
	for _, u := range bad {
		if linkURLOK(u, "git.example.test") {
			t.Errorf("%q accepted", u)
		}
	}
	if linkURLOK("https://git.example.test/x", "") {
		t.Error("empty host accepted")
	}
}
