package guard

import (
	"testing"

	"github.com/brendatamaar/yggdrasil/internal/core"
)

func FuzzParseCommandLine(f *testing.F) {
	for _, s := range []string{"go test ./...", `a "b c" 'd"e'`, `x ""`, "a && b", `"unclosed`, "", "100%", "$(x)", "a\tb", `it's "x"`} {
		f.Add(s)
	}
	f.Fuzz(func(t *testing.T, line string) {
		argv, err := ParseCommandLine(line)
		if err != nil {
			return
		}
		if len(argv) == 0 {
			t.Fatal("accepted a line with no words")
		}
		if p := checkArgvClean(argv); p != "" {
			t.Fatalf("%q -> %q: %s", line, argv, p)
		}
	})
}

// FuzzDecidePaths: whenever Decide allows a Read, Search or Write, every path
// has a root as a prefix at a separator boundary and no ".." part.
func FuzzDecidePaths(f *testing.F) {
	f.Add(`C:\work\repo\a.go`, `C:\work\repo`, true, uint8(0))
	f.Add(`C:\work\repo-evil\a`, `C:\work\repo`, false, uint8(2))
	f.Add(`/srv/repo/../etc`, `/srv/repo`, false, uint8(1))
	f.Add(`c:/WORK/repo/x`, `C:\work\repo`, true, uint8(0))
	f.Fuzz(func(t *testing.T, canon, root string, fold bool, tool uint8) {
		tl := []Tool{ToolRead, ToolSearch, ToolWrite}[int(tool)%3]
		p := Policy{Mode: core.ModeCode, ReadRoots: []string{root}, WriteRoots: []string{root}, FoldCase: fold, Enforcement: EnforcementHook}
		if p.Validate() != nil {
			return
		}
		v := Decide(p, Call{Tool: tl, Paths: []PathRef{{Canon: canon, OK: true}}})
		if !v.Allow {
			return
		}
		if hasDotDot(canon) || !refInside(root, canon, fold) {
			t.Fatalf("%s allowed %q under root %q (fold %v)", tl, canon, root, fold)
		}
	})
}
