package main

import (
	"context"
	"strings"
	"testing"
	"testing/fstest"

	"github.com/brendatamaar/yggdrasil/internal/core"
	"github.com/brendatamaar/yggdrasil/internal/core/guard"
	"github.com/brendatamaar/yggdrasil/internal/db"
	"github.com/brendatamaar/yggdrasil/internal/db/dbtest"
)

func ruleStrings(rules []guard.CommandRule) []string {
	var out []string
	for _, r := range rules {
		if err := r.Validate(); err != nil {
			panic(err)
		}
		if r.Tail != guard.TailNone {
			panic("tail must be none")
		}
		out = append(out, strings.Join(append([]string{r.Program}, r.Args...), " "))
	}
	return out
}

func hasAll(t *testing.T, got []string, want ...string) {
	t.Helper()
	have := map[string]bool{}
	for _, g := range got {
		have[g] = true
	}
	for _, w := range want {
		if !have[w] {
			t.Errorf("rules %q lack %q", got, w)
		}
	}
}

func TestDefaultRulesFromToolchains(t *testing.T) {
	node := fstest.MapFS{
		"package.json":   {Data: []byte(`{"scripts":{"test":"vitest","build":"vite build","lint":"eslint ."}}`)},
		"pnpm-lock.yaml": {Data: []byte("lockfileVersion: 9\n")},
	}
	got := ruleStrings(defaultRules(node, nil))
	hasAll(t, got, "pnpm run test", "pnpm run build", "pnpm run lint")
	for _, g := range got {
		if strings.Contains(g, "install") || strings.Contains(g, "dev") {
			t.Errorf("rule %q must not be a default", g)
		}
	}

	maven := fstest.MapFS{
		"pom.xml": {Data: []byte(`<project><modelVersion>4.0.0</modelVersion><groupId>com.acme</groupId><artifactId>shop-api</artifactId><version>1</version></project>`)},
		"mvnw":    {Data: []byte("#!/bin/sh\n")},
	}
	hasAll(t, ruleStrings(defaultRules(maven, nil)), "mvn test-compile", "mvn test")

	gradle := fstest.MapFS{
		"build.gradle": {Data: []byte("plugins { id 'java' }\n")},
		"gradlew":      {Data: []byte("#!/bin/sh\n")},
	}
	got = ruleStrings(defaultRules(gradle, nil))
	hasAll(t, got, "gradle test", "gradle build")
	for _, g := range got {
		if strings.HasPrefix(g, "gradlew") {
			t.Errorf("wrapper rule %q", g)
		}
	}

	golang := fstest.MapFS{"go.mod": {Data: []byte("module acme/shop\n")}}
	hasAll(t, ruleStrings(defaultRules(golang, nil)), "go vet ./...", "go build ./...", "go test ./...")

	if rules := defaultRules(fstest.MapFS{"README.md": {Data: []byte("hi")}}, nil); len(rules) != 0 {
		t.Errorf("an unknown repo got rules %v", rules)
	}
}

func TestRuleOfSkipsRefusedRules(t *testing.T) {
	// bash runs anything: Validate refuses it and defaultRules leaves it out.
	r, ok := ruleOf([]string{"bash", "-c", "x"})
	if !ok || r.Validate() == nil {
		t.Fatalf("bash rule should be refused, got %v ok=%v", r, ok)
	}
	if _, ok := ruleOf(nil); ok {
		t.Fatal("empty argv made a rule")
	}
	r, _ = ruleOf([]string{"mvnw.cmd", "-B", "test"})
	if r.Program != "mvn" || len(r.Args) != 1 || r.Args[0] != "test" {
		t.Fatalf("wrapper mapping = %+v", r)
	}
}

func TestAllowlistOverrideFromSettings(t *testing.T) {
	ctx := context.Background()
	d, err := db.Open(ctx, t.TempDir()+"/y.db")
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = d.Close() })
	store := db.NewStore(d, nil)
	prj := dbtest.Project(t, store, core.Project{Name: "acme-shop", Path: t.TempDir()})
	repo := dbtest.Repo(t, store, core.Repo{ProjectID: prj.ID, Name: "shop-api", Path: t.TempDir(), DefaultBranch: "main"})
	al := newAllowlist(store, nil)

	// No override and nothing detected: no rules.
	rules, err := al(ctx, repo)
	if err != nil || len(rules) != 0 {
		t.Fatalf("defaults = %v, %v", rules, err)
	}

	version := db.SettingNew
	set := func(v string) {
		t.Helper()
		if err := store.Commit(ctx, func(tx *db.Tx) error {
			nv, err := tx.SetSetting(ctx, allowlistKey(repo.ID), v, version)
			version = nv
			return err
		}); err != nil {
			t.Fatal(err)
		}
	}
	set(`[{"program":"go","args":["test","./..."],"tail":"none"}]`)
	rules, err = al(ctx, repo)
	if err != nil || len(rules) != 1 || rules[0].Program != "go" {
		t.Fatalf("override = %v, %v", rules, err)
	}

	// A stored override that is not valid allows nothing and says why.
	set(`[{"program":"bash","args":[],"tail":"none"}]`)
	if rules, err = al(ctx, repo); err == nil || rules != nil {
		t.Fatalf("invalid override = %v, %v", rules, err)
	}
}
