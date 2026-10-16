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

func TestSeedTracksARepoAndPrintsTheIDs(t *testing.T) {
	cfg := config{dataDir: t.TempDir()}
	repo := gitRepo(t, filepath.Join(t.TempDir(), "acme-shop"), "shop-api")
	var out bytes.Buffer
	if err := seed(context.Background(), &out, cfg, repo, ""); err != nil {
		t.Fatal(err)
	}
	ids := idPattern.FindAllString(out.String(), -1)
	if len(ids) != 2 || !strings.HasPrefix(ids[0], "prj_") || !strings.HasPrefix(ids[1], "rep_") {
		t.Fatalf("output:\n%s", out.String())
	}
	if !strings.Contains(out.String(), "acme-shop") || !strings.Contains(out.String(), "shop-api") || !strings.Contains(out.String(), "Project ID") {
		t.Errorf("output lacks names or the instruction:\n%s", out.String())
	}

	// Running it again finds the same project and repo.
	var again bytes.Buffer
	if err := seed(context.Background(), &again, cfg, repo, ""); err != nil {
		t.Fatal(err)
	}
	if got := idPattern.FindAllString(again.String(), -1); strings.Join(got, ",") != strings.Join(ids, ",") {
		t.Errorf("second run gave %v, want %v", got, ids)
	}

	// Tracking is audited (once, for what was created) and touched no file.
	d, store, aud, err := openStore(context.Background(), cfg, safety.NewRedactor())
	if err != nil {
		t.Fatal(err)
	}
	defer d.Close()
	_ = aud
	for subject, action := range map[string]string{"project:" + ids[0]: "project.track", "repo:" + ids[1]: "repo.track"} {
		rows, err := store.ListAudit(context.Background(), subject, 10)
		if err != nil || len(rows) != 1 || rows[0].Action != action || rows[0].Actor != "user" {
			t.Errorf("audit for %s = %+v, %v", subject, rows, err)
		}
	}
	entries, err := os.ReadDir(repo)
	if err != nil || len(entries) != 1 || entries[0].Name() != ".git" {
		t.Errorf("seed changed the repo folder: %v, %v", entries, err)
	}
}

func TestSeedNamesTheProjectAndKeepsReposApart(t *testing.T) {
	cfg := config{dataDir: t.TempDir()}
	root := filepath.Join(t.TempDir(), "acme-shop")
	var a, b bytes.Buffer
	if err := seed(context.Background(), &a, cfg, gitRepo(t, root, "shop-api"), "Acme shop"); err != nil {
		t.Fatal(err)
	}
	if err := seed(context.Background(), &b, cfg, gitRepo(t, root, "shop-web"), "Acme shop"); err != nil {
		t.Fatal(err)
	}
	ia, ib := idPattern.FindAllString(a.String(), -1), idPattern.FindAllString(b.String(), -1)
	if ia[0] != ib[0] || ia[1] == ib[1] || !strings.Contains(a.String(), "Acme shop") {
		t.Errorf("one project with two repos expected: %v and %v", ia, ib)
	}
}

func TestSeedRefusesWhatIsNotARepo(t *testing.T) {
	cfg := config{dataDir: t.TempDir()}
	plain := t.TempDir()
	file := filepath.Join(plain, "afile")
	if err := os.WriteFile(file, nil, 0o600); err != nil {
		t.Fatal(err)
	}
	for name, path := range map[string]string{
		"no .git":        plain,
		"a file":         file,
		"missing folder": filepath.Join(plain, "nope"),
		"WSL path":       `\\wsl$\Ubuntu\home\me\repo`,
	} {
		err := seed(context.Background(), &bytes.Buffer{}, cfg, path, "")
		if err == nil {
			t.Errorf("%s: seed succeeded", name)
		}
	}
	// Nothing was written for a refused folder.
	d, store, _, err := openStore(context.Background(), cfg, safety.NewRedactor())
	if err != nil {
		t.Fatal(err)
	}
	defer d.Close()
	if ps, _ := store.ListProjects(context.Background()); len(ps) != 0 {
		t.Errorf("a refused folder created projects: %+v", ps)
	}
}

func TestSeedRefusesTwoReposWithOneNameAtDifferentPaths(t *testing.T) {
	cfg := config{dataDir: t.TempDir()}
	root := t.TempDir()
	first := gitRepo(t, filepath.Join(root, "one"), "shop-api")
	if err := seed(context.Background(), &bytes.Buffer{}, cfg, first, "same"); err != nil {
		t.Fatal(err)
	}
	second := gitRepo(t, filepath.Join(root, "two"), "shop-api")
	err := seed(context.Background(), &bytes.Buffer{}, cfg, second, "same")
	if err == nil || !strings.Contains(err.Error(), "already tracked") {
		t.Errorf("second repo with the same name: %v", err)
	}
}
