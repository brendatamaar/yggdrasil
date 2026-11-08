package terminal

import (
	"fmt"
	"maps"
	"math/rand"
	"reflect"
	"strings"
	"testing"
	"time"

	"github.com/brendatamaar/yggdrasil/internal/core"
)

const propRuns = 300

var epoch = time.Date(2026, 1, 1, 0, 0, 0, 0, time.UTC)

var (
	hashes   = []TokenHash{hh(1), hh(2), hh(3), hh(4), hh(5), hh(6), hh(7), hh(8), hh(9), hh(10), hh(11), hh(12)}
	sessions = []core.TerminalSessionID{"ts_1", "ts_2", "ts_3", "ts_4", "ts_5", "ts_6", "ts_7"}
	folders  = []Folder{{Repo: "repo_a"}, {Repo: "repo_a", Worktree: "wt_1"}, {Repo: "repo_b"}}
)

func hh(n int) TokenHash { return HashToken(fmt.Sprintf("raw-%d", n)) }

type gen struct {
	r   *rand.Rand
	now time.Time
}

func (g *gen) tick() time.Time {
	g.now = g.now.Add(time.Duration(g.r.Intn(12)) * time.Second)
	return g.now
}

func pick[T any](r *rand.Rand, xs []T) T { return xs[r.Intn(len(xs))] }

func (g *gen) input() Input {
	r := g.r
	now := g.tick()
	sess := pick(r, sessions)
	switch r.Intn(13) {
	case 0, 1, 2:
		return IssueRequested{Hash: pick(r, hashes), Folder: pick(r, folders), Now: now, Reply: "rp"}
	case 3, 4, 5, 6:
		check := FolderTracked
		if r.Intn(6) == 0 {
			check = FolderUntracked
		}
		f := pick(r, folders)
		return RedeemRequested{Hash: pick(r, hashes), Folder: f, Session: sess,
			Size: Size{Cols: r.Intn(MaxCols + 20), Rows: 1 + r.Intn(MaxRows)}, Check: check, Now: now, Reply: "rp"}
	case 7:
		return Opened{Session: sess, Pid: 1 + r.Intn(999), Now: now}
	case 8:
		return OpenFailed{Session: sess, Now: now}
	case 9:
		return Resized{Session: sess, Size: Size{Cols: r.Intn(MaxCols + 50), Rows: r.Intn(MaxRows + 50)}}
	case 10:
		switch r.Intn(3) {
		case 0:
			return ClientClosed{Session: sess, Now: now}
		case 1:
			return CloseRequested{Session: sess, Now: now}
		}
		return ShellExited{Session: sess, Code: r.Intn(4), Now: now}
	case 11:
		if r.Intn(8) == 0 {
			return Shutdown{Now: now}
		}
		return ShellExited{Session: sess, Code: r.Intn(4), Now: now}
	}
	return Wake{Kind: WakeExpire, Now: now}
}

// forRuns drives random input sequences; check runs after every step with the
// state before, the input, the state after and the commands. It also checks
// that Step does not mutate the registry it is given.
func forRuns(t *testing.T, steps int, check func(t *testing.T, prev Registry, in Input, next Registry, cmds []Command)) {
	t.Helper()
	for seed := int64(1); seed <= propRuns; seed++ {
		g := &gen{r: rand.New(rand.NewSource(seed)), now: epoch}
		reg := NewRegistry()
		for i := 0; i < steps; i++ {
			in := g.input()
			snap := clone(reg)
			next, cmds := Step(reg, in)
			if !reflect.DeepEqual(snap, reg) {
				t.Fatalf("seed %d step %d: Step changed its input registry on %#v", seed, i, in)
			}
			check(t, reg, in, next, cmds)
			if t.Failed() {
				t.Fatalf("seed %d step %d: input %#v", seed, i, in)
			}
			reg = next
		}
	}
}

func TestPropStepDoesNotMutateInput(t *testing.T) {
	forRuns(t, 60, func(*testing.T, Registry, Input, Registry, []Command) {})
}

func TestPropTokenSingleUse(t *testing.T) {
	// Hash -> number of OpenPTY commands. A hash can be issued again after it
	// expired; count per issue by resetting when a token is newly issued.
	for seed := int64(1); seed <= propRuns; seed++ {
		g := &gen{r: rand.New(rand.NewSource(seed)), now: epoch}
		reg := NewRegistry()
		opens := map[TokenHash]int{}
		for i := 0; i < 80; i++ {
			in := g.input()
			if is, ok := in.(IssueRequested); ok {
				if _, had := reg.Tokens[is.Hash]; !had {
					opens[is.Hash] = 0
				}
			}
			next, cmds := Step(reg, in)
			if rd, ok := in.(RedeemRequested); ok {
				for _, c := range cmds {
					if _, ok := c.(OpenPTY); ok {
						opens[rd.Hash]++
					}
				}
			}
			for h, n := range opens {
				if n > 1 {
					t.Fatalf("seed %d: token %s opened %d sessions", seed, h[:6], n)
				}
			}
			reg = next
		}
	}
}

func TestPropNoGrantAfterExpiry(t *testing.T) {
	forRuns(t, 80, func(t *testing.T, prev Registry, in Input, _ Registry, cmds []Command) {
		rd, ok := in.(RedeemRequested)
		if !ok {
			return
		}
		for _, c := range cmds {
			if rp, ok := c.(Reply); ok && rp.Kind == ReplyRedeem && rp.Granted {
				iss, ok := prev.Tokens[rd.Hash].(Issued)
				if !ok || !rd.Now.Before(iss.ExpiresAt) {
					t.Errorf("granted a redeem for %#v at %v", prev.Tokens[rd.Hash], rd.Now)
				}
			}
		}
	})
}

func TestPropRedeemBurnsToken(t *testing.T) {
	forRuns(t, 80, func(t *testing.T, prev Registry, in Input, next Registry, _ []Command) {
		rd, ok := in.(RedeemRequested)
		if !ok {
			return
		}
		if _, was := prev.Tokens[rd.Hash].(Issued); !was {
			return
		}
		if _, still := next.Tokens[rd.Hash].(Issued); still {
			t.Error("a redeemed token is still Issued")
		}
	})
}

func TestPropLimits(t *testing.T) {
	forRuns(t, 120, func(t *testing.T, _ Registry, _ Input, next Registry, _ []Command) {
		if p := pending(next); p > MaxPending {
			t.Errorf("%d pending tokens", p)
		}
		if a := active(next); a > MaxSessions {
			t.Errorf("%d active sessions", a)
		}
	})
}

func TestPropOpenOnlyTokenFolder(t *testing.T) {
	forRuns(t, 80, func(t *testing.T, prev Registry, in Input, _ Registry, cmds []Command) {
		for _, c := range cmds {
			op, ok := c.(OpenPTY)
			if !ok {
				continue
			}
			rd, isRedeem := in.(RedeemRequested)
			if !isRedeem {
				t.Fatalf("OpenPTY from %T", in)
			}
			iss, _ := prev.Tokens[rd.Hash].(Issued)
			if op.Folder != iss.Folder || rd.Check != FolderTracked || rd.Folder != iss.Folder {
				t.Errorf("OpenPTY folder %v, token %v, input %v/%v", op.Folder, iss.Folder, rd.Folder, rd.Check)
			}
		}
	})
}

type audits struct{ open, closed, failed int }

func TestPropSessionAuditedOnce(t *testing.T) {
	for seed := int64(1); seed <= propRuns; seed++ {
		g := &gen{r: rand.New(rand.NewSource(seed)), now: epoch}
		reg := NewRegistry()
		count := map[core.TerminalSessionID]*audits{}
		for i := 0; i < 120; i++ {
			next, cmds := Step(reg, g.input())
			for _, c := range cmds {
				switch c := c.(type) {
				case OpenPTY:
					count[c.Session] = &audits{} // a new incarnation of the ID
				case WriteAudit:
					a := count[c.Session]
					if a == nil {
						t.Fatalf("seed %d: audit %v for a session that never opened", seed, c.Action)
					}
					switch c.Action {
					case AuditOpen:
						a.open++
					case AuditClose:
						a.closed++
					case AuditOpenFailed:
						a.failed++
					}
				}
			}
			for id, a := range count {
				var want audits
				switch next.Sessions[id].(type) {
				case Opening:
				case Open:
					want = audits{open: 1}
				case Closing:
					want = *a
					if a.closed != 1 || a.failed != 0 || a.open > 1 {
						t.Fatalf("seed %d: closing %s has %+v", seed, id, *a)
					}
				default: // removed
					if a.open > 1 || a.closed > 1 || a.failed > 1 || (a.closed == 1) == (a.failed == 1) || (a.open == 1 && a.closed != 1) {
						t.Fatalf("seed %d: removed %s has %+v", seed, id, *a)
					}
					continue
				}
				if *a != want {
					t.Fatalf("seed %d: %s in %T has %+v, want %+v", seed, id, next.Sessions[id], *a, want)
				}
			}
			reg = next
		}
	}
}

func TestPropClosingEnds(t *testing.T) {
	forRuns(t, 120, func(t *testing.T, prev Registry, in Input, next Registry, cmds []Command) {
		for id, s := range next.Sessions {
			_, nowClosing := s.(Closing)
			_, wasClosing := prev.Sessions[id].(Closing)
			if nowClosing && !wasClosing {
				found := false
				for _, c := range cmds {
					if cp, ok := c.(ClosePTY); ok && cp.Session == id {
						found = true
					}
				}
				if !found {
					t.Errorf("%s entered Closing without ClosePTY", id)
				}
			}
		}
		for id, s := range prev.Sessions {
			if _, ok := s.(Closing); ok {
				if n, there := next.Sessions[id]; there {
					if _, still := n.(Closing); !still {
						t.Errorf("%s left Closing for %T", id, n)
					}
				}
			}
		}
	})
}

func TestPropShutdownDrains(t *testing.T) {
	for seed := int64(1); seed <= propRuns; seed++ {
		g := &gen{r: rand.New(rand.NewSource(seed)), now: epoch}
		reg := NewRegistry()
		down := false
		for i := 0; i < 120; i++ {
			in := g.input()
			if _, ok := in.(Shutdown); ok {
				down = true
			}
			next, cmds := Step(reg, in)
			if down {
				if active(next) != 0 {
					t.Fatalf("seed %d: sessions alive after Shutdown", seed)
				}
				for _, c := range cmds {
					if _, ok := c.(OpenPTY); ok {
						t.Fatalf("seed %d: OpenPTY after Shutdown", seed)
					}
					if rp, ok := c.(Reply); ok && rp.Granted {
						t.Fatalf("seed %d: grant after Shutdown", seed)
					}
				}
			}
			reg = next
		}
	}
}

func TestPropUnknownSessionIgnored(t *testing.T) {
	forRuns(t, 100, func(t *testing.T, prev Registry, in Input, next Registry, cmds []Command) {
		var id core.TerminalSessionID
		switch in := in.(type) {
		case Opened:
			id = in.Session
		case OpenFailed:
			id = in.Session
		case Resized:
			id = in.Session
		case ClientClosed:
			id = in.Session
		case CloseRequested:
			id = in.Session
		case ShellExited:
			id = in.Session
		default:
			return
		}
		if _, known := prev.Sessions[id]; known {
			return
		}
		if !reflect.DeepEqual(prev, next) || len(cmds) != 0 {
			t.Errorf("unknown session %s changed state or emitted %v", id, cmds)
		}
	})
}

func TestPropResizeBounded(t *testing.T) {
	forRuns(t, 100, func(t *testing.T, _ Registry, in Input, next Registry, cmds []Command) {
		for _, c := range cmds {
			if rs, ok := c.(ResizePTY); ok && rs.Size.Validate() != nil {
				t.Errorf("ResizePTY with %v", rs.Size)
			}
			if op, ok := c.(OpenPTY); ok && op.Size.Validate() != nil {
				t.Errorf("OpenPTY with %v", op.Size)
			}
		}
		for id, s := range next.Sessions {
			if o, ok := s.(Open); ok && o.Size.Validate() != nil {
				t.Errorf("%s open with size %v", id, o.Size)
			}
		}
	})
}

func TestPropExpireOnlyTokens(t *testing.T) {
	forRuns(t, 100, func(t *testing.T, prev Registry, in Input, next Registry, _ []Command) {
		w, ok := in.(Wake)
		if !ok {
			return
		}
		want := map[TokenHash]Token{}
		for h, tok := range prev.Tokens {
			if tokenExpiry(tok).After(w.Now) {
				want[h] = tok
			}
		}
		if !reflect.DeepEqual(want, next.Tokens) {
			t.Errorf("tokens after Wake = %v, want %v", next.Tokens, want)
		}
		if !maps.Equal(prev.Sessions, next.Sessions) {
			t.Error("Wake changed sessions")
		}
	})
}

// walkStrings calls f for every string reachable from v.
func walkStrings(v reflect.Value, f func(string)) {
	switch v.Kind() {
	case reflect.String:
		f(v.String())
	case reflect.Interface, reflect.Pointer:
		if !v.IsNil() {
			walkStrings(v.Elem(), f)
		}
	case reflect.Struct:
		for i := 0; i < v.NumField(); i++ {
			walkStrings(v.Field(i), f)
		}
	case reflect.Slice, reflect.Array:
		for i := 0; i < v.Len(); i++ {
			walkStrings(v.Index(i), f)
		}
	case reflect.Map:
		for it := v.MapRange(); it.Next(); {
			walkStrings(it.Key(), f)
			walkStrings(it.Value(), f)
		}
	}
}

// TestNoSecretInOutputs generates raw tokens, feeds only their hashes to the
// core, and looks for the raw text in every state and command.
func TestNoSecretInOutputs(t *testing.T) {
	for seed := int64(1); seed <= 100; seed++ {
		g := &gen{r: rand.New(rand.NewSource(seed)), now: epoch}
		raws := map[TokenHash]string{}
		for i, h := range hashes {
			raw := strings.Repeat(string(rune('a'+i)), 42) + "A"
			hash, err := ParseToken(raw)
			if err != nil {
				t.Fatal(err)
			}
			raws[hash] = raw
			_ = h
		}
		var pool []TokenHash
		for h := range raws {
			pool = append(pool, h)
		}
		reg := NewRegistry()
		for i := 0; i < 80; i++ {
			in := g.input()
			switch x := in.(type) {
			case IssueRequested:
				x.Hash = pick(g.r, pool)
				in = x
			case RedeemRequested:
				x.Hash = pick(g.r, pool)
				in = x
			}
			var cmds []Command
			reg, cmds = Step(reg, in)
			scan := func(s string) {
				for _, raw := range raws {
					if strings.Contains(s, raw) {
						t.Fatalf("raw token found in output: %q", s)
					}
				}
			}
			walkStrings(reflect.ValueOf(reg), scan)
			walkStrings(reflect.ValueOf(cmds), scan)
		}
	}
}

// TestAuditHasNoInput checks that audit entries only hold values from closed
// or ID-shaped sets: no free text can get in.
func TestAuditHasNoInput(t *testing.T) {
	actions := map[AuditAction]bool{AuditOpen: true, AuditClose: true, AuditOpenFailed: true}
	causes := map[CloseCause]bool{"": true, CauseClientClosed: true, CauseShellExited: true, CauseUserClosed: true, CauseAppShutdown: true, CauseOpenFailed: true}
	ids := map[string]bool{"": true}
	for _, s := range sessions {
		ids[string(s)] = true
	}
	for _, f := range folders {
		ids[string(f.Repo)], ids[string(f.Worktree)] = true, true
	}
	seen := 0
	forRuns(t, 120, func(t *testing.T, _ Registry, _ Input, _ Registry, cmds []Command) {
		for _, c := range cmds {
			a, ok := c.(WriteAudit)
			if !ok {
				continue
			}
			seen++
			if !actions[a.Action] || !causes[a.Cause] || !ids[string(a.Session)] || !ids[string(a.Folder.Repo)] || !ids[string(a.Folder.Worktree)] {
				t.Errorf("audit entry with unexpected content: %#v", a)
			}
			if n := reflect.TypeOf(a).NumField(); n != 5 {
				t.Errorf("WriteAudit has %d fields; review what it may carry", n)
			}
		}
	})
	if seen == 0 {
		t.Fatal("the generator produced no audit entries")
	}
}
