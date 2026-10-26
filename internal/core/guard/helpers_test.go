package guard

import (
	"fmt"
	"math/rand"
	"strings"
	"testing"

	"github.com/brendatamaar/yggdrasil/internal/core"
)

const propRuns = 400

// forSeeds runs f with a deterministic generator per seed and names the seed
// of a failing run.
func forSeeds(t *testing.T, f func(t *testing.T, r *rand.Rand)) {
	t.Helper()
	for seed := int64(1); seed <= propRuns; seed++ {
		ok := t.Run(fmt.Sprintf("seed%d", seed), func(t *testing.T) { f(t, rand.New(rand.NewSource(seed))) })
		if !ok {
			return
		}
	}
}

func pick[T any](r *rand.Rand, xs ...T) T { return xs[r.Intn(len(xs))] }

var allModes = []core.TaskMode{core.ModeAsk, core.ModeCode, core.ModeCurrentBranch, core.ModeProject}

// path forms the generators draw from: roots in several shapes, siblings by
// prefix, extra drives, escapes, protected names, 8.3 look-alikes.
var genRoots = []string{`C:\work\repo`, `C:\work\extra`, `/srv/repo`, `D:\code\app`}

func genPathString(r *rand.Rand) string {
	root := pick(r, genRoots...)
	sep := pick(r, `\`, `/`)
	var s string
	switch r.Intn(14) {
	case 0:
		s = root
	case 1:
		s = root + sep + "src" + sep + "main.go"
	case 2:
		s = root + "-evil" + sep + "x.go" // prefix sibling
	case 3:
		s = root + "2"
	case 4:
		s = strings.ToUpper(root) + sep + "a.txt"
	case 5:
		s = strings.ToLower(root) + sep + "A.TXT"
	case 6:
		s = root + sep + ".." + sep + "x"
	case 7:
		s = root + sep + ".git" + sep + "config"
	case 8:
		s = root + sep + ".claude" + sep + "settings.json"
	case 9:
		s = root + sep + pick(r, ".mcp.json", "CLAUDE.md", "AGENTS.md", "opencode.json", "GEMINI.md", ".opencode", ".codex", "CLAUDE.local.md", ".gitmodules", ".husky")
	case 10:
		s = `C:\Users\someone\secrets.txt`
	case 11:
		s = `C:\work\REPO~1\x.go` // 8.3 look-alike
	case 12:
		s = root + sep + "f.txt:stream"
	default:
		s = pick(r, "", "relative"+sep+"x", "/", `\\host\share\x`, `\\?\C:\work\repo\x`, root+sep, root+sep+".", root+sep+"a  ")
	}
	return s
}

func genPolicy(r *rand.Rand) Policy {
	mode := pick(r, allModes...)
	p := Policy{Mode: mode, FoldCase: r.Intn(2) == 0, Enforcement: pick(r, EnforcementHook, EnforcementLimited)}
	p.ReadRoots = []string{pick(r, genRoots...)}
	if r.Intn(3) == 0 {
		p.ReadRoots = append(p.ReadRoots, pick(r, genRoots...))
	}
	if mode != core.ModeAsk {
		p.WriteRoots = []string{pick(r, genRoots...)}
	}
	for n := r.Intn(4); n > 0; n-- {
		p.Allow = append(p.Allow, CommandRule{
			Program: pick(r, "go", "npm", "mvnw", "git"),
			Args:    pick(r, nil, []string{"test"}, []string{"vet", "./..."}, []string{"run", "test"}),
			Tail:    pick(r, TailNone, TailPaths),
		})
	}
	return p
}

func genArgv(r *rand.Rand) []string {
	prog := pick(r, "go", "go.exe", "GO", "go.bat", "go.com", "npm.cmd", "npm", "mvnw.cmd", "git", "evil", `.\go`, `/usr/bin/go`, "")
	argv := []string{prog}
	argv = append(argv, pick(r, nil, []string{"test"}, []string{"vet", "./..."}, []string{"run", "test"}, []string{"push", "--force"})...)
	for n := r.Intn(3); n > 0; n-- {
		argv = append(argv, pick(r, "pkg", "./pkg", "-run", "--config=x", "@r.txt", "./a,../..", `C:\work\repo\x`, "C:/work/repo/x", `C:\elsewhere\x`, "../x", "",
			`\-exec=evil.sh`, `$'\055x'`, `{-x,y}`, `"-"x`, `''-x`, "-", "$HOME", "*", "~", "é", "a b", "a\tb", "a;b", "x\\y"))
	}
	return argv
}

// asciiLowerRef lowers A-Z only; the reference for ASCII folding.
func asciiLowerRef(s string) string {
	b := []byte(s)
	for i, c := range b {
		if c >= 'A' && c <= 'Z' {
			b[i] = c + 32
		}
	}
	return string(b)
}

func genProgram(r *rand.Rand) *PathRef {
	switch r.Intn(5) {
	case 0:
		return nil
	case 1:
		return &PathRef{Canon: pick(r, genRoots...) + `\bin\go.exe`, OK: true}
	case 2:
		return &PathRef{Canon: `C:\tools\go.exe`, OK: false}
	default:
		return &PathRef{Canon: `C:\tools\go\bin\go.exe`, OK: true}
	}
}

func genCall(r *rand.Rand) Call {
	c := Call{Tool: pick(r, ToolRead, ToolSearch, ToolWrite, ToolExec, ToolSubagent, ToolOther, Tool("bogus"))}
	for n := r.Intn(3); n > 0; n-- {
		c.Paths = append(c.Paths, PathRef{Canon: genPathString(r), OK: r.Intn(8) != 0, Raw: "raw" + genPathString(r)})
	}
	if c.Tool == ToolSearch || r.Intn(6) == 0 {
		c.Globs = append(c.Globs, pick(r, "**/*.go", "src/*.{ts,tsx}", `C:\work\repo\**\*.go`, `/etc/*`, "../*", "{/etc,x}", "~/x", "*"))
	}
	if c.Tool == ToolExec {
		c.Argv = genArgv(r)
		c.ParseErr = r.Intn(6) == 0
		c.Program = genProgram(r)
	}
	if c.Tool == ToolSubagent {
		c.Isolation = pick(r, "", "", "worktree")
	}
	return c
}

// refInside is an independent, deliberately simple containment oracle used by
// the property tests: separators unified, optional folding, whole-part prefix.
func refInside(root, p string, fold bool) bool {
	n := func(s string) string {
		s = strings.ReplaceAll(s, `\`, "/")
		if fold {
			s = asciiLowerRef(s)
		}
		lead := ""
		for i := 0; i < len(s) && s[i] == '/' && i < 2; i++ {
			lead += "/"
		}
		var segs []string
		for _, seg := range strings.Split(s, "/") {
			if seg != "" && seg != "." {
				segs = append(segs, seg)
			}
		}
		return lead + strings.Join(segs, "/")
	}
	root, p = n(root), n(p)
	return p == root || strings.HasPrefix(p, strings.TrimSuffix(root, "/")+"/")
}

func anyInside(roots []string, p string, fold bool) bool {
	for _, r := range roots {
		if refInside(r, p, fold) {
			return true
		}
	}
	return false
}

func hasDotDot(p string) bool {
	for _, s := range strings.FieldsFunc(p, func(c rune) bool { return c == '/' || c == '\\' }) {
		if s == ".." {
			return true
		}
	}
	return false
}
