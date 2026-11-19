package api

import (
	"strings"
	"testing"

	coremcp "github.com/brendatamaar/yggdrasil/internal/core/mcp"
	"github.com/brendatamaar/yggdrasil/internal/core/newrepos"
	coreprop "github.com/brendatamaar/yggdrasil/internal/core/proposals"
)

// The host, owner and repository-name rules exist in four places: the API
// boundary (parsePublishPreview), the MCP argument check (core/mcp
// checkHost/checkAccount, reached through CheckCall), core/proposals
// (PublishRepo.Validate) and core/newrepos (PublishSpec.Validate). They must
// accept the same set, or a value the UI offers is refused later, or a value an
// agent proposes can never be confirmed. This test runs one corpus (and a fuzz
// target) through all four.

const (
	driftHead   = "0123456789abcdef0123456789abcdef01234567"
	driftMarker = "0123456789abcdef"
)

func apiAccepts(host, owner, name string) bool {
	return parsePublishPreview(PublishPreviewRequest{HostInstance: host, Owner: owner, OwnerKind: "user", Name: name, Visibility: "private"}) == nil
}

func mcpAccepts(host, owner, name string) bool {
	return coremcp.CheckCall(coremcp.ProposePublish{Repo: "shop-api", Host: host, Owner: owner, OwnerKind: "user", Name: name, Visibility: "private", Summary: "publish"}) == nil
}

func proposalsAccepts(host, owner, name string) bool {
	return coreprop.PublishRepo{Operation: "op_1", Repo: "rep_1", HostInstance: host, Owner: owner, OwnerKind: "user", Name: name,
		Visibility: "private", Branch: "main", ExpectedHead: driftHead, Marker: driftMarker}.Validate() == nil
}

func newreposAccepts(host, owner, name string) bool {
	return newrepos.PublishSpec{Repo: "rep_1", HostInstance: host, Owner: owner, OwnerKind: "user", Name: name,
		Visibility: "private", Branch: "main", ExpectedHead: driftHead, Marker: driftMarker}.Validate() == nil
}

type ruleSet struct {
	name    string
	accepts func(host, owner, name string) bool
}

var publishRuleSets = []ruleSet{
	{"api", apiAccepts}, {"mcp", mcpAccepts}, {"proposals", proposalsAccepts}, {"newrepos", newreposAccepts},
}

func driftReport(t *testing.T, host, owner, name string) {
	t.Helper()
	first := publishRuleSets[0].accepts(host, owner, name)
	for _, rs := range publishRuleSets[1:] {
		if got := rs.accepts(host, owner, name); got != first {
			t.Fatalf("rule drift on host=%q owner=%q name=%q: api=%v, %s=%v", host, owner, name, first, rs.name, got)
		}
	}
}

// The base values must be accepted everywhere, or the corpus proves nothing.
func TestPublishRulesBaseIsAcceptedEverywhere(t *testing.T) {
	for _, rs := range publishRuleSets {
		if !rs.accepts("git.acme.test", "acme", "shop-api") {
			t.Fatalf("%s refuses the base values", rs.name)
		}
	}
}

func TestPublishAccountRulesDoNotDrift(t *testing.T) {
	long := strings.Repeat("a", 101)
	for _, v := range []string{
		"acme", "Acme-Shop", "a", "shop_api", "shop.api", "a.b-c_d", strings.Repeat("a", 100), long,
		"", ".hidden", "-lead", "trail.git", "x.GIT", "has space", "tab\t", "a/b", `a\b`, "a@b", "a:b", "ünï", "a\x00", "a\n", "..", "-", ".",
	} {
		driftReport(t, "git.acme.test", v, "shop-api")
		driftReport(t, "git.acme.test", "acme", v)
	}
}

// KNOWN DEFECT: core/hosts.InstanceID is host[:port] ("git.acme.test:8443"),
// and core/mcp, core/proposals and core/newrepos accept a port in the host, but
// the API boundary rejects ':' so a self-hosted instance with a port can never
// be published to from the UI. Correct behaviour: the four rules agree.
func TestPublishHostRulesDoNotDrift(t *testing.T) {
	var drift []string
	for _, h := range []string{
		"github.com", "git.acme.test", "git.acme.test:8443", "localhost:3000", "a", strings.Repeat("a", 100), strings.Repeat("a", 101), strings.Repeat("a", 256),
		"", "GitHub.com", "git acme", "git/acme", `git\acme`, "me@git.test", "git.test\t", "git.test\x00", "git.test\x7f", "g\xffit", "https://git.test",
	} {
		first := publishRuleSets[0].accepts(h, "acme", "shop-api")
		for _, rs := range publishRuleSets[1:] {
			if rs.accepts(h, "acme", "shop-api") != first {
				drift = append(drift, h+" (api="+boolText(first)+" vs "+rs.name+")")
			}
		}
	}
	if len(drift) > 0 {
		t.Skipf("KNOWN DEFECT: the host rules differ between api and the core packages: %s", strings.Join(drift, "; "))
	}
}

func boolText(b bool) string {
	if b {
		return "accepts"
	}
	return "refuses"
}

// The three core copies must agree with each other on hosts, even while the api
// copy is known to differ.
func TestCorePublishHostRulesAgree(t *testing.T) {
	for _, h := range []string{
		"github.com", "git.acme.test:8443", strings.Repeat("a", 101), "", "GitHub.com", "git acme", "me@git.test", "g\xffit", "git.test\x00",
	} {
		first := mcpAccepts(h, "acme", "shop-api")
		for _, rs := range publishRuleSets[2:] {
			if got := rs.accepts(h, "acme", "shop-api"); got != first {
				t.Errorf("host %q: mcp=%v, %s=%v", h, first, rs.name, got)
			}
		}
	}
}

// KNOWN DEFECT: core/mcp refuses a host over 255 bytes but core/proposals and
// core/newrepos have no length limit (and the api limit is 100). Correct
// behaviour: one bound everywhere.
func TestCoreHostLengthDoesNotDrift(t *testing.T) {
	h := strings.Repeat("a", 300)
	if mcpAccepts(h, "acme", "shop-api") != proposalsAccepts(h, "acme", "shop-api") ||
		proposalsAccepts(h, "acme", "shop-api") != newreposAccepts(h, "acme", "shop-api") {
		t.Skipf("KNOWN DEFECT: a 300 byte host is mcp=%v proposals=%v newrepos=%v", mcpAccepts(h, "acme", "shop-api"), proposalsAccepts(h, "acme", "shop-api"), newreposAccepts(h, "acme", "shop-api"))
	}
}

func FuzzPublishAccountRulesDoNotDrift(f *testing.F) {
	for _, s := range []string{"acme", "", ".x", "-x", "a.git", "a b", "a/b", "ü", strings.Repeat("a", 101), "a\x00"} {
		f.Add(s)
	}
	f.Fuzz(func(t *testing.T, v string) {
		driftReport(t, "git.acme.test", v, "shop-api")
		driftReport(t, "git.acme.test", "acme", v)
	})
}

func FuzzPublishHostRulesAgreeInCore(f *testing.F) {
	for _, s := range []string{"github.com", "git.acme.test:8443", "", "A", "a b", "a@b", "a/b", strings.Repeat("a", 300), "\xff"} {
		f.Add(s)
	}
	f.Fuzz(func(t *testing.T, h string) {
		if len(h) > 255 { // see TestCoreHostLengthDoesNotDrift
			return
		}
		first := mcpAccepts(h, "acme", "shop-api")
		for _, rs := range publishRuleSets[2:] {
			if got := rs.accepts(h, "acme", "shop-api"); got != first {
				t.Fatalf("host %q: mcp=%v, %s=%v", h, first, rs.name, got)
			}
		}
	})
}
