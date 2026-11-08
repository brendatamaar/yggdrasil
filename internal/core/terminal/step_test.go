package terminal

import (
	"reflect"
	"testing"
	"time"
)

func TestLimitsPinned(t *testing.T) {
	if TokenTTL != 30*time.Second || MaxPending != 8 || MaxSessions != 4 || MaxCols != 500 || MaxRows != 500 {
		t.Fatal("limits changed; update docs/design/terminal.md")
	}
}

var (
	fa = Folder{Repo: "repo_a"}
	h1 = hh(1)
)

func issueAt(now time.Time) IssueRequested {
	return IssueRequested{Hash: h1, Folder: fa, Now: now, Reply: "i"}
}

func redeemAt(now time.Time) RedeemRequested {
	return RedeemRequested{Hash: h1, Folder: fa, Session: "ts_1", Size: Size{80, 24}, Check: FolderTracked, Now: now, Reply: "r"}
}

func reply(cmds []Command, kind ReplyKind) Reply {
	for _, c := range cmds {
		if r, ok := c.(Reply); ok && r.Kind == kind {
			return r
		}
	}
	return Reply{}
}

func TestIssueGrantsAndSchedulesExpiry(t *testing.T) {
	r, cmds := Step(NewRegistry(), issueAt(epoch))
	if _, ok := r.Tokens[h1].(Issued); !ok {
		t.Fatalf("token = %#v", r.Tokens[h1])
	}
	if !reflect.DeepEqual(cmds, []Command{
		ScheduleWake{Kind: WakeExpire, At: epoch.Add(TokenTTL)},
		Reply{ID: "i", Kind: ReplyIssue, Granted: true},
	}) {
		t.Fatalf("cmds = %#v", cmds)
	}
}

func TestIssueRefusals(t *testing.T) {
	r, _ := Step(NewRegistry(), issueAt(epoch))
	_, cmds := Step(r, issueAt(epoch))
	if got := reply(cmds, ReplyIssue).Refusal; got != RefusedCollision {
		t.Errorf("duplicate hash: %q", got)
	}
	full := NewRegistry()
	for i := 0; i < MaxPending; i++ {
		full, _ = Step(full, IssueRequested{Hash: hashes[i], Folder: fa, Now: epoch})
	}
	_, cmds = Step(full, IssueRequested{Hash: hashes[MaxPending], Folder: fa, Now: epoch})
	if got := reply(cmds, ReplyIssue).Refusal; got != RefusedTooManyPending {
		t.Errorf("full: %q", got)
	}
	down, _ := Step(NewRegistry(), Shutdown{Now: epoch})
	_, cmds = Step(down, issueAt(epoch))
	if got := reply(cmds, ReplyIssue).Refusal; got != RefusedShuttingDown {
		t.Errorf("draining: %q", got)
	}
}

func TestRedeemOutcomes(t *testing.T) {
	issued, _ := Step(NewRegistry(), issueAt(epoch))
	soon := epoch.Add(5 * time.Second)

	r, cmds := Step(issued, redeemAt(soon))
	if rp := reply(cmds, ReplyRedeem); !rp.Granted || rp.Session != "ts_1" {
		t.Fatalf("reply = %#v", rp)
	}
	if _, ok := r.Sessions["ts_1"].(Opening); !ok {
		t.Fatalf("session = %#v", r.Sessions["ts_1"])
	}
	if _, ok := cmds[0].(OpenPTY); !ok {
		t.Fatalf("first command = %#v", cmds[0])
	}
	_, cmds = Step(r, redeemAt(soon))
	if got := reply(cmds, ReplyRedeem).Refusal; got != RefusedReused {
		t.Errorf("replay: %q", got)
	}

	_, cmds = Step(NewRegistry(), redeemAt(soon))
	if got := reply(cmds, ReplyRedeem).Refusal; got != RefusedUnknown {
		t.Errorf("unknown: %q", got)
	}

	exp, cmds := Step(issued, redeemAt(epoch.Add(TokenTTL)))
	if got := reply(cmds, ReplyRedeem).Refusal; got != RefusedExpired {
		t.Errorf("expired: %q", got)
	}
	if _, there := exp.Tokens[h1]; there {
		t.Error("expired token kept")
	}

	untracked := redeemAt(soon)
	untracked.Check = FolderUntracked
	r, cmds = Step(issued, untracked)
	if got := reply(cmds, ReplyRedeem).Refusal; got != RefusedNotTracked {
		t.Errorf("untracked: %q", got)
	}
	if _, ok := r.Tokens[h1].(Consumed); !ok {
		t.Error("a refused redeem must burn the token")
	}

	other := redeemAt(soon)
	other.Folder = Folder{Repo: "repo_b"}
	_, cmds = Step(issued, other)
	if got := reply(cmds, ReplyRedeem).Refusal; got != RefusedNotTracked {
		t.Errorf("other folder: %q", got)
	}
}

func TestRedeemSessionLimit(t *testing.T) {
	reg := NewRegistry()
	for i := 0; i < MaxSessions; i++ {
		reg.Sessions[sessions[i]] = Opening{Folder: fa}
	}
	reg, _ = Step(reg, issueAt(epoch))
	_, cmds := Step(reg, redeemAt(epoch))
	if got := reply(cmds, ReplyRedeem).Refusal; got != RefusedTooManySessions {
		t.Errorf("limit: %q", got)
	}
}

func lifecycle(t *testing.T) Registry {
	t.Helper()
	r, _ := Step(NewRegistry(), issueAt(epoch))
	r, _ = Step(r, redeemAt(epoch))
	return r
}

func TestOpenedResizeAndClose(t *testing.T) {
	r := lifecycle(t)
	r, cmds := Step(r, Opened{Session: "ts_1", Pid: 7, Now: epoch})
	if !reflect.DeepEqual(cmds, []Command{WriteAudit{Action: AuditOpen, Session: "ts_1", Folder: fa}}) {
		t.Fatalf("cmds = %#v", cmds)
	}
	if o := r.Sessions["ts_1"].(Open); o.Pid != 7 || o.Size != (Size{80, 24}) {
		t.Fatalf("open = %#v", o)
	}
	r, cmds = Step(r, Resized{Session: "ts_1", Size: Size{100, 40}})
	if !reflect.DeepEqual(cmds, []Command{ResizePTY{Session: "ts_1", Size: Size{100, 40}}}) {
		t.Fatalf("resize cmds = %#v", cmds)
	}
	_, cmds = Step(r, Resized{Session: "ts_1", Size: Size{0, 40}})
	if len(cmds) != 0 {
		t.Fatalf("bad resize emitted %#v", cmds)
	}
	r, cmds = Step(r, CloseRequested{Session: "ts_1", Now: epoch})
	want := []Command{ClosePTY{Session: "ts_1"}, WriteAudit{Action: AuditClose, Session: "ts_1", Folder: fa, Cause: CauseUserClosed}}
	if !reflect.DeepEqual(cmds, want) {
		t.Fatalf("close cmds = %#v", cmds)
	}
	r, cmds = Step(r, ShellExited{Session: "ts_1", Code: 0, Now: epoch})
	if len(cmds) != 0 || len(r.Sessions) != 0 {
		t.Fatalf("after exit: %v %#v", r.Sessions, cmds)
	}
}

func TestShellExitedFromOpenAuditsCode(t *testing.T) {
	r := lifecycle(t)
	r, _ = Step(r, Opened{Session: "ts_1", Pid: 7, Now: epoch})
	r, cmds := Step(r, ShellExited{Session: "ts_1", Code: 3, Now: epoch})
	want := []Command{WriteAudit{Action: AuditClose, Session: "ts_1", Folder: fa, Cause: CauseShellExited, Code: 3}}
	if !reflect.DeepEqual(cmds, want) || len(r.Sessions) != 0 {
		t.Fatalf("got %#v %v", cmds, r.Sessions)
	}
}

func TestOpenFailed(t *testing.T) {
	r := lifecycle(t)
	r, cmds := Step(r, OpenFailed{Session: "ts_1", Now: epoch})
	want := []Command{WriteAudit{Action: AuditOpenFailed, Session: "ts_1", Folder: fa, Cause: CauseOpenFailed}}
	if !reflect.DeepEqual(cmds, want) || len(r.Sessions) != 0 {
		t.Fatalf("got %#v %v", cmds, r.Sessions)
	}
}

func TestCloseWhileOpeningThenStartFails(t *testing.T) {
	r := lifecycle(t)
	r, cmds := Step(r, ClientClosed{Session: "ts_1", Now: epoch})
	if len(cmds) != 2 {
		t.Fatalf("cmds = %#v", cmds)
	}
	r, cmds = Step(r, Opened{Session: "ts_1", Pid: 3, Now: epoch})
	if len(cmds) != 0 {
		t.Fatal("Opened in Closing must be ignored")
	}
	r, cmds = Step(r, OpenFailed{Session: "ts_1", Now: epoch})
	if len(cmds) != 0 || len(r.Sessions) != 0 {
		t.Fatalf("got %#v %v", cmds, r.Sessions)
	}
}

func TestShutdownClosesEverySessionOnce(t *testing.T) {
	r := lifecycle(t)
	r, _ = Step(r, Opened{Session: "ts_1", Pid: 3, Now: epoch})
	r, cmds := Step(r, Shutdown{Now: epoch})
	if len(cmds) != 2 || r.Phase != Draining {
		t.Fatalf("got %#v", cmds)
	}
	_, cmds = Step(r, Shutdown{Now: epoch})
	if len(cmds) != 0 {
		t.Fatalf("second shutdown emitted %#v", cmds)
	}
}

func TestWakeReschedulesWhileTokensRemain(t *testing.T) {
	r, _ := Step(NewRegistry(), issueAt(epoch))
	r, _ = Step(r, IssueRequested{Hash: hh(2), Folder: fa, Now: epoch.Add(10 * time.Second)})
	r, cmds := Step(r, Wake{Kind: WakeExpire, Now: epoch.Add(TokenTTL)})
	if len(r.Tokens) != 1 {
		t.Fatalf("tokens = %v", r.Tokens)
	}
	if !reflect.DeepEqual(cmds, []Command{ScheduleWake{Kind: WakeExpire, At: epoch.Add(10*time.Second + TokenTTL)}}) {
		t.Fatalf("cmds = %#v", cmds)
	}
	_, cmds = Step(r, Wake{Kind: "other", Now: epoch.Add(time.Hour)})
	if len(cmds) != 0 {
		t.Fatal("unknown wake kind must be ignored")
	}
}
