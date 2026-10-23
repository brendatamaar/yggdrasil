package hosts

import (
	"math/rand"
	"reflect"
	"testing"
	"time"

	"github.com/brendatamaar/yggdrasil/internal/core"
)

// The property tests drive Step with random input sequences over a small
// universe and check one invariant of the design (H1-H9) after every step.

var (
	t0        = time.Date(2026, 10, 1, 9, 0, 0, 0, time.UTC)
	instances = []Instance{
		{ID: "github.com", Kind: KindGitHub, Base: "https://github.com", Token: true},
		{ID: "git.acme.dev", Kind: KindGitLab, Base: "https://git.acme.dev", Token: false},
		{ID: "code.example.org", Kind: KindGitea, Base: "https://code.example.org", Token: true},
	}
	repoIDs  = []core.RepoID{"r1", "r2", "r3", "r4", "r5", "r6", "r7", "r8"}
	branches = []string{"main", "feature/cart", ""}
)

type gen struct {
	rng *rand.Rand
	now time.Time
	// withRefresh and withReconfig allow inputs that reset keys.
	withRefresh bool
	withConfig  bool
}

func (g *gen) tick() time.Time {
	g.now = g.now.Add(time.Duration(g.rng.Intn(90)) * time.Second)
	return g.now
}

func (g *gen) payload(k Key) Payload {
	if _, ok := k.(InboxKey); ok {
		return Inbox{Items: []InboxItem{{MR: MergeRequest{Number: g.rng.Intn(3) + 1, State: MROpen}, Repo: "a/b", Role: RoleAuthored}}}
	}
	st := []PipelineStatus{PipelineSuccess, PipelineFailed, PipelineRunning}[g.rng.Intn(3)]
	if g.rng.Intn(4) == 0 {
		return RepoState{}
	}
	return RepoState{Pipeline: &Pipeline{Status: st, URL: "https://x/p"}}
}

func (g *gen) targets() TargetsChanged {
	var rs []RepoTarget
	for _, id := range repoIDs {
		if g.rng.Intn(4) == 0 {
			continue
		}
		inst := instances[g.rng.Intn(len(instances))]
		rs = append(rs, RepoTarget{Repo: id, Instance: inst.ID, Project: "acme-shop/shop-api", Branch: branches[g.rng.Intn(len(branches))]})
	}
	return TargetsChanged{Repos: rs, Now: g.tick()}
}

func (g *gen) config() ConfigChanged {
	var is []Instance
	for _, in := range instances {
		if g.rng.Intn(6) == 0 {
			continue
		}
		if g.rng.Intn(5) == 0 {
			in.Token = !in.Token
		}
		is = append(is, in)
	}
	return ConfigChanged{Instances: is, Now: g.tick()}
}

// fetching lists the keys currently in flight, in a stable order.
func fetching(p Poller) []Key {
	var ks []Key
	for k, e := range p.Entries {
		if _, ok := e.Phase.(Fetching); ok {
			ks = append(ks, k)
		}
	}
	sortKeys(ks)
	return ks
}

func sortKeys(ks []Key) {
	for i := 1; i < len(ks); i++ {
		for j := i; j > 0 && keyLess(ks[j], ks[j-1]) < 0; j-- {
			ks[j], ks[j-1] = ks[j-1], ks[j]
		}
	}
}

func (g *gen) next(p Poller) Input {
	for {
		switch n := g.rng.Intn(14); {
		case n < 2:
			return g.targets()
		case n == 2:
			if g.withConfig {
				return g.config()
			}
		case n == 3:
			return Tick{Now: g.tick()}
		case n == 4:
			if g.withRefresh {
				var ks []Key
				for _, k := range sortedTargets(p) {
					if g.rng.Intn(3) == 0 {
						ks = append(ks, k)
					}
				}
				return RefreshRequested{Keys: ks, Now: g.tick()}
			}
		default:
			fs := fetching(p)
			var k Key = RepoKey{Repo: repoIDs[g.rng.Intn(len(repoIDs))], Branch: "main"}
			attempt := g.rng.Intn(5)
			if len(fs) > 0 && g.rng.Intn(10) < 8 {
				k = fs[g.rng.Intn(len(fs))]
				attempt = p.Entries[k].Phase.(Fetching).Attempt
			}
			now := g.tick()
			switch g.rng.Intn(6) {
			case 0, 1:
				return Fetched{Key: k, Attempt: attempt, Payload: g.payload(k), ETag: "e", Now: now}
			case 2:
				return NotModified{Key: k, Attempt: attempt, Now: now}
			default:
				kinds := []FailKind{FailTransient, FailTransient, FailRateLimited, FailUnauthorized, FailNotFound}
				var ra *time.Time
				if g.rng.Intn(2) == 0 {
					t := now.Add(time.Duration(g.rng.Intn(300)-30) * time.Second)
					ra = &t
				}
				return FetchFailed{Key: k, Attempt: attempt, Kind: kinds[g.rng.Intn(len(kinds))], RetryAt: ra, Now: now}
			}
		}
	}
}

func sortedTargets(p Poller) []Key {
	var ks []Key
	for k := range p.Targets {
		ks = append(ks, k)
	}
	sortKeys(ks)
	return ks
}

// drive runs many random sequences and calls check after every step.
func drive(t *testing.T, refresh, config bool, check func(t *testing.T, before, after Poller, in Input, cmds []Command)) {
	t.Helper()
	for seed := int64(0); seed < 300; seed++ {
		g := &gen{rng: rand.New(rand.NewSource(seed)), now: t0, withRefresh: refresh, withConfig: config}
		p := Poller{}
		p, _ = Step(p, ConfigChanged{Instances: instances, Now: t0})
		for i := 0; i < 120; i++ {
			in := g.next(p)
			after, cmds := Step(p, in)
			check(t, p, after, in, cmds)
			if t.Failed() {
				t.Fatalf("seed %d step %d input %#v", seed, i, in)
			}
			p = after
		}
	}
}

func nowIn(in Input) time.Time { return nowOf(in) }

func TestPropInstanceConcurrency(t *testing.T) { // H1
	drive(t, true, true, func(t *testing.T, _, p Poller, _ Input, _ []Command) {
		count := map[InstanceID]int{}
		for k, e := range p.Entries {
			if _, ok := e.Phase.(Fetching); ok {
				count[p.Targets[k].Instance]++
			}
		}
		for id, n := range count {
			if n > MaxInFlight {
				t.Errorf("%s has %d fetches in flight", id, n)
			}
		}
		for id, n := range p.InFlight {
			if n != count[id] {
				t.Errorf("InFlight[%s] = %d, but %d keys are fetching", id, n, count[id])
			}
		}
	})
}

func TestPropRespectsRateLimit(t *testing.T) { // H2
	drive(t, true, true, func(t *testing.T, _, p Poller, in Input, cmds []Command) {
		for _, c := range cmds {
			if f, ok := c.(Fetch); ok && nowIn(in).Before(p.Limited[f.Instance]) {
				t.Errorf("fetch for %s at %v while limited until %v", f.Instance, nowIn(in), p.Limited[f.Instance])
			}
		}
	})
}

// kept reports whether a key kept its target across a step. A key whose
// target changed is dropped and re-added: a new key, with a clean slate.
func kept(before, after Poller, k Key) bool {
	b, ok := before.Targets[k]
	return ok && b == after.Targets[k]
}

func TestPropBlockedStaysQuiet(t *testing.T) { // H3
	drive(t, true, true, func(t *testing.T, before, after Poller, in Input, cmds []Command) {
		for _, c := range cmds {
			f, ok := c.(Fetch)
			if !ok || !kept(before, after, f.Key) {
				continue
			}
			if _, blocked := before.Entries[f.Key].Phase.(Blocked); !blocked {
				continue
			}
			switch v := in.(type) {
			case ConfigChanged:
			case RefreshRequested:
				asked := len(v.Keys) == 0
				for _, k := range v.Keys {
					asked = asked || k == f.Key
				}
				if !asked {
					t.Errorf("blocked %v fetched on a refresh that did not name it", f.Key)
				}
			default:
				t.Errorf("blocked %v fetched on %T", f.Key, in)
			}
		}
	})
}

func TestPropBackoffBounded(t *testing.T) { // H4
	for n := 1; n < 40; n++ {
		if Backoff(n+1) < Backoff(n) || Backoff(n) > MaxBackoff || Backoff(n) <= 0 {
			t.Fatalf("Backoff(%d) = %v, Backoff(%d) = %v", n, Backoff(n), n+1, Backoff(n+1))
		}
	}
	if Backoff(1) != 10*time.Second || Backoff(2) != 30*time.Second || Backoff(4) != 270*time.Second || Backoff(5) != MaxBackoff {
		t.Fatalf("backoff curve is off: %v %v %v %v", Backoff(1), Backoff(2), Backoff(4), Backoff(5))
	}
	drive(t, true, true, func(t *testing.T, before, after Poller, in Input, _ []Command) {
		for k, e := range after.Entries {
			if w, ok := e.Phase.(Waiting); ok && w.Why == WaitTransient {
				if w.Failures != e.Failures || w.Failures < 1 {
					t.Errorf("%v waits with failures %d (entry %d)", k, w.Failures, e.Failures)
				}
			}
		}
		var succ Key
		var attempt int
		switch v := in.(type) {
		case Fetched:
			succ, attempt = v.Key, v.Attempt
		case NotModified:
			// A 304 without data to stand for is a failure.
			if fg, isF := before.Entries[v.Key].Phase.(Fetching); isF && fg.Prev.Data != nil {
				succ, attempt = v.Key, v.Attempt
			}
		}
		if e, ok := after.Entries[succ]; ok && succ != nil && e.Failures != 0 {
			if fg, isF := before.Entries[succ].Phase.(Fetching); isF && fg.Attempt == attempt {
				t.Errorf("a success left %d failures", e.Failures)
			}
		}
		f, ok := in.(FetchFailed)
		if !ok || f.Kind != FailTransient {
			return
		}
		pe, was := before.Entries[f.Key]
		if fg, isF := pe.Phase.(Fetching); !was || !isF || fg.Attempt != f.Attempt {
			return
		}
		ne := after.Entries[f.Key]
		w, ok := ne.Phase.(Waiting)
		if !ok || ne.Failures != pe.Failures+1 || w.Until != f.Now.Add(Backoff(ne.Failures)) || w.Until.Sub(f.Now) > MaxBackoff {
			t.Errorf("transient failure gave %#v after %d failures", ne, pe.Failures)
		}
	})
}

func TestPropStaleResultIgnored(t *testing.T) { // H5
	drive(t, true, true, func(t *testing.T, before, after Poller, in Input, cmds []Command) {
		var k Key
		var attempt int
		switch v := in.(type) {
		case Fetched:
			k, attempt = v.Key, v.Attempt
		case NotModified:
			k, attempt = v.Key, v.Attempt
		case FetchFailed:
			k, attempt = v.Key, v.Attempt
		default:
			return
		}
		fg, ok := before.Entries[k].Phase.(Fetching)
		_, targeted := before.Targets[k]
		if ok && targeted && fg.Attempt == attempt {
			return
		}
		if !reflect.DeepEqual(before, after) || len(cmds) != 0 {
			t.Errorf("a stale %T changed the state or emitted %v", in, cmds)
		}
	})
	// A dropped and re-added key never matches the old attempt.
	p, _ := Step(Poller{}, ConfigChanged{Instances: instances, Now: t0})
	tg := TargetsChanged{Repos: []RepoTarget{{Repo: "r1", Instance: "git.acme.dev", Project: "a/b", Branch: "main"}}, Now: t0}
	p, cmds := Step(p, tg)
	old := cmds[len(cmds)-1].(Fetch)
	p, _ = Step(p, TargetsChanged{Now: t0})
	p, cmds = Step(p, tg)
	cur := cmds[len(cmds)-1].(Fetch)
	if cur.Attempt == old.Attempt {
		t.Fatal("a re-added key reuses an attempt number")
	}
	after, cmds2 := Step(p, Fetched{Key: old.Key, Attempt: old.Attempt, Payload: RepoState{}, Now: t0})
	if !reflect.DeepEqual(p, after) || len(cmds2) != 0 {
		t.Fatal("the old attempt's result was applied")
	}
}

func TestPropKeepsLastGoodData(t *testing.T) { // H6
	drive(t, true, true, func(t *testing.T, before, after Poller, in Input, _ []Command) {
		f, ok := in.(FetchFailed)
		if !ok {
			return
		}
		fg, isF := before.Entries[f.Key].Phase.(Fetching)
		if _, targeted := before.Targets[f.Key]; !targeted || !isF || fg.Attempt != f.Attempt {
			return
		}
		// start() may move the key straight back to Fetching: prevOf still holds the data.
		if got := prevOf(after.Entries[f.Key].Phase).Data; !reflect.DeepEqual(got, fg.Prev.Data) {
			t.Errorf("data after a failure = %#v, want %#v", got, fg.Prev.Data)
		}
	})
}

func TestPropEntriesMatchTargets(t *testing.T) { // H7
	drive(t, true, true, func(t *testing.T, _, p Poller, _ Input, _ []Command) {
		if len(p.Entries) != len(p.Targets) {
			t.Errorf("%d entries for %d targets", len(p.Entries), len(p.Targets))
		}
		for k := range p.Targets {
			if _, ok := p.Entries[k]; !ok {
				t.Errorf("target %v has no entry", k)
			}
		}
		for k := range p.Entries {
			if _, ok := p.Targets[k]; !ok {
				t.Errorf("entry %v is not a target", k)
			}
		}
	})
}

func TestPropNoFetchBeforeTTL(t *testing.T) { // H8
	drive(t, false, true, func(t *testing.T, before, after Poller, in Input, cmds []Command) {
		for _, c := range cmds {
			f, ok := c.(Fetch)
			if !ok || !kept(before, after, f.Key) {
				continue
			}
			switch ph := before.Entries[f.Key].Phase.(type) {
			case Idle:
				if ph.Data != nil && nowIn(in).Before(ph.FetchedAt.Add(before.Targets[f.Key].TTL)) {
					t.Errorf("%v fetched at %v, but its data is fresh until %v", f.Key, nowIn(in), ph.FetchedAt.Add(before.Targets[f.Key].TTL))
				}
			case Waiting:
				if nowIn(in).Before(ph.Until) {
					t.Errorf("%v fetched at %v before its wait ends at %v", f.Key, nowIn(in), ph.Until)
				}
			case Fetching:
				t.Errorf("%v fetched while a fetch is in flight", f.Key)
			}
		}
	})
}

func TestPropReadOnlyCommands(t *testing.T) { // H9
	drive(t, true, true, func(t *testing.T, _, after Poller, _ Input, cmds []Command) {
		for _, c := range cmds {
			switch v := c.(type) {
			case Fetch:
				if _, ok := after.Targets[v.Key]; !ok {
					t.Errorf("fetch for %v, which is not targeted", v.Key)
				}
			case PublishKey, PublishHosts:
			default:
				t.Errorf("command %T is neither a fetch nor a publish", c)
			}
		}
	})
}

// deepCopy copies v through maps, slices, pointers and interfaces, so a later
// write through the original cannot show in the copy.
func deepCopy(v reflect.Value) reflect.Value {
	switch v.Kind() {
	case reflect.Map:
		if v.IsNil() {
			return v
		}
		out := reflect.MakeMapWithSize(v.Type(), v.Len())
		for _, k := range v.MapKeys() {
			out.SetMapIndex(k, deepCopy(v.MapIndex(k)))
		}
		return out
	case reflect.Slice:
		if v.IsNil() {
			return v
		}
		out := reflect.MakeSlice(v.Type(), v.Len(), v.Len())
		for i := 0; i < v.Len(); i++ {
			out.Index(i).Set(deepCopy(v.Index(i)))
		}
		return out
	case reflect.Pointer:
		if v.IsNil() {
			return v
		}
		out := reflect.New(v.Type().Elem())
		out.Elem().Set(deepCopy(v.Elem()))
		return out
	case reflect.Interface:
		if v.IsNil() {
			return v
		}
		out := reflect.New(v.Type()).Elem()
		out.Set(deepCopy(v.Elem()))
		return out
	case reflect.Struct:
		out := reflect.New(v.Type()).Elem()
		out.Set(v) // unexported fields keep their value
		for i := 0; i < v.NumField(); i++ {
			if out.Field(i).CanSet() {
				out.Field(i).Set(deepCopy(v.Field(i)))
			}
		}
		return out
	}
	return v
}

func cloneAny[T any](x T) T { return deepCopy(reflect.ValueOf(&x).Elem()).Interface().(T) }

// Step must not write to the state or the input it receives: the caller keeps
// using them.
func TestStepDoesNotMutateItsInput(t *testing.T) {
	for seed := int64(1); seed <= 20; seed++ {
		g := &gen{rng: rand.New(rand.NewSource(seed)), now: t0, withRefresh: true, withConfig: true}
		p, _ := Step(Poller{}, ConfigChanged{Instances: instances, Now: t0})
		for i := 0; i < 200; i++ {
			in := g.next(p)
			snapP, snapIn := cloneAny(p), cloneAny(in)
			next, _ := Step(p, in)
			if !reflect.DeepEqual(p, snapP) {
				t.Fatalf("seed %d step %d: Step changed its state: before %#v after %#v", seed, i, snapP, p)
			}
			if !reflect.DeepEqual(in, snapIn) {
				t.Fatalf("seed %d step %d: Step changed its input %#v", seed, i, in)
			}
			p = next
		}
	}
}

func TestOrderInboxFirstThenRepoID(t *testing.T) {
	p, _ := Step(Poller{}, ConfigChanged{Instances: []Instance{{ID: "github.com", Kind: KindGitHub, Base: "https://github.com", Token: true}}, Now: t0})
	_, cmds := Step(Poller{Instances: p.Instances, Targets: p.Targets, Entries: p.Entries}, TargetsChanged{Repos: []RepoTarget{
		{Repo: "r2", Instance: "github.com", Project: "a/b", Branch: "main"},
		{Repo: "r1", Instance: "github.com", Project: "a/c", Branch: "main"},
	}, Now: t0})
	var order []Key
	for _, c := range cmds {
		if f, ok := c.(Fetch); ok {
			order = append(order, f.Key)
		}
	}
	// The inbox was already started by the config step, so only repos follow.
	if len(order) != 2 || order[0] != (RepoKey{Repo: "r1", Branch: "main"}) {
		t.Fatalf("order = %v", order)
	}
}

func TestFailedPipelines(t *testing.T) {
	p := Poller{Entries: map[Key]Entry{
		RepoKey{Repo: "r1", Branch: "main"}: {Phase: Idle{Data: RepoState{Pipeline: &Pipeline{Status: PipelineFailed}}}},
		RepoKey{Repo: "r2", Branch: "main"}: {Phase: Waiting{Prev: Idle{Data: RepoState{Pipeline: &Pipeline{Status: PipelineFailed}}}}},
		RepoKey{Repo: "r3", Branch: "main"}: {Phase: Idle{Data: RepoState{Pipeline: &Pipeline{Status: PipelineSuccess}}}},
		RepoKey{Repo: "r4", Branch: "main"}: {Phase: Idle{}},
		InboxKey{Instance: "github.com"}:    {Phase: Idle{Data: Inbox{}}},
	}}
	got := FailedPipelines(p)
	if len(got) != 2 {
		t.Fatalf("FailedPipelines = %v", got)
	}
	if _, ok := got["r1"]; !ok {
		t.Fatal("r1 missing")
	}
	if _, ok := got["r2"]; !ok {
		t.Fatal("r2 missing")
	}
}

func TestLinks(t *testing.T) {
	cases := []struct {
		inst   Instance
		branch string
		want   LinkSet
	}{
		{Instance{Kind: KindGitLab, Base: "https://git.acme.dev/"}, "feature/cart", LinkSet{
			Repo: "https://git.acme.dev/acme-shop/shop-api", Branch: "https://git.acme.dev/acme-shop/shop-api/-/tree/feature/cart",
			MRList: "https://git.acme.dev/acme-shop/shop-api/-/merge_requests", Pipelines: "https://git.acme.dev/acme-shop/shop-api/-/pipelines"}},
		{Instance{Kind: KindGitHub, Base: "https://github.com"}, "a b#c", LinkSet{
			Repo: "https://github.com/acme-shop/shop-api", Branch: "https://github.com/acme-shop/shop-api/tree/a%20b%23c",
			MRList: "https://github.com/acme-shop/shop-api/pulls", Pipelines: "https://github.com/acme-shop/shop-api/actions"}},
		{Instance{Kind: KindGitea, Base: "https://code.example.org"}, "", LinkSet{
			Repo:   "https://code.example.org/acme-shop/shop-api",
			MRList: "https://code.example.org/acme-shop/shop-api/pulls", Pipelines: "https://code.example.org/acme-shop/shop-api/actions"}},
	}
	for _, c := range cases {
		if got := Links(c.inst, "acme-shop/shop-api", c.branch); got != c.want {
			t.Errorf("Links(%v, %q) = %+v, want %+v", c.inst.Kind, c.branch, got, c.want)
		}
	}
}

func TestRateLimitClampsRetryAt(t *testing.T) {
	p, _ := Step(Poller{}, ConfigChanged{Instances: instances, Now: t0})
	p, cmds := Step(p, TargetsChanged{Repos: []RepoTarget{{Repo: "r1", Instance: "git.acme.dev", Project: "a/b", Branch: "main"}}, Now: t0})
	f := cmds[len(cmds)-1].(Fetch)
	far := t0.Add(48 * time.Hour)
	p, _ = Step(p, FetchFailed{Key: f.Key, Attempt: f.Attempt, Kind: FailRateLimited, RetryAt: &far, Now: t0})
	if got := p.Limited["git.acme.dev"]; got != t0.Add(time.Hour) {
		t.Fatalf("limited until %v", got)
	}
}

// onInstance lists the keys of an instance other than skip, in stable order.
func onInstance(p Poller, inst InstanceID, skip Key) []Key {
	var ks []Key
	for k := range p.Targets {
		if p.Targets[k].Instance == inst && k != skip {
			ks = append(ks, k)
		}
	}
	sortKeys(ks)
	return ks
}

func isBlocked(ph Phase, why BlockReason) bool {
	b, ok := ph.(Blocked)
	return ok && b.Why == why
}

// An unauthorized answer for a token blocks every other key that uses the
// token (Idle or Waiting; a fetch in flight reports for itself). An instance
// without a token has no such propagation.
func TestPropUnauthorizedBlocksTheTokensOtherKeys(t *testing.T) {
	var withToken, withoutToken int
	drive(t, true, true, func(t *testing.T, before, after Poller, in Input, _ []Command) {
		f, ok := in.(FetchFailed)
		if !ok || f.Kind != FailUnauthorized {
			return
		}
		fg, isF := before.Entries[f.Key].Phase.(Fetching)
		if _, targeted := before.Targets[f.Key]; !targeted || !isF || fg.Attempt != f.Attempt {
			return
		}
		inst := before.Targets[f.Key].Instance
		if !isBlocked(after.Entries[f.Key].Phase, BlockUnauthorized) {
			t.Errorf("the failed key %v = %#v", f.Key, after.Entries[f.Key].Phase)
		}
		for _, k := range onInstance(before, inst, f.Key) {
			was := before.Entries[k].Phase
			now := after.Entries[k].Phase
			switch was.(type) {
			case Idle, Waiting:
				if before.Instances[inst].Token {
					withToken++
					if !isBlocked(now, BlockUnauthorized) {
						t.Errorf("%v on %s (token) was %T and is %#v after an unauthorized answer", k, inst, was, now)
					}
				} else {
					withoutToken++
					if isBlocked(now, BlockUnauthorized) {
						t.Errorf("%v on %s (no token) was blocked by another key's unauthorized answer", k, inst)
					}
				}
			case Fetching:
				if _, still := now.(Fetching); !still {
					t.Errorf("%v was in flight and is now %#v", k, now)
				}
			}
		}
	})
	if withToken == 0 || withoutToken == 0 {
		t.Fatalf("generator never covered both cases: with token %d, without %d", withToken, withoutToken)
	}
}

func TestUnauthorizedBlocksTheTokensOtherKeys(t *testing.T) {
	p, _ := Step(Poller{}, ConfigChanged{Instances: instances, Now: t0})
	p, cmds := Step(p, TargetsChanged{Repos: []RepoTarget{
		{Repo: "r1", Instance: "github.com", Project: "a/b", Branch: "main"},
		{Repo: "r2", Instance: "github.com", Project: "a/c", Branch: "main"},
		{Repo: "r3", Instance: "github.com", Project: "a/d", Branch: "main"},
		{Repo: "r4", Instance: "git.acme.dev", Project: "a/e", Branch: "main"},
		{Repo: "r5", Instance: "git.acme.dev", Project: "a/f", Branch: "main"},
	}, Now: t0})
	attempt := map[Key]int{}
	for _, c := range cmds {
		if f, ok := c.(Fetch); ok {
			attempt[f.Key] = f.Attempt
		}
	}
	// Let r2 finish and wait for its next poll, r3 stay Idle after a success.
	k1, k2, k3 := RepoKey{Repo: "r1", Branch: "main"}, RepoKey{Repo: "r2", Branch: "main"}, RepoKey{Repo: "r3", Branch: "main"}
	k4, k5 := RepoKey{Repo: "r4", Branch: "main"}, RepoKey{Repo: "r5", Branch: "main"}
	p, _ = Step(p, Fetched{Key: k2, Attempt: attempt[k2], Payload: RepoState{}, Now: t0})
	p, _ = Step(p, FetchFailed{Key: k3, Attempt: attempt[k3], Kind: FailTransient, Now: t0}) // Waiting
	p, _ = Step(p, Fetched{Key: k5, Attempt: attempt[k5], Payload: RepoState{}, Now: t0})    // Idle, no token
	if _, ok := p.Entries[k3].Phase.(Waiting); !ok {
		t.Fatalf("setup: r3 = %#v", p.Entries[k3].Phase)
	}
	inbox := InboxKey{Instance: "github.com"}
	if _, ok := p.Entries[inbox].Phase.(Fetching); !ok {
		t.Fatalf("setup: inbox = %#v", p.Entries[inbox].Phase)
	}

	p, _ = Step(p, FetchFailed{Key: k1, Attempt: attempt[k1], Kind: FailUnauthorized, Now: t0})
	for _, k := range []Key{k1, k2, k3} {
		if !isBlocked(p.Entries[k].Phase, BlockUnauthorized) {
			t.Errorf("%v = %#v, want Blocked{unauthorized}", k, p.Entries[k].Phase)
		}
	}
	if _, ok := p.Entries[inbox].Phase.(Fetching); !ok {
		t.Errorf("the inbox in flight = %#v, want it left alone", p.Entries[inbox].Phase)
	}
	// The same host without a token: another instance's key is not touched.
	p, _ = Step(p, FetchFailed{Key: k4, Attempt: attempt[k4], Kind: FailUnauthorized, Now: t0})
	if !isBlocked(p.Entries[k4].Phase, BlockUnauthorized) {
		t.Errorf("r4 = %#v", p.Entries[k4].Phase)
	}
	if isBlocked(p.Entries[k5].Phase, BlockUnauthorized) {
		t.Errorf("r5 on an instance with no token was blocked: %#v", p.Entries[k5].Phase)
	}
	for _, k := range []Key{k1, k2, k3} {
		if b, ok := p.Entries[k].Phase.(Blocked); !ok || b.Since != t0 {
			t.Errorf("%v = %#v, want blocked since %v", k, p.Entries[k].Phase, t0)
		}
	}
}

// H3, state side: a Blocked key that keeps its target leaves Blocked only
// when the user asks for it (RefreshRequested names it), or a ConfigChanged
// changed the key's instance (kind, token or base) or the reason was
// "unsupported". A ConfigChanged that leaves the instance as it was never
// unblocks anything, and no other input does.
func TestPropBlockedLeavesOnlyForItsCause(t *testing.T) {
	var unblocked int
	drive(t, true, true, func(t *testing.T, before, after Poller, in Input, _ []Command) {
		for k, e := range before.Entries {
			b, wasBlocked := e.Phase.(Blocked)
			if !wasBlocked || !kept(before, after, k) {
				continue
			}
			if _, still := after.Entries[k].Phase.(Blocked); still {
				continue
			}
			unblocked++
			inst := before.Targets[k].Instance
			switch v := in.(type) {
			case ConfigChanged:
				if before.Instances[inst] == after.Instances[inst] && b.Why != BlockUnsupported {
					t.Errorf("%v (%s) left Blocked{%s} on a config change that did not touch %s", k, inst, b.Why, inst)
				}
			case RefreshRequested:
				asked := len(v.Keys) == 0
				for _, ak := range v.Keys {
					asked = asked || ak == k
				}
				if !asked {
					t.Errorf("%v left Blocked on a refresh that did not name it", k)
				}
			default:
				t.Errorf("%v left Blocked on %T", k, in)
			}
		}
	})
	if unblocked == 0 {
		t.Fatal("the generator never unblocked a key")
	}
}

// A Blocked key that a config change unblocks has its FetchedAt cleared, so
// the data counts as stale and is fetched at once with the new token.
func TestPropConfigUnblockClearsFetchedAt(t *testing.T) {
	var checked int
	drive(t, true, true, func(t *testing.T, before, after Poller, in Input, _ []Command) {
		if _, ok := in.(ConfigChanged); !ok {
			return
		}
		for k, e := range before.Entries {
			b, wasBlocked := e.Phase.(Blocked)
			if !wasBlocked || !kept(before, after, k) {
				continue
			}
			if _, still := after.Entries[k].Phase.(Blocked); still {
				continue
			}
			if !b.Prev.FetchedAt.IsZero() {
				checked++
			}
			if got := prevOf(after.Entries[k].Phase).FetchedAt; !got.IsZero() {
				t.Errorf("%v unblocked by a config change keeps FetchedAt %v", k, got)
			}
		}
	})
	if checked == 0 {
		t.Fatal("the generator never unblocked a key that had fetched data")
	}
}
