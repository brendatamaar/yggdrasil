package repos_test

import (
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/brendatamaar/yggdrasil/internal/apperr"
	corerepos "github.com/brendatamaar/yggdrasil/internal/core/repos"
	"github.com/brendatamaar/yggdrasil/internal/platform"
	"github.com/brendatamaar/yggdrasil/internal/repos"
)

// On Windows one folder has many spellings; ParseFolder gives them one form.
func TestParseFolderCaseVariantsAreOneFolder(t *testing.T) {
	if !platform.PathsCaseInsensitive {
		t.Skip("this file system is case-sensitive")
	}
	dir := filepath.Join(t.TempDir(), "Shop Api")
	if err := os.Mkdir(dir, 0o750); err != nil {
		t.Fatal(err)
	}
	want, err := repos.ParseFolder(dir)
	if err != nil {
		t.Fatal(err)
	}
	for _, alt := range []string{strings.ToUpper(dir), strings.ToLower(dir), strings.ToLower(dir[:1]) + dir[1:]} {
		got, err := repos.ParseFolder(alt)
		if err != nil || got != want {
			t.Errorf("ParseFolder(%q) = %q, %v; want %q", alt, got, err, want)
		}
	}
}

func TestParsePicksDedupesCaseVariants(t *testing.T) {
	_, err := repos.ParsePicks("/work/shop", []corerepos.Pick{{RelPath: "Api"}, {RelPath: "api"}})
	if platform.PathsCaseInsensitive {
		wantInvalid(t, "case variants of one folder", err)
	} else if err != nil {
		t.Errorf("different folders on a case-sensitive disk: %v", err)
	}
}

// A project folder that is itself a link is refused.
func TestParseFolderRefusesALinkRoot(t *testing.T) {
	base := t.TempDir()
	target := filepath.Join(base, "real")
	if err := os.Mkdir(target, 0o750); err != nil {
		t.Fatal(err)
	}
	link := filepath.Join(base, "lnk")
	if !makeLink(t, link, target) {
		t.Skip("can't make a link here")
	}
	_, err := repos.ParseFolder(link)
	wantInvalid(t, "a link as the project folder", err)
	if m, _ := apperr.MessageOf(err); !strings.Contains(m, "link") {
		t.Errorf("message %q doesn't say it is a link", m)
	}
}
