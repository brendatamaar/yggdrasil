package guard

import (
	"reflect"
	"testing"
	"time"

	"github.com/brendatamaar/yggdrasil/internal/core"
)

var t0 = time.Date(2026, 1, 1, 12, 0, 0, 0, time.UTC)

func scan(hash string, paths ...string) Scan {
	s := Scan{Hash: hash}
	for _, p := range paths {
		s.Files = append(s.Files, ScanFile{Path: p, WouldRun: []string{"a hook"}})
	}
	return s
}

func cmdTypes(cmds []Command) []string {
	var out []string
	for _, c := range cmds {
		out = append(out, reflect.TypeOf(c).Name())
	}
	return out
}

func TestStepTrustTable(t *testing.T) {
	const repo = core.RepoID("r1")
	cfg := scan("h1", ".claude/settings.json")
	cfg2 := scan("h2", ".claude/settings.json", ".mcp.json")
	un := func(found Scan, why Why) Untrusted { return Untrusted{Repo: repo, Found: found, Why: why} }
	tr := func(hash string, s Scan) Trusted { return Trusted{Repo: repo, Hash: hash, At: t0, Scan: s} }
	none := NoConfig{Repo: repo}
	cases := []struct {
		name string
		from Trust
		in   TrustInput
		to   Trust
		cmds []string
	}{
		{"unknown + empty scan", Unknown{Repo: repo}, Scanned{Repo: repo, Now: t0}, none, nil},
		{"unknown + config found", Unknown{Repo: repo}, Scanned{Repo: repo, Scan: cfg, Now: t0}, un(cfg, Why{Kind: WhyNeverTrusted}), []string{"Audit"}},
		{"unknown + scan failed", Unknown{Repo: repo}, ScanFailed{Repo: repo, Why: "locked"}, un(Scan{}, Why{Kind: WhyScanIncomplete}), []string{"Audit"}},
		{"unknown + trust", Unknown{Repo: repo}, UserTrusts{Repo: repo, Hash: "h1", Now: t0}, Unknown{Repo: repo}, nil},
		{"unknown + revoke", Unknown{Repo: repo}, UserRevokes{Repo: repo, Now: t0}, Unknown{Repo: repo}, nil},
		{"none + empty scan", none, Scanned{Repo: repo, Scan: Scan{}, Now: t0}, none, nil},
		{"none + config found", none, Scanned{Repo: repo, Scan: cfg, Now: t0}, un(cfg, Why{Kind: WhyNeverTrusted}), []string{"Audit"}},
		{"none + scan failed", none, ScanFailed{Repo: repo, Why: "locked"}, un(Scan{}, Why{Kind: WhyScanIncomplete}), []string{"Audit"}},
		{"untrusted + empty scan", un(cfg, Why{Kind: WhyRevoked}), Scanned{Repo: repo, Now: t0}, none, nil},
		{"untrusted + new scan keeps why", un(cfg, Why{Kind: WhyRevoked}), Scanned{Repo: repo, Scan: cfg2, Now: t0}, un(cfg2, Why{Kind: WhyRevoked}), nil},
		{"untrusted + trust the shown hash", un(cfg, Why{Kind: WhyNeverTrusted}), UserTrusts{Repo: repo, Hash: "h1", Now: t0}, tr("h1", cfg), []string{"PersistTrust", "Audit"}},
		{"untrusted + trust a stale hash", un(cfg2, Why{Kind: WhyNeverTrusted}), UserTrusts{Repo: repo, Hash: "h1", Now: t0}, un(cfg2, Why{Kind: WhyNeverTrusted}), []string{"Audit"}},
		{"untrusted + trust an empty hash", un(Scan{Files: cfg.Files}, Why{}), UserTrusts{Repo: repo, Hash: "", Now: t0}, un(Scan{Files: cfg.Files}, Why{}), []string{"Audit"}},
		{"untrusted + revoke", un(cfg, Why{}), UserRevokes{Repo: repo, Now: t0}, un(cfg, Why{}), nil},
		{"untrusted + scan failed", un(cfg, Why{Kind: WhyNeverTrusted}), ScanFailed{Repo: repo, Why: "x"}, un(cfg, Why{Kind: WhyScanIncomplete}), []string{"Audit"}},
		{"trusted + same hash", tr("h1", cfg), Scanned{Repo: repo, Scan: cfg, Now: t0}, tr("h1", cfg), nil},
		{"trusted + config removed", tr("h1", cfg), Scanned{Repo: repo, Now: t0}, none, []string{"ClearTrust", "Audit"}},
		{"trusted + config changed", tr("h1", cfg), Scanned{Repo: repo, Scan: cfg2, Now: t0}, un(cfg2, Why{Kind: WhyConfigChanged, Was: "h1"}), []string{"ClearTrust", "Audit"}},
		{"trusted + revoke", tr("h1", cfg), UserRevokes{Repo: repo, Now: t0}, un(cfg, Why{Kind: WhyRevoked}), []string{"ClearTrust", "Audit"}},
		{"trusted + trust again", tr("h1", cfg), UserTrusts{Repo: repo, Hash: "h2", Now: t0}, tr("h1", cfg), nil},
		{"trusted + scan failed", tr("h1", cfg), ScanFailed{Repo: repo, Why: "x"}, un(cfg, Why{Kind: WhyScanIncomplete}), []string{"Audit"}},
		{"none + trust", none, UserTrusts{Repo: repo, Hash: "h1", Now: t0}, none, nil},
		{"none + revoke", none, UserRevokes{Repo: repo, Now: t0}, none, nil},
	}
	for _, c := range cases {
		t.Run(c.name, func(t *testing.T) {
			got, cmds := StepTrust(c.from, c.in)
			if !reflect.DeepEqual(got, c.to) {
				t.Fatalf("state %#v, want %#v", got, c.to)
			}
			if !reflect.DeepEqual(cmdTypes(cmds), c.cmds) {
				t.Fatalf("commands %v, want %v", cmdTypes(cmds), c.cmds)
			}
		})
	}
}

func TestStepTrustPersistsTheShownHashForThatRepo(t *testing.T) {
	_, cmds := StepTrust(Untrusted{Repo: "r9", Found: scan("h1", "CLAUDE.md")}, UserTrusts{Repo: "r9", Hash: "h1", Now: t0})
	p, ok := cmds[0].(PersistTrust)
	if !ok || p.Repo != "r9" || p.Hash != "h1" {
		t.Fatalf("commands %#v", cmds)
	}
}

func TestViewMapsTrust(t *testing.T) {
	for _, c := range []struct {
		tr   Trust
		want TrustView
	}{
		{NoConfig{Repo: "r"}, TrustNone}, {Trusted{Hash: "h"}, TrustTrusted}, {Untrusted{}, TrustUntrusted},
		{Unknown{Repo: "r"}, TrustUntrusted}, {Unknown{}, TrustUntrusted},
	} {
		if got := View(c.tr); got != c.want {
			t.Errorf("View(%#v) = %s, want %s", c.tr, got, c.want)
		}
	}
	if View(nil) != TrustUntrusted {
		t.Error("an unknown state must view as untrusted")
	}
}

// M3: before the first scan nothing is known, and unknown counts as untrusted.
func TestUnknownIsUntrustedAndAdmitsAskOnly(t *testing.T) {
	var zero Unknown
	v := View(zero)
	if v != TrustUntrusted {
		t.Fatalf("View(Unknown) = %s", v)
	}
	for _, mode := range []core.TaskMode{core.ModeCode, core.ModeCurrentBranch, core.ModeProject} {
		a := Admit(Request{Mode: mode, OptIn: true, Trust: []TrustView{View(zero)}, Enforcement: EnforcementHook})
		if a.Admitted || a.Refusal != ReasonRepoUntrusted {
			t.Errorf("%s admitted on an unknown trust state: %+v", mode, a)
		}
	}
	if a := Admit(Request{Mode: core.ModeAsk, Trust: []TrustView{View(zero)}, Enforcement: EnforcementHook}); !a.Admitted {
		t.Errorf("Ask refused: %+v", a)
	}
}

// A failed scan makes the repo untrusted with WhyScanIncomplete.
func TestScanFailedMakesRepoUntrusted(t *testing.T) {
	for name, from := range map[string]Trust{
		"unknown": Unknown{Repo: "r1"}, "none": NoConfig{Repo: "r1"},
		"trusted": Trusted{Repo: "r1", Hash: "h", Scan: scan("h", "a")}, "untrusted": Untrusted{Repo: "r1", Why: Why{Kind: WhyRevoked}},
	} {
		got, cmds := StepTrust(from, ScanFailed{Repo: "r1", Why: "access denied: C:\\secret\\file"})
		u, ok := got.(Untrusted)
		if !ok || u.Why.Kind != WhyScanIncomplete || u.Repo != "r1" {
			t.Errorf("%s: got %#v", name, got)
		}
		if View(got) != TrustUntrusted {
			t.Errorf("%s: view %s", name, View(got))
		}
		for _, c := range cmds {
			if a, ok := c.(Audit); ok {
				// the free-text reason must not reach the audit entry
				if a.Entry.Outcome != "scan_incomplete" || len(a.Entry.Files) != 0 {
					t.Errorf("%s: audit %+v", name, a.Entry)
				}
			}
		}
	}
	// An untrusted repo whose scan is incomplete cannot be trusted by hash.
	got, _ := StepTrust(Unknown{Repo: "r1"}, ScanFailed{Repo: "r1", Why: "x"})
	if again, _ := StepTrust(got, UserTrusts{Repo: "r1", Hash: "", Now: t0}); View(again) != TrustUntrusted {
		t.Error("an empty hash trusted an incomplete scan")
	}
}

// Inputs for another repo change nothing and ask for nothing.
func TestStepTrustIgnoresOtherRepos(t *testing.T) {
	cfg := scan("h1", ".claude/settings.json")
	states := []Trust{
		Unknown{Repo: "r1"}, NoConfig{Repo: "r1"}, Untrusted{Repo: "r1", Found: cfg},
		Trusted{Repo: "r1", Hash: "h1", At: t0, Scan: cfg},
	}
	inputs := []TrustInput{
		Scanned{Repo: "r2", Scan: cfg, Now: t0}, Scanned{Repo: "r2", Now: t0}, Scanned{Repo: "", Scan: cfg, Now: t0},
		UserTrusts{Repo: "r2", Hash: "h1", Now: t0}, UserRevokes{Repo: "r2", Now: t0}, ScanFailed{Repo: "r2", Why: "x"},
	}
	for _, s := range states {
		for _, in := range inputs {
			got, cmds := StepTrust(s, in)
			if !reflect.DeepEqual(got, s) || len(cmds) != 0 {
				t.Errorf("%#v + %#v -> %#v %v", s, in, got, cmds)
			}
		}
	}
}

func TestStepTrustKeepsTheRepo(t *testing.T) {
	cfg := scan("h1", "CLAUDE.md")
	s, _ := StepTrust(Unknown{Repo: "r1"}, Scanned{Repo: "r1", Scan: cfg, Now: t0})
	s, _ = StepTrust(s, UserTrusts{Repo: "r1", Hash: "h1", Now: t0})
	if tr, ok := s.(Trusted); !ok || tr.Repo != "r1" {
		t.Fatalf("got %#v", s)
	}
}
