package mcp

import (
	"math/rand"
	"regexp"
	"strings"
	"testing"

	"github.com/brendatamaar/yggdrasil/internal/safety"
)

const (
	testSecret  = "ygg-test-secret-token-0123456789abcdef"
	testDataDir = `C:\Users\dev\AppData\Roaming\ygg-data`
	testRepoWin = `D:\work\shop api\shop-api`
	testRepoNix = "/srv/odd/repos/shop-web"
)

var (
	leakWin  = regexp.MustCompile(`(?i)[a-z]:\\|\b[a-z]:/|\\\\[A-Za-z0-9_.$-]+\\`)
	leakUnix = regexp.MustCompile(`(^|[\s"'(=:])/[^\s/"')]+/`)
)

func newSanitizer() *Server {
	r := safety.NewRedactor()
	r.Add(testSecret)
	return &Server{cfg: Config{Redactor: r, DataDir: testDataDir}}
}

func leaks(out string) string {
	switch {
	case strings.Contains(out, testSecret):
		return "secret"
	case leakWin.MatchString(out):
		return "windows path"
	case leakUnix.MatchString(out):
		return "unix path"
	case strings.Contains(strings.ToLower(out), "ygg-data"), strings.Contains(out, "shop api"), strings.Contains(out, "odd/repos"):
		return "known folder"
	}
	return ""
}

var sanitizeFragments = []string{
	"error: ", " at ", "open ", "(", ")", `"`, "'", "=", ": ", "\n", " ", "src/a.go", "a/b/c.go", "diff a/x b/x",
	testSecret, testDataDir, testDataDir + `\ygg.db`, strings.ReplaceAll(testDataDir, `\`, "/"),
	strings.ToUpper(testDataDir), testRepoWin + `\src\a.go`, strings.ReplaceAll(testRepoWin, `\`, "/"),
	testRepoNix, testRepoNix + "/pom.xml", "/opt/tool/bin/x", "/weird/dir/file", "/var/log/x.log", `\\fileserver\share\x`,
	`C:\Windows\System32`, "E:/tmp/x", `\\?\C:\long\path`, "/", "//", "no path here",
}

// M7: whatever an error or a tool result is made of, no absolute path (Windows
// or Unix), no data or repo folder and no registered secret comes out.
func TestPropSanitizeLeavesNoPathOrSecret(t *testing.T) {
	srv := newSanitizer()
	extra := []string{testRepoWin, testRepoNix}
	for seed := int64(1); seed <= 3000; seed++ {
		r := rand.New(rand.NewSource(seed))
		var b strings.Builder
		for i, n := 0, 1+r.Intn(8); i < n; i++ {
			b.WriteString(sanitizeFragments[r.Intn(len(sanitizeFragments))])
		}
		in := b.String()
		out := srv.sanitize(in, extra...)
		if why := leaks(out); why != "" {
			t.Fatalf("seed %d: %s left in %q (from %q)", seed, why, out, in)
		}
	}
}

func FuzzSanitizeLeavesNoSecretOrKnownFolder(f *testing.F) {
	for _, s := range sanitizeFragments {
		f.Add(s)
	}
	srv := newSanitizer()
	f.Fuzz(func(t *testing.T, in string) {
		out := srv.sanitize(in, testRepoWin, testRepoNix)
		for _, bad := range []string{testSecret, testDataDir, testRepoWin, testRepoNix} {
			if strings.Contains(out, bad) {
				t.Fatalf("%q left in %q (from %q)", bad, out, in)
			}
		}
	})
}

// A path under the data folder or a repo is scrubbed even when it is not under
// a system folder, and in either slash style.
func TestSanitizeScrubsKnownFolders(t *testing.T) {
	srv := newSanitizer()
	for _, in := range []string{
		`open ` + testDataDir + `\ygg.db: locked`,
		`open ` + strings.ReplaceAll(testDataDir, `\`, "/") + `/ygg.db: locked`,
		`read ` + testRepoNix + `/pom.xml failed`,
		`read ` + strings.ToLower(testRepoWin) + `\x.go failed`,
		`see /weird/dir/file now`,
	} {
		out := srv.sanitize(in, testRepoWin, testRepoNix)
		if leaks(out) != "" || !strings.Contains(out, "<path>") {
			t.Errorf("%q -> %q", in, out)
		}
	}
}
