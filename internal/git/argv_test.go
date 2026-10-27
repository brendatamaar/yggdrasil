package git

import (
	"math/rand"
	"slices"
	"strings"
	"testing"
)

// forbiddenFlags and forbiddenSubcommands are the destructive forms of
// docs/design/proposals.md P10 (invariant 1).
var forbiddenFlags = []string{"--force", "-f", "--force-with-lease", "--force-if-includes", "--mirror", "--delete", "-d", "-D",
	"--prune", "--hard", "-A", "--all", "-a", "--update", "-u", "--amend", "--no-edit"}

var forbiddenSubcommands = []string{"reset", "clean", "rm", "branch", "checkout", "restore", "gc", "prune", "filter-branch", "update-ref"}

var allowedSubcommands = []string{"add", "commit", "push"}

// checkDestructiveFree reports why argv could destroy something, or "".
func checkDestructiveFree(argv []string) string {
	// Options after "--" are paths and refs, not flags.
	head := argv
	if i := slices.Index(argv, "--"); i >= 0 {
		head = argv[:i]
	}
	sub := ""
	for i := 0; i < len(head); i++ {
		a := head[i]
		switch {
		case a == "-c" || a == "-o":
			i++ // the value of -c / -o is not a flag
			continue
		case slices.Contains(forbiddenFlags, a):
			return "forbidden flag " + a
		case !strings.HasPrefix(a, "-") && sub == "":
			sub = a
		}
	}
	if slices.Contains(forbiddenSubcommands, sub) || !slices.Contains(allowedSubcommands, sub) {
		return "subcommand " + sub
	}
	for _, a := range argv {
		if a == "" || (sub == "push" && (strings.HasPrefix(a, "+") || strings.HasPrefix(a, ":"))) {
			return "refspec or argument " + a
		}
	}
	if sub == "push" {
		refspecs := 0
		for _, a := range argv[len(head):] {
			src, dst, ok := strings.Cut(a, ":")
			if ok {
				refspecs++
				if src == "" || src != dst || !strings.HasPrefix(src, "refs/heads/") {
					return "refspec " + a
				}
			}
		}
		if refspecs != 1 {
			return "push has not exactly one refspec"
		}
	}
	return ""
}

func randName(r *rand.Rand, alphabet string, n int) string {
	b := make([]byte, 1+r.Intn(n))
	for i := range b {
		b[i] = alphabet[r.Intn(len(alphabet))]
	}
	return string(b)
}

func TestPropGitArgvHasNoDestructiveForm(t *testing.T) {
	const hooks = "/tmp/ygg-nohooks-1"
	valid := 0
	for seed := int64(1); seed <= 2000; seed++ {
		r := rand.New(rand.NewSource(seed))
		// Hostile names included: the argv builders are fed what survives the
		// last-line checks, and the checks themselves are tested for refusal.
		branch := randName(r, "abcxyz/-_.+:~^*? \\@{}", 12)
		remote := randName(r, "origin-_.+:", 8)
		paths := []string{randName(r, "ab/._-:*", 8), randName(r, "xy/-+", 8)}

		req := PushRequest{Remote: remote, Branch: branch, ExpectHead: "abc"}
		if r.Intn(2) == 0 {
			req.MR = &MROptions{Target: randName(r, "main-dev", 6), Title: randName(r, "t ", 8), Description: randName(r, "d ", 8)}
		}
		if checkBranchName(branch) == nil && checkRemoteName(remote) == nil {
			valid++
			if why := checkDestructiveFree(PushArgv(req, hooks)); why != "" {
				t.Fatalf("seed %d: push argv %v: %s", seed, PushArgv(req, hooks), why)
			}
		}
		if checkPathspecs(paths) == nil {
			for _, argv := range [][]string{AddArgv(paths), CommitArgv(paths, hooks)} {
				if why := checkDestructiveFree(argv); why != "" {
					t.Fatalf("seed %d: argv %v: %s", seed, argv, why)
				}
			}
		}
	}
	if valid < 50 {
		t.Fatalf("generator produced only %d valid push requests; the test is not testing", valid)
	}
}

// The checker itself must see the forms it exists to catch.
func TestDestructiveCheckerCatchesTheForms(t *testing.T) {
	bad := [][]string{
		{"push", "--force", "--", "origin", "refs/heads/a:refs/heads/a"},
		{"push", "-f", "--", "origin", "refs/heads/a:refs/heads/a"},
		{"push", "--", "origin", "+refs/heads/a:refs/heads/a"},
		{"push", "--", "origin", ":refs/heads/a"},
		{"push", "--delete", "--", "origin", "a"},
		{"push", "--", "origin", "refs/heads/a:refs/heads/b"},
		{"reset", "--hard"},
		{"clean", "-fd"},
		{"branch", "-D", "x"},
		{"rm", "--", "a"},
		{"add", "-A"},
		{"add", "--all"},
	}
	for _, argv := range bad {
		if checkDestructiveFree(argv) == "" {
			t.Errorf("checker accepted %v", argv)
		}
	}
}

func TestBranchAndPathChecksRefuseDestructiveInput(t *testing.T) {
	for _, b := range []string{"", "-f", "+main", ":main", "a:b", "a b", "a..b", "x/", "a.lock", "a\nb", "a*", "refs/heads/x:y"} {
		if checkBranchName(b) == nil {
			t.Errorf("branch %q accepted", b)
		}
	}
	for _, p := range []string{"", ".", "-A", "/etc/x", `\x`, "../x", "a/../b", ".git/config", ":(glob)*", "a\x00b"} {
		if checkPathspecs([]string{p}) == nil {
			t.Errorf("path %q accepted", p)
		}
	}
	if checkPathspecs(nil) == nil {
		t.Error("an empty path list was accepted: that would mean all")
	}
}

func TestRemoteURLChecks(t *testing.T) {
	good := []string{"https://git.example.test/a/b.git", "git@git.example.test:a/b.git", "ssh://git@git.example.test/a/b.git", t.TempDir()}
	for _, u := range good {
		if err := checkRemoteURL(u); err != nil {
			t.Errorf("%q refused: %v", u, err)
		}
	}
	bad := []string{"", "https://user:pw@h/x", "https://tok@h/x", "ssh://u:pw@h/x", "ext::sh -c boom", "file:///x", "-oProxyCommand=x",
		"https://h/x?token=1", "relative/path", "http://h/x#frag"}
	for _, u := range bad {
		if err := checkRemoteURL(u); err == nil {
			t.Errorf("%q accepted", u)
		}
	}
}
