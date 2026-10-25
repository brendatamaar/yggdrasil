package guard

import (
	"time"

	"github.com/brendatamaar/yggdrasil/internal/core"
)

// Trust is the trust state of one repo's own agent config (SF-09, D17). It is
// sealed: Unknown, NoConfig, Untrusted or Trusted. Every state carries the
// repo it belongs to, and StepTrust ignores an input for another repo.
type Trust interface{ isTrust() }

// Unknown: nothing has been scanned yet. It is the starting state, and it
// counts as untrusted (only Ask is admitted) until a scan says otherwise.
type Unknown struct{ Repo core.RepoID }

// NoConfig: a complete scan found no agent config, so there is nothing to
// trust and every mode is allowed.
type NoConfig struct{ Repo core.RepoID }

// Untrusted: the repo has agent config the user has not trusted, or it could
// not be scanned completely.
type Untrusted struct {
	Repo core.RepoID
	// Found is what the user will be shown.
	Found Scan
	Why   Why
}

// Trusted: the user trusted exactly Hash.
type Trusted struct {
	Repo core.RepoID
	Hash string
	At   time.Time
	// Scan is the config the hash was computed from, kept so a revoke can go
	// back to Untrusted without a new scan. (Not in the design table; added
	// because UserRevokes needs "current scan".)
	Scan Scan
}

func (Unknown) isTrust()   {}
func (NoConfig) isTrust()  {}
func (Untrusted) isTrust() {}
func (Trusted) isTrust()   {}

// WhyKind says why a repo is untrusted.
type WhyKind string

const (
	WhyNeverTrusted   WhyKind = "never_trusted"
	WhyConfigChanged  WhyKind = "config_changed"
	WhyRevoked        WhyKind = "revoked"
	WhyScanIncomplete WhyKind = "scan_incomplete"
)

// Why is the reason of an Untrusted state.
type Why struct {
	Kind WhyKind
	// Was is the hash that was trusted, for WhyConfigChanged.
	Was string
}

// TrustInput is an input of StepTrust (sealed): Scanned, ScanFailed,
// UserTrusts or UserRevokes. Time arrives in Now; the core never reads a
// clock.
type TrustInput interface{ isTrustInput() }

// Scanned: the shell read the repo's agent config completely.
type Scanned struct {
	Repo core.RepoID
	Scan Scan
	Now  time.Time
}

// ScanFailed: the shell could not read the repo's agent config completely
// (locked file, unreadable folder). Why is free text for the shell's log; it
// never reaches an audit entry.
type ScanFailed struct {
	Repo core.RepoID
	Why  string
}

// UserTrusts: the user trusted the hash they were shown.
type UserTrusts struct {
	Repo core.RepoID
	Hash string
	Now  time.Time
}

// UserRevokes: the user withdrew trust.
type UserRevokes struct {
	Repo core.RepoID
	Now  time.Time
}

func (Scanned) isTrustInput()     {}
func (ScanFailed) isTrustInput()  {}
func (UserTrusts) isTrustInput()  {}
func (UserRevokes) isTrustInput() {}

func inputRepo(in TrustInput) core.RepoID {
	switch in := in.(type) {
	case Scanned:
		return in.Repo
	case ScanFailed:
		return in.Repo
	case UserTrusts:
		return in.Repo
	case UserRevokes:
		return in.Repo
	}
	return ""
}

// View maps a Trust state onto what Admit needs. Only NoConfig and Trusted
// are fine; Unknown, Untrusted and anything unrecognised fail closed.
func View(t Trust) TrustView {
	switch t.(type) {
	case NoConfig:
		return TrustNone
	case Trusted:
		return TrustTrusted
	}
	return TrustUntrusted
}

// StepTrust is (state, input) -> (state, commands) for repo trust (the
// StepTrust table of docs/design/guard.md). An input for another repo, or an
// unknown input, changes nothing. A nil state is Unknown for the input's repo.
func StepTrust(t Trust, in TrustInput) (Trust, []Command) {
	if t == nil {
		t = Unknown{Repo: inputRepo(in)}
	}
	if repoOf(t) != inputRepo(in) {
		return t, nil
	}
	// A failed scan makes the repo untrusted whatever it was.
	if sf, ok := in.(ScanFailed); ok {
		return scanFailed(t, sf)
	}
	switch st := t.(type) {
	case Unknown:
		if in, ok := in.(Scanned); ok {
			if in.Scan.Empty() {
				return NoConfig{Repo: st.Repo}, nil
			}
			return neverTrusted(st.Repo, in.Scan)
		}
	case NoConfig:
		if in, ok := in.(Scanned); ok && !in.Scan.Empty() {
			return neverTrusted(st.Repo, in.Scan)
		}
	case Untrusted:
		switch in := in.(type) {
		case Scanned:
			if in.Scan.Empty() {
				return NoConfig{Repo: st.Repo}, nil
			}
			return Untrusted{Repo: st.Repo, Found: in.Scan, Why: st.Why}, nil
		case UserTrusts:
			if in.Hash != "" && in.Hash == st.Found.Hash {
				return Trusted{Repo: st.Repo, Hash: in.Hash, At: in.Now, Scan: st.Found}, []Command{
					PersistTrust{Repo: in.Repo, Hash: in.Hash},
					Audit{Entry: trustEntry("trusted", in.Repo, st.Found)},
				}
			}
			return t, []Command{Audit{Entry: AuditEntry{Action: "guard.trust", Outcome: "trust_stale", Repo: in.Repo}}}
		}
	case Trusted:
		switch in := in.(type) {
		case Scanned:
			switch {
			case in.Scan.Hash == st.Hash:
				return t, nil
			case in.Scan.Empty():
				return NoConfig{Repo: st.Repo}, []Command{
					ClearTrust{Repo: in.Repo},
					Audit{Entry: AuditEntry{Action: "guard.trust", Outcome: "config_removed", Repo: in.Repo}},
				}
			default:
				return Untrusted{Repo: st.Repo, Found: in.Scan, Why: Why{Kind: WhyConfigChanged, Was: st.Hash}}, []Command{
					ClearTrust{Repo: in.Repo},
					Audit{Entry: trustEntry("trust_lost", in.Repo, in.Scan)},
				}
			}
		case UserRevokes:
			return Untrusted{Repo: st.Repo, Found: st.Scan, Why: Why{Kind: WhyRevoked}}, []Command{
				ClearTrust{Repo: in.Repo},
				Audit{Entry: trustEntry("revoked", in.Repo, st.Scan)},
			}
		}
	}
	return t, nil
}

func repoOf(t Trust) core.RepoID {
	switch st := t.(type) {
	case Unknown:
		return st.Repo
	case NoConfig:
		return st.Repo
	case Untrusted:
		return st.Repo
	case Trusted:
		return st.Repo
	}
	return ""
}

func neverTrusted(repo core.RepoID, s Scan) (Trust, []Command) {
	return Untrusted{Repo: repo, Found: s, Why: Why{Kind: WhyNeverTrusted}}, []Command{
		Audit{Entry: trustEntry("trust_required", repo, s)},
	}
}

// scanFailed: Untrusted with WhyScanIncomplete. What the user was last shown
// is kept (a trusted repo keeps its scan so the user can compare). The
// persisted trust, if any, is left alone: the next complete scan decides.
func scanFailed(t Trust, sf ScanFailed) (Trust, []Command) {
	var found Scan
	switch st := t.(type) {
	case Untrusted:
		if st.Why.Kind == WhyScanIncomplete {
			return t, nil // already so; stay idempotent
		}
		found = st.Found
	case Trusted:
		found = st.Scan
	}
	return Untrusted{Repo: sf.Repo, Found: found, Why: Why{Kind: WhyScanIncomplete}}, []Command{
		Audit{Entry: AuditEntry{Action: "guard.trust", Outcome: "scan_incomplete", Repo: sf.Repo}},
	}
}

func trustEntry(outcome string, repo core.RepoID, s Scan) AuditEntry {
	e := AuditEntry{Action: "guard.trust", Outcome: outcome, Repo: repo}
	for _, f := range s.Files {
		e.Files = append(e.Files, f.Path)
	}
	return e
}
