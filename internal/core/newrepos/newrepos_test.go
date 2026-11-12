package newrepos

import (
	"errors"
	"strings"
	"testing"
)

func validInit() InitSpec {
	return InitSpec{Project: "prj_1", Repo: "rep_1", Leaf: "shop-new", Branch: "main", README: "# x\n", Ignore: "/bin/\n", CommitMessage: "Initial commit"}
}

func validPublish() PublishSpec {
	return PublishSpec{
		Repo: "rep_1", HostInstance: "github.com", Owner: "acme", OwnerKind: OwnerOrg, Name: "shop-new",
		Visibility: VisibilityPrivate, Branch: "main", ExpectedHead: strings.Repeat("a", 40), Marker: strings.Repeat("b", 16),
	}
}

func TestInitSpecAcceptedImpliesValid(t *testing.T) {
	s, err := ParseInitSpec(validInit())
	if err != nil {
		t.Fatal(err)
	}
	if err := s.Validate(); err != nil || len(s.Hash()) != 64 {
		t.Fatalf("validate %v, hash %q", err, s.Hash())
	}
}

func TestCheckLeafRejects(t *testing.T) {
	for _, bad := range []string{"", ".", "..", "a/b", `a\b`, "a:b", "a:stream", "a*", "con", "NUL", "com1.txt", "x.", "x ", " x",
		"PROGRA~1", ".git", ".GIT", "a\x00b", "a\nb", strings.Repeat("a", MaxLeaf+1), "\xff"} {
		if err := CheckLeaf(bad); !errors.Is(err, ErrInvalidSpec) {
			t.Errorf("leaf %q: %v, want ErrInvalidSpec", bad, err)
		}
	}
	for _, good := range []string{"shop-new", "shop_new", "Shop.New", "héllo", "a b", "x~y"} {
		if err := CheckLeaf(good); err != nil {
			t.Errorf("leaf %q refused: %v", good, err)
		}
	}
}

func TestInitSpecRejects(t *testing.T) {
	mut := map[string]func(*InitSpec){
		"branch":      func(s *InitSpec) { s.Branch = "dev" },
		"big readme":  func(s *InitSpec) { s.README = strings.Repeat("a", MaxContent+1) },
		"nul content": func(s *InitSpec) { s.Ignore = "a\x00" },
		"message":     func(s *InitSpec) { s.CommitMessage = "  " },
		"project":     func(s *InitSpec) { s.Project = "" },
	}
	for name, f := range mut {
		s := validInit()
		f(&s)
		if _, err := ParseInitSpec(s); err == nil {
			t.Errorf("%s was accepted", name)
		}
	}
}

func TestPublishSpecDefaultsAndRejects(t *testing.T) {
	if _, err := ParsePublishSpec(validPublish()); err != nil {
		t.Fatal(err)
	}
	mut := map[string]func(*PublishSpec){
		"no visibility": func(s *PublishSpec) { s.Visibility = "" },
		"unknown vis":   func(s *PublishSpec) { s.Visibility = "internal" },
		"owner with /":  func(s *PublishSpec) { s.Owner = "a/b" },
		"name .git":     func(s *PublishSpec) { s.Name = "x.git" },
		"host userinfo": func(s *PublishSpec) { s.HostInstance = "u@github.com" },
		"upper host":    func(s *PublishSpec) { s.HostInstance = "GitHub.com" },
		"short head":    func(s *PublishSpec) { s.ExpectedHead = "abc" },
		"other branch":  func(s *PublishSpec) { s.Branch = "dev" },
		"short marker":  func(s *PublishSpec) { s.Marker = "ab" },
		"unknown owner": func(s *PublishSpec) { s.OwnerKind = "team" },
	}
	for name, f := range mut {
		s := validPublish()
		f(&s)
		if _, err := ParsePublishSpec(s); err == nil {
			t.Errorf("%s was accepted", name)
		}
	}
	s := validPublish()
	if !strings.Contains(s.Description(), s.Marker) || strings.Contains(s.Description(), s.Owner) {
		t.Errorf("description %q must carry the marker and nothing else", s.Description())
	}
}

func TestHashesCoverEveryField(t *testing.T) {
	base := validInit().Hash()
	for name, f := range map[string]func(*InitSpec){
		"leaf": func(s *InitSpec) { s.Leaf = "other" }, "readme": func(s *InitSpec) { s.README += "x" },
		"ignore": func(s *InitSpec) { s.Ignore += "x" }, "msg": func(s *InitSpec) { s.CommitMessage += "x" },
		"repo": func(s *InitSpec) { s.Repo = "rep_2" }, "project": func(s *InitSpec) { s.Project = "prj_2" },
	} {
		s := validInit()
		f(&s)
		if s.Hash() == base {
			t.Errorf("init hash ignores %s", name)
		}
	}
	pb := validPublish().Hash()
	for name, f := range map[string]func(*PublishSpec){
		"visibility": func(s *PublishSpec) { s.Visibility = VisibilityPublic }, "owner": func(s *PublishSpec) { s.Owner = "other" },
		"name": func(s *PublishSpec) { s.Name = "other" }, "head": func(s *PublishSpec) { s.ExpectedHead = strings.Repeat("c", 40) },
		"marker": func(s *PublishSpec) { s.Marker = strings.Repeat("d", 16) }, "kind": func(s *PublishSpec) { s.OwnerKind = OwnerUser },
		"host": func(s *PublishSpec) { s.HostInstance = "git.acme.dev" },
	} {
		s := validPublish()
		f(&s)
		if s.Hash() == pb {
			t.Errorf("publish hash ignores %s", name)
		}
	}
}

func TestStageNamesAreDisjointAndParse(t *testing.T) {
	seen := map[string]bool{}
	for _, s := range InitStages() {
		seen[s.Name()] = true
	}
	for _, s := range PublishStages() {
		if seen[s.Name()] {
			t.Errorf("stage %s is both an init and a publish stage", s)
		}
	}
	for _, s := range append(stagesOf(InitStages()), stagesOf(PublishStages())...) {
		got, err := ParseStage(s.Name())
		if err != nil || got != s {
			t.Errorf("ParseStage(%q) = %v, %v", s.Name(), got, err)
		}
	}
	if _, err := ParseStage("rm_rf"); err == nil {
		t.Error("an unknown stage parsed")
	}
}

func stagesOf[T Stage](in []T) []Stage {
	out := make([]Stage, len(in))
	for i, s := range in {
		out[i] = s
	}
	return out
}

// The foundation Step is a documented stub: nothing is reserved, written or
// published by it.
func TestStepIsInert(t *testing.T) {
	op := Operation{ID: "op_1", Project: "prj_1", Repo: "rep_1"}
	for _, in := range []Input{
		Reserve{Spec: validInit()}, ProposalCreated{ID: "p", Hash: "h"}, Confirmed{Attempt: 1}, StageSucceeded{Attempt: 1},
		StageFailed{Attempt: 1}, PublishConfirmed{Attempt: 1}, Recovered{}, Reconciled{Attempt: 1}, RetryConfirmed{Attempt: 1},
		Reject{}, Untracked{},
	} {
		got, cmds := Step(op, in)
		if got.State != nil || got.Version != op.Version || len(cmds) != 0 {
			t.Errorf("Step(%T) = %+v, %v", in, got, cmds)
		}
	}
}
