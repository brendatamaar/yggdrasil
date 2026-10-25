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
	cases := []struct {
		name string
		from Trust
		in   TrustInput
		to   Trust
		cmds []string
	}{
		{"none + empty scan", NoConfig{}, Scanned{Repo: repo, Scan: Scan{}, Now: t0}, NoConfig{}, nil},
		{"none + config found", NoConfig{}, Scanned{Repo: repo, Scan: cfg, Now: t0}, Untrusted{Found: cfg, Why: Why{Kind: WhyNeverTrusted}}, []string{"Audit"}},
		{"untrusted + empty scan", Untrusted{Found: cfg, Why: Why{Kind: WhyRevoked}}, Scanned{Repo: repo, Now: t0}, NoConfig{}, nil},
		{"untrusted + new scan keeps why", Untrusted{Found: cfg, Why: Why{Kind: WhyRevoked}}, Scanned{Repo: repo, Scan: cfg2, Now: t0},
			Untrusted{Found: cfg2, Why: Why{Kind: WhyRevoked}}, nil},
		{"untrusted + trust the shown hash", Untrusted{Found: cfg, Why: Why{Kind: WhyNeverTrusted}}, UserTrusts{Repo: repo, Hash: "h1", Now: t0},
			Trusted{Hash: "h1", At: t0, Scan: cfg}, []string{"PersistTrust", "Audit"}},
		{"untrusted + trust a stale hash", Untrusted{Found: cfg2, Why: Why{Kind: WhyNeverTrusted}}, UserTrusts{Repo: repo, Hash: "h1", Now: t0},
			Untrusted{Found: cfg2, Why: Why{Kind: WhyNeverTrusted}}, []string{"Audit"}},
		{"untrusted + trust an empty hash", Untrusted{Found: Scan{Files: cfg.Files}}, UserTrusts{Repo: repo, Hash: "", Now: t0},
			Untrusted{Found: Scan{Files: cfg.Files}}, []string{"Audit"}},
		{"untrusted + revoke", Untrusted{Found: cfg}, UserRevokes{Repo: repo, Now: t0}, Untrusted{Found: cfg}, nil},
		{"trusted + same hash", Trusted{Hash: "h1", At: t0, Scan: cfg}, Scanned{Repo: repo, Scan: cfg, Now: t0}, Trusted{Hash: "h1", At: t0, Scan: cfg}, nil},
		{"trusted + config removed", Trusted{Hash: "h1", At: t0, Scan: cfg}, Scanned{Repo: repo, Now: t0}, NoConfig{}, []string{"ClearTrust", "Audit"}},
		{"trusted + config changed", Trusted{Hash: "h1", At: t0, Scan: cfg}, Scanned{Repo: repo, Scan: cfg2, Now: t0},
			Untrusted{Found: cfg2, Why: Why{Kind: WhyConfigChanged, Was: "h1"}}, []string{"ClearTrust", "Audit"}},
		{"trusted + revoke", Trusted{Hash: "h1", At: t0, Scan: cfg}, UserRevokes{Repo: repo, Now: t0},
			Untrusted{Found: cfg, Why: Why{Kind: WhyRevoked}}, []string{"ClearTrust", "Audit"}},
		{"trusted + trust again", Trusted{Hash: "h1", At: t0, Scan: cfg}, UserTrusts{Repo: repo, Hash: "h2", Now: t0}, Trusted{Hash: "h1", At: t0, Scan: cfg}, nil},
		{"none + trust", NoConfig{}, UserTrusts{Repo: repo, Hash: "h1", Now: t0}, NoConfig{}, nil},
		{"none + revoke", NoConfig{}, UserRevokes{Repo: repo, Now: t0}, NoConfig{}, nil},
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
	_, cmds := StepTrust(Untrusted{Found: scan("h1", "CLAUDE.md")}, UserTrusts{Repo: "r9", Hash: "h1", Now: t0})
	p, ok := cmds[0].(PersistTrust)
	if !ok || p.Repo != "r9" || p.Hash != "h1" {
		t.Fatalf("commands %#v", cmds)
	}
}

func TestViewMapsTrust(t *testing.T) {
	for _, c := range []struct {
		tr   Trust
		want TrustView
	}{{NoConfig{}, TrustNone}, {Trusted{Hash: "h"}, TrustTrusted}, {Untrusted{}, TrustUntrusted}} {
		if got := View(c.tr); got != c.want {
			t.Errorf("View(%#v) = %s, want %s", c.tr, got, c.want)
		}
	}
	if View(nil) != TrustUntrusted {
		t.Error("an unknown state must view as untrusted")
	}
}
