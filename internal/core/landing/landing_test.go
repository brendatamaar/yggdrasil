package landing

import (
	"strings"
	"testing"
	"unicode/utf8"
)

func TestStagingPath(t *testing.T) {
	cases := []struct{ name, id, leaf string }{
		{"shop-api-ab12", "r1", "shop-api-ab12-r1"},
		{"..", "r1", "wt-r1"},
		{"", "r1", "wt-r1"},
		{`a/b\c:d`, "r1", "a_b_c_d-r1"},
		{".hidden", "r1", "hidden-r1"},
		{"my repo", "x", "my_repo-x"},
		{"é", "x", "_-x"},
	}
	for _, c := range cases {
		got, err := StagingPath("D:/wt", c.name, c.id)
		if err != nil || got.Leaf != c.leaf || got.Root != "D:/wt" {
			t.Errorf("StagingPath(%q,%q) = %+v, %v; want leaf %q", c.name, c.id, got, err, c.leaf)
		}
	}
	long, _ := StagingPath("r", strings.Repeat("a", 500), "id")
	if utf8.RuneCountInString(long.Leaf) != maxStageNameRunes+3 {
		t.Errorf("long name not capped: %q", long.Leaf)
	}
	a, _ := StagingPath("r", "n", "id1")
	b, _ := StagingPath("r", "n", "id2")
	if a.Leaf == b.Leaf {
		t.Error("two ids gave one leaf")
	}
}

func TestStagingPathRejects(t *testing.T) {
	for _, c := range []struct{ root, id string }{
		{"", "id"}, {"r\x00", "id"}, {"r", ""}, {"r", "a/b"}, {"r", "a b"}, {"r", ".."}, {"r", strings.Repeat("a", 65)},
	} {
		if _, err := StagingPath(c.root, "n", c.id); err == nil {
			t.Errorf("StagingPath(%q, n, %q) accepted", c.root, c.id)
		}
	}
}

func FuzzStagingPath(f *testing.F) {
	f.Add("root", "name", "id")
	f.Add("r", "../..", "x")
	f.Fuzz(func(t *testing.T, root, name, id string) {
		p, err := StagingPath(root, name, id)
		if err != nil {
			return
		}
		if strings.ContainsAny(p.Leaf, `/\:`) || p.Leaf == "." || p.Leaf == ".." || strings.HasPrefix(p.Leaf, ".") || !strings.HasSuffix(p.Leaf, "-"+id) {
			t.Fatalf("unsafe leaf %q", p.Leaf)
		}
	})
}

func TestReqValidate(t *testing.T) {
	m := MergeReq{Repo: "r", Worktree: "w", Branch: "ygg/x", ExpectBranchHead: strings.Repeat("a", 40), TargetBranch: "main", Title: "t", Path: "C:/r"}
	if err := m.Validate(); err != nil {
		t.Fatal(err)
	}
	m.ExpectBranchHead = "xyz"
	if m.Validate() == nil {
		t.Error("bad head accepted")
	}
	r := RemoveReq{Worktree: "w", Repo: "r", Path: "p", Root: "root", Name: "n"}
	if err := r.Validate(); err != nil {
		t.Fatal(err)
	}
	r.Root = ""
	if r.Validate() == nil {
		t.Error("empty root accepted")
	}
	if LockClass("").Validate() == nil {
		t.Error("empty lock class accepted")
	}
}

func TestStepStubsChangeNothing(t *testing.T) {
	ms, mc := StepMerge(MergeIdle{}, MergeStart{})
	if _, ok := ms.(MergeIdle); !ok || len(mc) != 0 {
		t.Fatal("merge stub changed state")
	}
	rs, rc := StepRemove(RemoveIdle{}, RemoveStart{})
	if _, ok := rs.(RemoveIdle); !ok || len(rc) != 0 {
		t.Fatal("remove stub changed state")
	}
}
