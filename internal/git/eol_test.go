package git

import (
	"reflect"
	"slices"
	"strings"
	"testing"
)

func TestParseNumstat(t *testing.T) {
	got, err := ParseNumstat("2\t3\tf.txt\x001\t0\tsp aceé.txt\x00-\t-\timg.png\x000\t0\ttab\tname\x00")
	if err != nil {
		t.Fatal(err)
	}
	want := []NumstatEntry{
		{Path: "f.txt", Added: 2, Deleted: 3},
		{Path: "sp aceé.txt", Added: 1},
		{Path: "img.png", Binary: true},
		{Path: "tab\tname"},
	}
	if !reflect.DeepEqual(got, want) {
		t.Errorf("got %+v, want %+v", got, want)
	}
	if got, err := ParseNumstat(""); err != nil || len(got) != 0 {
		t.Errorf("empty = %v, %v", got, err)
	}
	for name, in := range map[string]string{
		"rename form":  "1\t0\t\x00a.txt\x00b.txt\x00",
		"two fields":   "1\tf.txt\x00",
		"not numbers":  "x\ty\tf.txt\x00",
		"negative":     "-1\t2\tf.txt\x00",
		"half binary":  "-\t2\tf.txt\x00",
		"empty path":   "1\t2\t\x00",
		"no separator": "12f.txt\x00",
	} {
		if _, err := ParseNumstat(in); err == nil {
			t.Errorf("%s: accepted %q", name, in)
		}
	}
}

func TestClassifyEOL(t *testing.T) {
	cases := []struct {
		name, plain, ignoring string
		want                  []string
	}{
		{"line endings only, file omitted", "3\t3\ta.txt\x00", "", []string{"a.txt"}},
		{"line endings only, zero counts", "3\t3\ta.txt\x00", "0\t0\ta.txt\x00", []string{"a.txt"}},
		{"real change as well", "3\t3\ta.txt\x00", "1\t1\ta.txt\x00", nil},
		{"real added line only", "4\t3\ta.txt\x00", "1\t0\ta.txt\x00", nil},
		{"no change at all (mode only)", "0\t0\ta.txt\x00", "", nil},
		{"binary", "-\t-\tb.png\x00", "", nil},
		{"binary in the second", "2\t2\ta.txt\x00", "-\t-\ta.txt\x00", nil},
		{"mix, sorted", "1\t1\tz.txt\x002\t2\tb.txt\x001\t1\tm.txt\x00", "1\t1\tm.txt\x00", []string{"b.txt", "z.txt"}},
		{"empty", "", "", nil},
		{"only in the second", "", "1\t1\ta.txt\x00", nil},
		{"duplicate path once", "1\t1\ta.txt\x001\t1\ta.txt\x00", "", []string{"a.txt"}},
	}
	for _, c := range cases {
		got, err := ClassifyEOL(c.plain, c.ignoring)
		if err != nil {
			t.Errorf("%s: %v", c.name, err)
			continue
		}
		if !slices.Equal(got, c.want) {
			t.Errorf("%s: got %v, want %v", c.name, got, c.want)
		}
	}
	if _, err := ClassifyEOL("junk", ""); err == nil {
		t.Error("bad plain numstat accepted")
	}
	if _, err := ClassifyEOL("", "junk"); err == nil {
		t.Error("bad ignoring numstat accepted")
	}
}

// Whatever ClassifyEOL returns is sorted, unique, and every path has changed
// lines in the plain numstat that the ignoring numstat does not keep.
func FuzzClassifyEOL(f *testing.F) {
	f.Add("3\t3\ta.txt\x00", "")
	f.Add("1\t1\tz.txt\x002\t2\tb.txt\x00", "1\t1\tz.txt\x00")
	f.Add("-\t-\tb.png\x00", "0\t0\tb.png\x00")
	f.Fuzz(func(t *testing.T, plain, ignoring string) {
		got, err := ClassifyEOL(plain, ignoring)
		if err != nil {
			return
		}
		p, _ := ParseNumstat(plain)
		q, _ := ParseNumstat(ignoring)
		for i, path := range got {
			if i > 0 && got[i-1] >= path {
				t.Fatalf("not sorted and unique: %q", got)
			}
			var in bool
			for _, e := range p {
				if e.Path == path && !e.Binary && e.Added+e.Deleted > 0 {
					in = true
				}
			}
			if !in {
				t.Fatalf("%q has no changed lines in the plain numstat", path)
			}
			for _, e := range q {
				if e.Path == path && (e.Binary || e.Added+e.Deleted > 0) {
					t.Fatalf("%q still changes with CR ignored", path)
				}
			}
		}
	})
}

func TestCountPatch(t *testing.T) {
	p := "diff --git a/f b/f\nnew file mode 100644\nindex 0000000..587be6b\n--- /dev/null\n+++ b/f\t\n@@ -0,0 +1,2 @@\n+x\n+--y\n"
	if a, d, b := countPatch(p); a != 2 || d != 0 || b {
		t.Errorf("counts = %d, %d, %v", a, d, b)
	}
	if a, d, b := countPatch("diff --git a/f b/f\n--- a/f\n+++ b/f\n@@ -1,2 +1,2 @@\n-a\n+b\n c\n\\ No newline at end of file\n"); a != 1 || d != 1 || b {
		t.Errorf("counts = %d, %d, %v", a, d, b)
	}
	if _, _, b := countPatch("diff --git a/x b/x\nnew file mode 100644\nindex 0000000..1\nBinary files /dev/null and b/x differ\n"); !b {
		t.Error("binary patch not detected")
	}
	if a, d, b := countPatch(strings.Repeat("+++ b/f\n--- a/f\n", 2)); a != 0 || d != 0 || b {
		t.Errorf("header lines counted: %d, %d", a, d)
	}
}

func TestParseNameStatus(t *testing.T) {
	got, err := parseNameStatus("M\x00a b.txt\x00A\x00n.txt\x00D\x00gone.txt\x00T\x00t\x00U\x00u\x00")
	if err != nil {
		t.Fatal(err)
	}
	want := []nameStatus{{"M", "a b.txt"}, {"A", "n.txt"}, {"D", "gone.txt"}, {"M", "t"}, {"M", "u"}}
	if !reflect.DeepEqual(got, want) {
		t.Errorf("got %v", got)
	}
	if got, err := parseNameStatus(""); err != nil || got != nil {
		t.Errorf("empty = %v, %v", got, err)
	}
	for _, in := range []string{"M\x00", "M", "R100\x00a\x00b\x00", "X\x00a\x00", "M\x00\x00"} {
		if _, err := parseNameStatus(in); err == nil {
			t.Errorf("accepted %q", in)
		}
	}
}

func TestParseWorktreesPrunable(t *testing.T) {
	out := "worktree /repo\nHEAD " + oid40 + "\nbranch refs/heads/main\n\n" +
		"worktree /gone/wt\nHEAD " + oid40 + "\nbranch refs/heads/ygg/a\nprunable gitdir file points to non-existent location\n\n" +
		"worktree /locked/wt\nHEAD " + oid40 + "\ndetached\nlocked\nprunable\n\n" +
		"worktree /ok/wt\nHEAD " + oid40 + "\nbranch refs/heads/ygg/b\n"
	list := parseWorktrees(out)
	if len(list) != 4 {
		t.Fatalf("got %d entries: %+v", len(list), list)
	}
	prun := []bool{list[0].Prunable, list[1].Prunable, list[2].Prunable, list[3].Prunable}
	if !reflect.DeepEqual(prun, []bool{false, true, true, false}) {
		t.Errorf("prunable = %v", prun)
	}
	if !list[2].Locked || !list[2].Detached {
		t.Errorf("other markers lost: %+v", list[2])
	}
}

func TestOtherPrunable(t *testing.T) {
	list := []Worktree{
		{Path: "/repo"},
		{Path: "/data/wt/a", Prunable: true},
		{Path: "/data/wt/b"},
		{Path: "/data/wt/c", Prunable: true},
	}
	if !OtherPrunable(list, "/data/wt/a") {
		t.Error("c is prunable and is not a: want true")
	}
	if OtherPrunable(list[:3], "/data/wt/a") {
		t.Error("only a is prunable and a is the path: want false")
	}
	if !OtherPrunable(list[:3], "/data/wt/b") {
		t.Error("a is prunable and the path is b: want true")
	}
	if OtherPrunable(nil, "/x") || OtherPrunable([]Worktree{{Path: "/x"}}, "/x") {
		t.Error("nothing prunable: want false")
	}
	if OtherPrunable(list[:3], "/data/wt/../wt/a/") {
		t.Error("paths are compared cleaned")
	}
}

func TestIsCommitID(t *testing.T) {
	for s, want := range map[string]bool{
		oid40: true, strings.Repeat("a", 64): true,
		"": false, "abc": false, strings.Repeat("A", 40): false, strings.Repeat("g", 40): false,
		oid40 + "0": false, " " + oid40[1:]: false, "-" + oid40[1:]: false, "HEAD": false,
	} {
		if isCommitID(s) != want {
			t.Errorf("isCommitID(%q) = %v", s, !want)
		}
	}
}
