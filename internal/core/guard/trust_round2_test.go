package guard

import "testing"

// M-A/S3: while the scan is incomplete, trusting is refused.
func TestUserTrustsRefusedWhileScanIncomplete(t *testing.T) {
	cfg := scan("h1", "a")
	for name, from := range map[string]Trust{
		"from trusted":   Trusted{Repo: "r1", Hash: "h1", Scan: cfg},
		"from untrusted": Untrusted{Repo: "r1", Found: cfg, Why: Why{Kind: WhyNeverTrusted}},
		"from unknown":   Unknown{Repo: "r1"},
	} {
		t.Run(name, func(t *testing.T) {
			s, _ := StepTrust(from, ScanFailed{Repo: "r1", Why: "locked"})
			got, cmds := StepTrust(s, UserTrusts{Repo: "r1", Hash: "h1", Now: t0})
			if _, ok := got.(Trusted); ok {
				t.Fatal("trusted while the scan was incomplete")
			}
			if got.(Untrusted).Why.Kind != WhyScanIncomplete || len(cmds) != 1 {
				t.Fatalf("got %#v %#v", got, cmds)
			}
			if a, ok := cmds[0].(Audit); !ok || a.Entry.Outcome != "trust_stale" {
				t.Fatalf("cmds %#v", cmds)
			}
		})
	}
}

// A complete scan recomputes Why; a revoke stays revoked.
func TestScannedRecomputesWhy(t *testing.T) {
	cfg := scan("h1", "a")
	cfg2 := scan("h2", "b")
	fromTrusted, _ := StepTrust(Trusted{Repo: "r1", Hash: "h1", Scan: cfg}, ScanFailed{Repo: "r1", Why: "x"})
	got, _ := StepTrust(fromTrusted, Scanned{Repo: "r1", Scan: cfg2, Now: t0})
	if u := got.(Untrusted); u.Why != (Why{Kind: WhyConfigChanged, Was: "h1"}) || u.Found.Hash != "h2" {
		t.Fatalf("after trusted: %#v", got)
	}
	fromUnknown, _ := StepTrust(Unknown{Repo: "r1"}, ScanFailed{Repo: "r1", Why: "x"})
	got, _ = StepTrust(fromUnknown, Scanned{Repo: "r1", Scan: cfg, Now: t0})
	if u := got.(Untrusted); u.Why.Kind != WhyNeverTrusted {
		t.Fatalf("after unknown: %#v", got)
	}
	// ... and then the user can trust.
	got, _ = StepTrust(got, UserTrusts{Repo: "r1", Hash: "h1", Now: t0})
	if _, ok := got.(Trusted); !ok {
		t.Fatalf("trust after complete scan: %#v", got)
	}
	revoked, _ := StepTrust(Trusted{Repo: "r1", Hash: "h1", Scan: cfg}, UserRevokes{Repo: "r1", Now: t0})
	failed, _ := StepTrust(revoked, ScanFailed{Repo: "r1", Why: "x"})
	got, _ = StepTrust(failed, Scanned{Repo: "r1", Scan: cfg2, Now: t0})
	if u := got.(Untrusted); u.Why.Kind != WhyRevoked {
		t.Fatalf("revoked: %#v", got)
	}
}

// An incomplete scan never yields Trusted.
func TestPropIncompleteScanNeverTrusted(t *testing.T) {
	driveTrust(t, func(prev Trust, in TrustInput, next Trust, cmds []Command, _ string) {
		if u, ok := prev.(Untrusted); ok && u.Why.Kind == WhyScanIncomplete {
			if _, tr := next.(Trusted); tr {
				t.Fatalf("incomplete %#v became Trusted by %#v", prev, in)
			}
		}
	})
}
