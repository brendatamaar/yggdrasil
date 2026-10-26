package claudecode

import (
	"context"
	"errors"
	"os"
	"path/filepath"
	"slices"
	"strings"
	"testing"

	"github.com/brendatamaar/yggdrasil/internal/agents"
	"github.com/brendatamaar/yggdrasil/internal/core"
)

func runStreamHook(t *testing.T, stream string, extra map[string]string) agents.Run {
	t.Helper()
	extra[fakeStream] = mustAbs(t, filepath.Join("testdata", stream))
	r, err := newAdapter(t, "streamhook").Start(context.Background(), startOpts(t, "streamhook", extra))
	if err != nil {
		t.Fatal(err)
	}
	drain(r)
	return r
}

func TestUnhookedToolUsesAreReported(t *testing.T) {
	// The stream shows toolu_scrub0001 and a sub-agent's toolu_scrub0002.
	r := runStreamHook(t, "live-subagent.jsonl", map[string]string{"YGG_FAKE_HOOKED": "toolu_scrub0001"})
	hc, ok := r.(agents.HookCoverageReporter)
	if !ok {
		t.Fatal("the run does not report hook coverage")
	}
	if got := hc.UnhookedToolUses(); !slices.Equal(got, []string{"toolu_scrub0002"}) {
		t.Errorf("UnhookedToolUses = %q", got)
	}
	if got := hc.HookedToolUses(); !slices.Equal(got, []string{"toolu_scrub0001"}) {
		t.Errorf("HookedToolUses = %q", got)
	}

	r = runStreamHook(t, "live-subagent.jsonl", map[string]string{"YGG_FAKE_HOOKED": "toolu_scrub0001,toolu_scrub0002"})
	if got := r.(agents.HookCoverageReporter).UnhookedToolUses(); len(got) != 0 {
		t.Errorf("all judged, but UnhookedToolUses = %q", got)
	}
}

func TestStartRefusesHookOrBinaryInsidePolicyRoots(t *testing.T) {
	for _, mode := range []core.TaskMode{core.ModeAsk, core.ModeCode} {
		o := startOpts(t, "stream", nil)
		o.Mode = mode
		o.Policy = policyFor(t, mode, o.Dir)
		inside := filepath.Join(o.Dir, "ygg.exe")
		if err := os.WriteFile(inside, []byte("x"), 0o700); err != nil {
			t.Fatal(err)
		}
		for name, a := range map[string]*Adapter{
			"hook":   New(Options{Binary: testExe(t), HookExecutable: inside, TempDir: t.TempDir()}),
			"binary": New(Options{Binary: inside, HookExecutable: testExe(t), TempDir: t.TempDir()}),
		} {
			r, err := a.Start(context.Background(), o)
			if err == nil {
				drain(r)
				t.Fatalf("%s/%s: Start accepted an executable inside a policy root", mode, name)
			}
			var ue *UserError
			if !errors.As(err, &ue) || strings.Contains(ue.Message, o.Dir) {
				t.Errorf("%s/%s: error = %v", mode, name, err)
			}
		}
	}
}

func TestTamperedPolicyIsReported(t *testing.T) {
	r := runStreamHook(t, "edits.jsonl", map[string]string{"YGG_FAKE_TAMPER": "1"})
	tr, ok := r.(agents.TamperReporter)
	if !ok || !tr.Tampered() {
		t.Fatal("a run that rewrote policy.json was not reported as tampered")
	}
	if r := runStreamHook(t, "edits.jsonl", map[string]string{}); r.(agents.TamperReporter).Tampered() {
		t.Error("an untouched run was reported as tampered")
	}
}

func TestTamperEmitsAnErrorEvent(t *testing.T) {
	o := startOpts(t, "streamhook", map[string]string{"YGG_FAKE_TAMPER": "1", fakeStream: mustAbs(t, filepath.Join("testdata", "edits.jsonl"))})
	sink := &collectSink{}
	res, err := agents.Execute(context.Background(), newAdapter(t, "streamhook"), o, sink)
	if err == nil && res.Status == agents.RunCompleted {
		t.Error("a tampered run completed")
	}
	found := false
	for _, e := range sink.all() {
		found = found || (e.Kind == agents.EventError && strings.Contains(e.Error, "safety files"))
	}
	if !found {
		t.Error("no tamper error event")
	}
}

func TestStartRefusesRunFolderInsidePolicyRoots(t *testing.T) {
	o := startOpts(t, "stream", nil)
	a := New(Options{Binary: testExe(t), HookExecutable: testExe(t), TempDir: o.Dir}) // run folder lands in the repo
	r, err := a.Start(context.Background(), o)
	if err == nil {
		drain(r)
		t.Fatal("a run folder inside the repo was accepted")
	}
	if left, _ := filepath.Glob(filepath.Join(o.Dir, "ygg-claude-*")); len(left) != 0 {
		t.Errorf("the refused run folder was left behind: %v", left)
	}
}

func TestDeniesAreCappedAndCounted(t *testing.T) {
	r := runStreamHook(t, "edits.jsonl", map[string]string{"YGG_FAKE_FLOOD": "1"})
	if n := len(r.(agents.DenyReporter).Denies()); n != 100 {
		t.Errorf("len(Denies) = %d, want 100", n)
	}
	dc, ok := r.(agents.DenyCountReporter)
	if !ok || dc.DenyCount() != 130 {
		t.Errorf("DenyCount ok=%v", ok)
	}
}

func TestOversizedHookLogFailsClosed(t *testing.T) {
	log := filepath.Join(t.TempDir(), "hook.log")
	big := strings.Repeat(`{"event":"PreToolUse","decision":"allow","toolUseId":"x"}`+"\n", (maxHookLog/50)+10)
	if err := os.WriteFile(log, []byte(big), 0o600); err != nil {
		t.Fatal(err)
	}
	if _, err := readHookLog(log); err == nil {
		t.Error("a log over the cap was read in full")
	}
	// The hook refuses to add to a full log: enforcing calls are denied.
	if err := appendRecord(log, hookRecord{Decision: decisionAllow}); err == nil {
		t.Error("a full log accepted another record")
	}
}

func TestStartRefusesWithoutAdmission(t *testing.T) {
	o := startOpts(t, "stream", nil)
	o.Admitted = false
	r, err := newAdapter(t, "stream").Start(context.Background(), o)
	if err == nil {
		drain(r)
		t.Fatal("Start accepted a run that guard.Admit did not admit")
	}
	var ue *UserError
	if !errors.As(err, &ue) {
		t.Errorf("error = %v", err)
	}
}
