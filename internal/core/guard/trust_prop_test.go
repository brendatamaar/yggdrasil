package guard

import (
	"fmt"
	"math/rand"
	"testing"
)

func genTrustInput(r *rand.Rand, s Trust) TrustInput {
	hashes := []string{"h1", "h2", "h3"}
	switch r.Intn(6) {
	case 0:
		return UserRevokes{Repo: "r1", Now: t0}
	case 1, 2:
		h := pick(r, hashes...)
		if u, ok := s.(Untrusted); ok && r.Intn(2) == 0 {
			h = u.Found.Hash
		}
		return UserTrusts{Repo: "r1", Hash: h, Now: t0}
	case 3:
		return Scanned{Repo: "r1", Scan: Scan{}, Now: t0}
	default:
		h := pick(r, hashes...)
		return Scanned{Repo: "r1", Scan: scan(h, fmt.Sprintf("f%s", h)), Now: t0}
	}
}

// driveTrust runs random sequences and hands each step to chk together with
// the hash of the last scan seen (empty before any scan, "-" for an empty scan).
func driveTrust(t *testing.T, chk func(prev Trust, in TrustInput, next Trust, cmds []Command, lastScan string)) {
	t.Helper()
	forSeeds(t, func(t *testing.T, r *rand.Rand) {
		var s Trust = NoConfig{}
		last := ""
		for i := 0; i < 30; i++ {
			in := genTrustInput(r, s)
			next, cmds := StepTrust(s, in)
			if sc, ok := in.(Scanned); ok {
				last = sc.Scan.Hash
				if sc.Scan.Empty() {
					last = "-"
				}
			}
			chk(s, in, next, cmds, last)
			s = next
		}
	})
}

// T1
func TestPropTrustOnlyTheShownHash(t *testing.T) {
	driveTrust(t, func(prev Trust, in TrustInput, next Trust, cmds []Command, _ string) {
		nt, ok := next.(Trusted)
		if !ok {
			return
		}
		if pt, was := prev.(Trusted); was && pt.Hash == nt.Hash {
			return // stayed Trusted
		}
		pu, ok := prev.(Untrusted)
		ut, ok2 := in.(UserTrusts)
		if !ok || !ok2 || pu.Found.Hash != nt.Hash || ut.Hash != nt.Hash {
			t.Fatalf("Trusted reached from %#v by %#v", prev, in)
		}
		persisted := false
		for _, c := range cmds {
			if p, ok := c.(PersistTrust); ok && p.Hash == nt.Hash {
				persisted = true
			}
		}
		if !persisted {
			t.Fatalf("Trusted without PersistTrust: %#v", cmds)
		}
	})
}

// T2
func TestPropChangedConfigLosesTrust(t *testing.T) {
	driveTrust(t, func(prev Trust, in TrustInput, next Trust, cmds []Command, _ string) {
		pt, ok := prev.(Trusted)
		sc, ok2 := in.(Scanned)
		if !ok || !ok2 || sc.Scan.Hash == pt.Hash {
			return
		}
		if _, still := next.(Trusted); still {
			t.Fatalf("still trusted after a different scan: %#v", sc.Scan)
		}
		for _, c := range cmds {
			if _, ok := c.(ClearTrust); ok {
				return
			}
		}
		t.Fatalf("no ClearTrust: %#v", cmds)
	})
}

// T3
func TestPropTrustMatchesLastScan(t *testing.T) {
	driveTrust(t, func(prev Trust, in TrustInput, next Trust, cmds []Command, last string) {
		if nt, ok := next.(Trusted); ok && nt.Hash != last {
			t.Fatalf("trusted %q but the last scan was %q", nt.Hash, last)
		}
	})
}

// Only the effects trust may ask for.
func TestPropTrustCommandsAreTrustOnly(t *testing.T) {
	driveTrust(t, func(prev Trust, in TrustInput, next Trust, cmds []Command, _ string) {
		for _, c := range cmds {
			switch c.(type) {
			case PersistTrust, ClearTrust, Audit:
			default:
				t.Fatalf("unexpected command %T", c)
			}
		}
	})
}
