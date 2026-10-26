package tasks

import (
	"context"
	"errors"
	"testing"

	"github.com/brendatamaar/yggdrasil/internal/core/guard"
	"github.com/brendatamaar/yggdrasil/internal/db"
)

func trustHarness(t *testing.T, files map[string]string) *harness {
	t.Helper()
	h := newHarness(t, []string{"shop-api"})
	h.adapter.configs = []string{".claude"}
	h.tune = func(c *Config) { c.Adapters = registry{"mock": h.adapter, TrustAgent: h.adapter} }
	if files != nil {
		h.scanner.set(h.repos[0].Path, files)
	}
	h.start()
	return h
}

func TestTrustFlow(t *testing.T) {
	h := trustHarness(t, claudeConfig)
	ctx := context.Background()
	id := h.repos[0].ID

	st, err := h.svc.TrustStatus(ctx, id)
	if err != nil {
		t.Fatal(err)
	}
	u, ok := st.(guard.Untrusted)
	if !ok || u.Why.Kind != guard.WhyNeverTrusted {
		t.Fatalf("status = %#v, want untrusted (never trusted)", st)
	}

	// A stale hash changes nothing and is not an error.
	st, err = h.svc.Trust(ctx, id, "deadbeef")
	if err != nil {
		t.Fatal(err)
	}
	if _, ok := st.(guard.Untrusted); !ok {
		t.Fatalf("stale hash gave %#v", st)
	}
	if on, _, _ := h.store.GetRepoTrust(ctx, id); on {
		t.Fatal("a stale hash was stored")
	}

	st, err = h.svc.Trust(ctx, id, u.Found.Hash)
	if err != nil {
		t.Fatal(err)
	}
	if tr, ok := st.(guard.Trusted); !ok || tr.Hash != u.Found.Hash {
		t.Fatalf("trust gave %#v", st)
	}
	on, hash, _ := h.store.GetRepoTrust(ctx, id)
	if !on || hash != u.Found.Hash {
		t.Fatalf("stored trust = %v %q", on, hash)
	}

	// A later status keeps the trust, and a revoke clears it.
	if st, _ = h.svc.TrustStatus(ctx, id); !isTrusted(st) {
		t.Fatalf("status after trust = %#v", st)
	}
	st, err = h.svc.Revoke(ctx, id)
	if err != nil {
		t.Fatal(err)
	}
	if r, ok := st.(guard.Untrusted); !ok || r.Why.Kind != guard.WhyRevoked || r.Found.Hash != u.Found.Hash {
		t.Fatalf("revoke gave %#v", st)
	}
	if on, _, _ := h.store.GetRepoTrust(ctx, id); on {
		t.Fatal("revoke left the trust stored")
	}

	list, err := h.store.ListAudit(ctx, "repo:"+string(id), 50)
	if err != nil {
		t.Fatal(err)
	}
	var trusted, revoked, stale bool
	for _, e := range list {
		if e.Action != "guard.trust" {
			continue
		}
		s := string(e.Detail)
		trusted = trusted || contains(s, "trusted")
		revoked = revoked || contains(s, "revoked")
		stale = stale || contains(s, "trust_stale")
	}
	if !trusted || !revoked || !stale {
		t.Fatalf("audit trusted=%v revoked=%v stale=%v", trusted, revoked, stale)
	}
}

func isTrusted(t guard.Trust) bool { _, ok := t.(guard.Trusted); return ok }

func contains(s, sub string) bool {
	for i := 0; i+len(sub) <= len(s); i++ {
		if s[i:i+len(sub)] == sub {
			return true
		}
	}
	return false
}

func TestTrustLostWhenConfigChanges(t *testing.T) {
	h := trustHarness(t, claudeConfig)
	h.trust(0, claudeConfig)
	ctx := context.Background()
	h.scanner.set(h.repos[0].Path, map[string]string{".claude/settings.json": `{"hooks":{}}`})
	st, err := h.svc.TrustStatus(ctx, h.repos[0].ID)
	if err != nil {
		t.Fatal(err)
	}
	if u, ok := st.(guard.Untrusted); !ok || u.Why.Kind != guard.WhyConfigChanged {
		t.Fatalf("status = %#v", st)
	}
	// Reading the status has no side effects: the stored trust is cleared at
	// admission, not by a GET.
	if on, _, _ := h.store.GetRepoTrust(ctx, h.repos[0].ID); !on {
		t.Fatal("TrustStatus cleared the stored trust")
	}
	list, _ := h.store.ListAudit(ctx, "repo:"+string(h.repos[0].ID), 50)
	if len(list) != 0 {
		t.Fatalf("TrustStatus wrote %d audit entries", len(list))
	}
}

func TestTrustAuditActors(t *testing.T) {
	h := trustHarness(t, claudeConfig)
	h.trust(0, claudeConfig)
	ctx := context.Background()
	id := h.repos[0].ID
	h.scanner.set(h.repos[0].Path, map[string]string{".claude/settings.json": `{"hooks":{}}`})
	st, err := h.svc.Trust(ctx, id, "deadbeef")
	if err != nil {
		t.Fatal(err)
	}
	if isTrusted(st) {
		t.Fatalf("stale hash trusted: %#v", st)
	}
	list, _ := h.store.ListAudit(ctx, "repo:"+string(id), 50)
	var sys, user bool
	for _, e := range list {
		switch {
		case contains(string(e.Detail), "trust_lost"):
			sys = e.Actor == "system"
		case contains(string(e.Detail), "trust_stale"):
			user = e.Actor == "user"
		}
	}
	if !sys || !user {
		t.Fatalf("actors wrong: scan-derived system=%v, user click user=%v (%d entries)", sys, user, len(list))
	}
}

func TestTrustScanFailureIsUntrusted(t *testing.T) {
	h := trustHarness(t, claudeConfig)
	h.trust(0, claudeConfig)
	ctx := context.Background()
	h.scanner.fail(h.repos[0].Path, errors.New("locked file"))
	st, err := h.svc.Trust(ctx, h.repos[0].ID, "x")
	if err != nil {
		t.Fatal(err)
	}
	if u, ok := st.(guard.Untrusted); !ok || u.Why.Kind != guard.WhyScanIncomplete {
		t.Fatalf("got %#v", st)
	}
	if on, _, _ := h.store.GetRepoTrust(ctx, h.repos[0].ID); on {
		t.Fatal("trust survived an incomplete scan")
	}
}

func TestTrustMissingRepo(t *testing.T) {
	h := trustHarness(t, nil)
	_, err := h.svc.TrustStatus(context.Background(), "rep_nope")
	if !errors.Is(err, db.ErrNotFound) {
		t.Fatalf("got %v, want not found", err)
	}
}

func TestNoConfigIsNoConfig(t *testing.T) {
	h := trustHarness(t, nil)
	st, err := h.svc.TrustStatus(context.Background(), h.repos[0].ID)
	if err != nil {
		t.Fatal(err)
	}
	if _, ok := st.(guard.NoConfig); !ok {
		t.Fatalf("got %#v", st)
	}
}
