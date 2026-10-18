package main

import (
	"bytes"
	"context"
	"os"
	"path/filepath"
	"regexp"
	"strings"
	"testing"

	"github.com/brendatamaar/yggdrasil/internal/safety"
)

// gitRepo makes an empty folder that looks like a repo to seed: it only checks
// for the .git entry.
func gitRepo(t *testing.T, parent, name string) string {
	t.Helper()
	dir := filepath.Join(parent, name)
	if err := os.MkdirAll(filepath.Join(dir, ".git"), 0o700); err != nil {
		t.Fatal(err)
	}
	return dir
}

var idPattern = regexp.MustCompile(`(prj|rep)_[0-9a-f]+`)

func TestSeedTracksEveryRepoUnderAFolderAndPrintsTheIDs(t *testing.T) {
	cfg := config{dataDir: t.TempDir()}
	root := filepath.Join(t.TempDir(), "acme-shop")
	gitRepo(t, root, "shop-api")
	gitRepo(t, root, "shop-web")
	var out bytes.Buffer
	if err := seed(context.Background(), &out, cfg, root, ""); err != nil {
		t.Fatal(err)
	}
	ids := idPattern.FindAllString(out.String(), -1)
	if len(ids) != 3 || !strings.HasPrefix(ids[0], "prj_") || !strings.HasPrefix(ids[1], "rep_") || !strings.HasPrefix(ids[2], "rep_") {
		t.Fatalf("output:\n%s", out.String())
	}
	for _, want := range []string{"acme-shop", "shop-api", "shop-web", "Project ID"} {
		if !strings.Contains(out.String(), want) {
			t.Errorf("output lacks %q:\n%s", want, out.String())
		}
	}

	// Running it again finds the same project and repos, and writes nothing.
	var again bytes.Buffer
	if err := seed(context.Background(), &again, cfg, root, ""); err != nil {
		t.Fatal(err)
	}
	if got := idPattern.FindAllString(again.String(), -1); strings.Join(got, ",") != strings.Join(ids, ",") {
		t.Errorf("second run gave %v, want %v", got, ids)
	}

	// Tracking is audited once for what was created, and touched no file.
	d, store, err := openStore(context.Background(), cfg, safety.NewRedactor())
	if err != nil {
		t.Fatal(err)
	}
	defer d.Close()
	for subject, action := range map[string]string{
		"project:" + ids[0]: "project.track", "repo:" + ids[1]: "repo.track", "repo:" + ids[2]: "repo.track",
	} {
		rows, err := store.ListAudit(context.Background(), subject, 10)
		if err != nil || len(rows) != 1 || rows[0].Action != action || rows[0].Actor != "user" {
			t.Errorf("audit for %s = %+v, %v", subject, rows, err)
		}
	}
	for _, name := range []string{"shop-api", "shop-web"} {
		entries, err := os.ReadDir(filepath.Join(root, name))
		if err != nil || len(entries) != 1 || entries[0].Name() != ".git" {
			t.Errorf("seed changed %s: %v, %v", name, entries, err)
		}
	}
}

func TestSeedATopLevelRepoAndAnExplicitName(t *testing.T) {
	cfg := config{dataDir: t.TempDir()}
	repo := gitRepo(t, t.TempDir(), "shop-api")
	var out bytes.Buffer
	if err := seed(context.Background(), &out, cfg, repo, "Acme shop"); err != nil {
		t.Fatal(err)
	}
	ids := idPattern.FindAllString(out.String(), -1)
	if len(ids) != 2 || !strings.Contains(out.String(), "Acme shop") || !strings.Contains(out.String(), "shop-api") {
		t.Errorf("output:\n%s", out.String())
	}
}

func TestSeedRefusesWhatHasNoRepo(t *testing.T) {
	cfg := config{dataDir: t.TempDir()}
	plain := t.TempDir()
	file := filepath.Join(plain, "afile")
	if err := os.WriteFile(file, nil, 0o600); err != nil {
		t.Fatal(err)
	}
	for name, path := range map[string]string{
		"no repos inside": plain,
		"a file":          file,
		"missing folder":  filepath.Join(plain, "nope"),
		"WSL path":        `\\wsl$\Ubuntu\home\me\repo`,
	} {
		if err := seed(context.Background(), &bytes.Buffer{}, cfg, path, ""); err == nil {
			t.Errorf("%s: seed succeeded", name)
		}
	}
	// Nothing was written for a refused folder.
	d, store, err := openStore(context.Background(), cfg, safety.NewRedactor())
	if err != nil {
		t.Fatal(err)
	}
	defer d.Close()
	if ps, _ := store.ListProjects(context.Background()); len(ps) != 0 {
		t.Errorf("a refused folder created projects: %+v", ps)
	}
}

func TestSeedRefusesAProjectNameUsedAtAnotherFolder(t *testing.T) {
	cfg := config{dataDir: t.TempDir()}
	root := t.TempDir()
	first := gitRepo(t, filepath.Join(root, "one"), "shop-api")
	if err := seed(context.Background(), &bytes.Buffer{}, cfg, first, "same"); err != nil {
		t.Fatal(err)
	}
	second := gitRepo(t, filepath.Join(root, "two"), "shop-api")
	err := seed(context.Background(), &bytes.Buffer{}, cfg, second, "same")
	if err == nil || !strings.Contains(err.Error(), "already tracked") {
		t.Errorf("second folder with the same name: %v", err)
	}
	// The same folder again, under another name, is the same project.
	err = seed(context.Background(), &bytes.Buffer{}, cfg, first, "other")
	if err != nil {
		t.Errorf("the same folder under another name is the same project: %v", err)
	}
}
