package maven

import (
	"fmt"
	"io/fs"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"testing/fstest"

	"github.com/brendatamaar/yggdrasil/internal/builds"
)

func TestParsePOMLatin1AndUnsupportedEncoding(t *testing.T) {
	latin := "<?xml version=\"1.0\" encoding=\"ISO-8859-1\"?><project><artifactId>caf\xe9</artifactId></project>"
	m, err := ParsePOM(strings.NewReader(latin))
	if err != nil || m.Coord.Artifact != "café" {
		t.Fatalf("latin-1 pom = %+v, %v", m, err)
	}
	_, err = ParsePOM(strings.NewReader(`<?xml version="1.0" encoding="Shift_JIS"?><project><artifactId>a</artifactId></project>`))
	if err == nil || !strings.Contains(err.Error(), "Save the file as UTF-8") {
		t.Fatalf("shift_jis error = %v", err)
	}
}

func TestModulesTruncationIsReported(t *testing.T) {
	var mods strings.Builder
	repo := fstest.MapFS{}
	for i := 0; i < maxReactor+5; i++ {
		fmt.Fprintf(&mods, "<module>m%d</module>", i)
		repo[fmt.Sprintf("m%d/pom.xml", i)] = &fstest.MapFile{Data: []byte(`<project><artifactId>x</artifactId></project>`)}
	}
	repo["pom.xml"] = &fstest.MapFile{Data: []byte(`<project><artifactId>root</artifactId><modules>` + mods.String() + `</modules></project>`)}
	d, _ := New().Detect(repo)
	if _, err := New().Modules(repo, d); err == nil || !strings.Contains(err.Error(), "more than") {
		t.Fatalf("a truncated reactor was not reported: %v", err)
	}
}

func TestModulesRejectSymlinkedModuleDir(t *testing.T) {
	root := t.TempDir()
	outside := t.TempDir()
	if err := os.WriteFile(filepath.Join(outside, "pom.xml"), []byte(`<project><artifactId>evil</artifactId></project>`), 0o600); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(root, "pom.xml"), []byte(`<project><artifactId>r</artifactId><modules><module>link</module></modules></project>`), 0o600); err != nil {
		t.Fatal(err)
	}
	if err := os.Symlink(outside, filepath.Join(root, "link")); err != nil {
		t.Skipf("cannot create a symlink here: %v", err)
	}
	repo := os.DirFS(root)
	if _, ok := repo.(fs.ReadLinkFS); !ok {
		t.Skip("this file system cannot report links")
	}
	d := builds.Detection{Toolchain: "maven", Dir: "."}
	if _, err := New().Modules(repo, d); err == nil || !strings.Contains(err.Error(), "symbolic link") {
		t.Fatalf("a symlinked module dir was followed: %v", err)
	}
}
