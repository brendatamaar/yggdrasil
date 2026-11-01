package changesets

import (
	"fmt"
	"math/rand"
	"slices"
	"strings"
	"testing"

	"github.com/brendatamaar/yggdrasil/internal/core"
)

// C1: a plan holds only pending members of repos in scope and never a landing
// kind (nor publish/subtasks, which are confirmed one at a time).
func TestPropPlanOnlyPendingInScope(t *testing.T) {
	prop(t, func(t *testing.T, before State, in Input, after State, cmds []Command) {
		o := open(after)
		scopes := []Scope{ScopeAll{}, ScopeRepo{Repo: "a"}, ScopeRepo{Repo: "b"}, ScopeRepo{Repo: "zz"}}
		for _, sc := range scopes {
			for _, p := range Plan(after, sc) {
				m, ok := find(o.Members, p.ID)
				switch {
				case !ok:
					t.Fatalf("plan names %s, not a member", p.ID)
				case m.Status != core.ProposalPending:
					t.Errorf("plan holds %s with status %s", p.ID, m.Status)
				case isLanding(m.Kind) || m.Kind == core.ProposalPublish || m.Kind == core.ProposalSubtasks:
					t.Errorf("plan holds a %s member", m.Kind)
				case !inScope(sc, m.Repo):
					t.Errorf("plan for %v holds repo %s", sc, m.Repo)
				case p.Hash != m.Hash:
					t.Errorf("planned hash differs from the member's")
				}
			}
		}
		if c, ok := after.(Confirming); ok {
			for _, p := range c.Queue {
				if m, found := find(o.Members, p.ID); found && isLanding(m.Kind) {
					t.Errorf("queue holds a landing member")
				}
			}
		}
	})
}

// C2: a run confirms a member only with the hash the user saw, and the hash
// equals the member's current hash.
func TestPropConfirmOnlySeenHash(t *testing.T) {
	prop(t, func(t *testing.T, before State, in Input, after State, cmds []Command) {
		for _, c := range cmds {
			cp, ok := c.(ConfirmProposal)
			if !ok {
				continue
			}
			switch v := in.(type) {
			case ConfirmRun:
				m, found := find(open(before).Members, cp.ID)
				if !found || v.Seen[cp.ID] != cp.Hash || m.Hash != cp.Hash {
					t.Errorf("ConfirmRun confirmed %s with hash %q (seen %q, current %q)", cp.ID, cp.Hash, v.Seen[cp.ID], m.Hash)
				}
			case MembersChanged:
				m, found := find(v.Members, cp.ID)
				if !found || m.Hash != cp.Hash {
					t.Errorf("run confirmed %s with a hash that is not the current one", cp.ID)
				}
			default:
				t.Errorf("%T emitted ConfirmProposal", in)
			}
		}
	})
}

// C3: at most one ConfirmProposal is outstanding: the next one only after
// Current reached executed, and always the head of the queue.
func TestPropOneOutstandingConfirm(t *testing.T) {
	prop(t, func(t *testing.T, before State, in Input, after State, cmds []Command) {
		n := count[ConfirmProposal](cmds)
		if n > 1 {
			t.Fatalf("%d ConfirmProposal in one step", n)
		}
		if n == 0 {
			return
		}
		cp := cmds[len(cmds)-1].(ConfirmProposal)
		ac, ok := after.(Confirming)
		if !ok || ac.Current != cp.ID {
			t.Fatalf("ConfirmProposal %s but the state waits for something else", cp.ID)
		}
		switch v := in.(type) {
		case ConfirmRun:
			if _, wasOpen := before.(Open); !wasOpen {
				t.Errorf("run started from %T", before)
			}
		case MembersChanged:
			bc, wasRunning := before.(Confirming)
			if !wasRunning {
				t.Fatalf("next confirm emitted from %T", before)
			}
			cur, _ := find(v.Members, bc.Current)
			if cur.Status != core.ProposalExecuted {
				t.Errorf("next confirm while Current is %s", cur.Status)
			}
			if len(bc.Queue) == 0 || bc.Queue[0].ID != cp.ID {
				t.Errorf("confirmed %s, not the head of the queue", cp.ID)
			}
		}
	})
}

// C4: a run stops at the first failed, rejected or refused member.
func TestPropRunStopsAtFirstProblem(t *testing.T) {
	prop(t, func(t *testing.T, before State, in Input, after State, cmds []Command) {
		bc, running := before.(Confirming)
		if !running {
			return
		}
		problem := false
		switch v := in.(type) {
		case MembersChanged:
			if _, ok := cleanMembers(bc.Set, v.Members); ok {
				cur, found := find(v.Members, bc.Current)
				problem = !found || cur.Status == core.ProposalFailed || cur.Status == core.ProposalRejected
			}
		case ConfirmRefused:
			problem = v.ID == bc.Current
		case StopRun:
			problem = true
		}
		if !problem {
			return
		}
		if _, still := after.(Confirming); still || count[ConfirmProposal](cmds) != 0 {
			t.Errorf("%T at a problem kept the run going", in)
		}
		if h, ok := open(after).LastRun.(RunHalted); !ok || h.Reason == "" {
			t.Errorf("run did not record a halt: %#v", open(after).LastRun)
		}
	})
}

// C5: the plan follows Order (repo position), then kind rank; within a repo
// commit precedes push precedes merge_request.
func TestPropPlanOrder(t *testing.T) {
	prop(t, func(t *testing.T, before State, in Input, after State, cmds []Command) {
		o := open(after)
		pos := func(r core.RepoID) int { return slices.Index(o.Set.Order, r) }
		var prev Member
		for i, p := range Plan(after, ScopeAll{}) {
			m, _ := find(o.Members, p.ID)
			if i > 0 {
				switch {
				case pos(m.Repo) < pos(prev.Repo):
					t.Errorf("repo %s planned after %s against the order %v", m.Repo, prev.Repo, o.Set.Order)
				case m.Repo == prev.Repo && kindRank[m.Kind] < kindRank[prev.Kind]:
					t.Errorf("%s planned after %s in repo %s", m.Kind, prev.Kind, m.Repo)
				}
			}
			prev = m
		}
	})
}

// C6: Order is always a permutation of the set's repos; SetOrder with
// anything else changes nothing.
func TestPropOrderPermutation(t *testing.T) {
	prop(t, func(t *testing.T, before State, in Input, after State, cmds []Command) {
		if o := open(after); !IsPermutation(o.Set.Order, o.Set.Repos) {
			t.Fatalf("order %v is not a permutation of %v", o.Set.Order, o.Set.Repos)
		}
		if so, ok := in.(SetOrder); ok && !IsPermutation(so.Order, open(before).Set.Repos) {
			if changedState(before, after) || len(cmds) != 0 {
				t.Errorf("SetOrder with %v changed the set", so.Order)
			}
		}
	})
}

// C7: ConfirmRun with an un-acknowledged warning changes nothing; an
// acknowledgement of a code that is not raised is ignored.
func TestPropWarningsNeedAck(t *testing.T) {
	prop(t, func(t *testing.T, before State, in Input, after State, cmds []Command) {
		cr, ok := in.(ConfirmRun)
		if !ok {
			return
		}
		raised := Preflight(before, cr.Scope, cr.Deps)
		if changedState(before, after) {
			for _, w := range raised {
				if !slices.Contains(cr.Ack, w.Code) {
					t.Errorf("run started with %s un-acknowledged", w.Code)
				}
			}
			return
		}
		cr3 := cr
		cr3.Ack = append(slices.Clone(cr.Ack), "bogus")
		if again, _ := StepSet(before, cr3); changedState(before, again) != changedState(before, after) {
			t.Errorf("an unknown acknowledgement changed the outcome")
		}
	})
}

// C8: Shipped means every member is terminal, one executed, none failed;
// Closed means nothing is pending or confirmed.
func TestPropShippedClosedMeaning(t *testing.T) {
	prop(t, func(t *testing.T, before State, in Input, after State, cmds []Command) {
		o := open(after)
		switch after.(type) {
		case Shipped:
			executed := 0
			for _, m := range o.Members {
				if m.Status != core.ProposalExecuted && m.Status != core.ProposalRejected {
					t.Errorf("Shipped with a %s member", m.Status)
				}
				if m.Status == core.ProposalExecuted {
					executed++
				}
			}
			if executed == 0 {
				t.Errorf("Shipped with nothing executed")
			}
		case Closed:
			for _, m := range o.Members {
				if isActive(m.Status) {
					t.Errorf("Closed with a %s member", m.Status)
				}
			}
		case Open, Confirming:
			if after.Status() != core.ChangeSetOpen {
				t.Errorf("status %s", after.Status())
			}
		}
	})
}

// C9: SetOrder and Close never change a set while it is Confirming.
func TestPropFrozenWhileConfirming(t *testing.T) {
	prop(t, func(t *testing.T, before State, in Input, after State, cmds []Command) {
		if _, running := before.(Confirming); !running {
			return
		}
		switch in.(type) {
		case SetOrder, Close, ConfirmRun:
			if changedState(before, after) || len(cmds) != 0 {
				t.Errorf("%T changed a running set", in)
			}
		}
	})
}

// C11: every state change emits exactly one WriteAudit and one Publish; a
// step that changes nothing emits no command.
func TestPropAuditPerChange(t *testing.T) {
	prop(t, func(t *testing.T, before State, in Input, after State, cmds []Command) {
		if !changedState(before, after) {
			if len(cmds) != 0 {
				t.Errorf("%T changed nothing but emitted %d commands", in, len(cmds))
			}
			return
		}
		if count[WriteAudit](cmds) != 1 || count[Publish](cmds) != 1 {
			t.Errorf("%T change emitted %d audits and %d publishes", in, count[WriteAudit](cmds), count[Publish](cmds))
		}
	})
}

// A valid MembersChanged with a pending or confirmed member leaves no set
// Shipped or Closed.
func TestPropReopenOnNewPending(t *testing.T) {
	prop(t, func(t *testing.T, before State, in Input, after State, cmds []Command) {
		mc, ok := in.(MembersChanged)
		if !ok {
			return
		}
		if _, valid := cleanMembers(open(before).Set, mc.Members); !valid {
			return
		}
		if slices.ContainsFunc(mc.Members, func(m Member) bool { return isActive(m.Status) }) {
			switch after.(type) {
			case Shipped, Closed:
				t.Errorf("a pending member left the set %T", after)
			}
		}
	})
}

// --- C10: RenderLinks ---

// oracleURLOK is an independent statement of the URL rule for the tests: http
// or https, authority equal to the host (optionally with a port), no userinfo,
// nothing that breaks a one-line description.
func oracleURLOK(u, host string) bool {
	if host == "" || strings.ContainsAny(u, " \\<>\x00\r\n\t\u2028\u2029") {
		return false
	}
	lu := strings.ToLower(u)
	var rest string
	switch {
	case strings.HasPrefix(lu, "https://"):
		rest = u[len("https://"):]
	case strings.HasPrefix(lu, "http://"):
		rest = u[len("http://"):]
	default:
		return false
	}
	if i := strings.IndexAny(rest, "/?#"); i >= 0 {
		rest = rest[:i]
	}
	rest, h := strings.ToLower(rest), strings.ToLower(host)
	return !strings.Contains(rest, "@") && (rest == h || strings.HasPrefix(rest, h+":"))
}

const testHost = "git.example.test"

func checkRender(t *testing.T, template string, order []core.RepoID, branches, links map[core.RepoID]string, host string) string {
	t.Helper()
	out := RenderLinks(template, order, branches, links, host)
	if strings.Count(template, LinkMarker) != 1 {
		if out != template {
			t.Fatalf("template without exactly one marker changed")
		}
		return out
	}
	if out == template {
		return out // fallback for a template that would rebuild a marker
	}
	if strings.Contains(out, LinkMarker) {
		t.Fatalf("marker still present: %q", out)
	}
	prefix, suffix, _ := strings.Cut(template, LinkMarker)
	if !strings.HasPrefix(out, prefix) || !strings.HasSuffix(out, suffix) || len(out) < len(prefix)+len(suffix) {
		t.Fatalf("text outside the marker changed: %q", out)
	}
	block := out[len(prefix) : len(out)-len(suffix)]
	if len(block) > MaxRenderedLinks {
		t.Fatalf("block of %d bytes over the cap", len(block))
	}
	if strings.ContainsFunc(block, func(r rune) bool { return isLineBreak(r) || r == '<' || r == '>' }) {
		t.Fatalf("block has a line break or angle bracket: %q", block)
	}
	for r, u := range links {
		if len(u) >= 8 && strings.Contains(block, u) {
			if !oracleURLOK(u, host) {
				t.Fatalf("unsafe or foreign URL %q for %s rendered", u, r)
			}
			if !slices.Contains(order, r) {
				t.Fatalf("URL of %s rendered although it is not in the order", r)
			}
		}
	}
	return out
}

func TestPropRenderLinksSafe(t *testing.T) {
	names := []core.RepoID{"shop-api", "shop-web", "x\ny", "<!-- ygg:links -->", "a b", "z z", "é"}
	forSeeds(t, func(t *testing.T, r *rand.Rand) {
		var order []core.RepoID
		branches, links := map[core.RepoID]string{}, map[core.RepoID]string{}
		for _, n := range names {
			if r.Intn(3) > 0 {
				order = append(order, n)
				if r.Intn(4) > 0 {
					branches[n] = []string{"feat/x", "ygg/t1", "a\nb", "<b>", ""}[r.Intn(5)]
				}
			}
			if r.Intn(2) == 0 {
				links[n] = []string{
					"https://git.example.test/" + string(n) + "/-/merge_requests/1",
					"https://git.example.test:8443/" + string(n) + "/1",
					"https://evil.test/" + string(n),
					"https://u:p@git.example.test/x",
					"http://git.example.test/a b",
					"javascript:alert(1)",
					"https://git.example.test/a\nb",
				}[r.Intn(7)]
			}
		}
		if r.Intn(5) == 0 && len(order) > 0 {
			order = append(order, order[0]) // duplicate
		}
		template := []string{"Part of a set. " + LinkMarker, LinkMarker, "no marker", LinkMarker + " " + LinkMarker}[r.Intn(4)]
		out := checkRender(t, template, order, branches, links, testHost)
		if template == "Part of a set. "+LinkMarker && out == template {
			t.Fatalf("marker not replaced")
		}
		if r.Intn(10) == 0 { // a long set is cut at whole entries
			for i := 0; i < 400; i++ {
				order = append(order, core.RepoID(fmt.Sprintf("repo-%03d", i)))
			}
			checkRender(t, "x "+LinkMarker, order, branches, links, testHost)
		}
	})
}

func FuzzRenderLinks(f *testing.F) {
	f.Add("d "+LinkMarker, "https://git.example.test/a/b/1", testHost, "feat")
	f.Add("<!-- ygg:li"+LinkMarker+"nks -->", "", "", "")
	f.Add(LinkMarker+LinkMarker, "http://h/\n", "H", "x\ny")
	f.Add("<!--"+LinkMarker+"-->", "https://u@h/x", "h", " ygg:links ")
	f.Fuzz(func(t *testing.T, template, url, host, branch string) {
		order := []core.RepoID{"shop-api", "shop-web"}
		const other = "https://git.example.test/other/1"
		if url == other || len(url) < 8 || strings.Contains("shop-api (ygg/t1): · shop-web (ygg/t1)"+branch, url) {
			url = ""
		}
		checkRender(t, template, order, map[core.RepoID]string{"shop-api": branch, "shop-web": "ygg/t1"},
			map[core.RepoID]string{"shop-api": url, "other": other}, host)
	})
}
