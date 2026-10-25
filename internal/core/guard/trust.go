package guard

import (
	"time"

	"github.com/brendatamaar/yggdrasil/internal/core"
)

// Trust is the trust state of one repo's own agent config (SF-09, D17). It is
// sealed: NoConfig, Untrusted or Trusted.
type Trust interface{ isTrust() }

// NoConfig: the repo ships no agent config, so there is nothing to trust and
// every mode is allowed.
type NoConfig struct{}

// Untrusted: the repo has agent config the user has not trusted.
type Untrusted struct {
	// Found is what the user will be shown.
	Found Scan
	Why   Why
}

// Trusted: the user trusted exactly Hash.
type Trusted struct {
	Hash string
	At   time.Time
	// Scan is the config the hash was computed from, kept so a revoke can go
	// back to Untrusted without a new scan. (Not in the design table; added
	// because UserRevokes needs "current scan".)
	Scan Scan
}

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

// TrustInput is an input of StepTrust (sealed): Scanned, UserTrusts or
// UserRevokes. Time arrives in Now; the core never reads a clock.
type TrustInput interface{ isTrustInput() }

// Scanned: the shell read the repo's agent config.
type Scanned struct {
	Repo core.RepoID
	Scan Scan
	Now  time.Time
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
func (UserTrusts) isTrustInput()  {}
func (UserRevokes) isTrustInput() {}

// View maps a Trust state onto what Admit needs.
func View(t Trust) TrustView {
	switch t.(type) {
	case NoConfig:
		return TrustNone
	case Trusted:
		return TrustTrusted
	}
	// Untrusted, and anything unknown: fail closed.
	return TrustUntrusted
}

// StepTrust is (state, input) -> (state, commands) for repo trust (the
// StepTrust table of docs/design/guard.md).
//
// STUB (task G1 implements it): returns the state unchanged and no commands.
func StepTrust(t Trust, in TrustInput) (Trust, []Command) {
	_ = in
	return t, nil
}
