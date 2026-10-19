package git

import "testing"

func TestParseTrack(t *testing.T) {
	cases := []struct {
		in   string
		a, b int
	}{
		{"", 0, 0}, {"[gone]", 0, 0}, {"[ahead 3]", 3, 0}, {"[behind 2]", 0, 2},
		{"[ahead 1, behind 4]", 1, 4}, {"[ahead x]", 0, 0}, {"[ahead -1]", 0, 0},
	}
	for _, c := range cases {
		if a, b := parseTrack(c.in); a != c.a || b != c.b {
			t.Errorf("parseTrack(%q) = %d, %d; want %d, %d", c.in, a, b, c.a, c.b)
		}
	}
}

func TestBranchesAheadBehind(t *testing.T) {
	f := newFx(t)
	a, b := f.pair()
	f.commit(a, "a.txt", "x", "remote work")
	f.git(a, "push", "-q")
	f.commit(b, "b.txt", "y", "local work")
	if err := f.g.Fetch(bg(), b, ""); err != nil {
		t.Fatal(err)
	}
	brs, err := f.g.Branches(bg(), b)
	if err != nil {
		t.Fatal(err)
	}
	for _, br := range brs {
		if br.Name == "main" && (br.Ahead != 1 || br.Behind != 1) {
			t.Errorf("main ahead/behind = %d/%d, want 1/1", br.Ahead, br.Behind)
		}
	}
}

func TestBranchExists(t *testing.T) {
	f := newFx(t)
	a, b := f.pair()
	f.git(a, "branch", "remote-only")
	f.git(a, "push", "-q", "origin", "remote-only")
	f.git(b, "branch", "local-only")
	if err := f.g.Fetch(bg(), b, ""); err != nil {
		t.Fatal(err)
	}
	cases := []struct {
		name          string
		local, remote bool
	}{
		{"main", true, true}, {"local-only", true, false}, {"remote-only", false, true}, {"nope", false, false},
	}
	for _, c := range cases {
		l, r, err := f.g.BranchExists(bg(), b, c.name)
		if err != nil || l != c.local || r != c.remote {
			t.Errorf("BranchExists(%q) = %v, %v, %v; want %v, %v", c.name, l, r, err, c.local, c.remote)
		}
	}
	if _, _, err := f.g.BranchExists(bg(), b, "-x"); err == nil {
		t.Error("an option-like name was accepted")
	}
}

func TestLogRef(t *testing.T) {
	f := newFx(t)
	_, b := f.pair()
	f.git(b, "branch", "topic")
	f.commit(b, "n.txt", "n", "on main")
	got, err := f.g.LogRef(bg(), b, "topic", 10)
	if err != nil || len(got) != 1 || got[0].Subject != "init" {
		t.Fatalf("LogRef(topic) = %+v, %v", got, err)
	}
	if _, err := f.g.LogRef(bg(), b, "--all", 10); err == nil {
		t.Error("an option-like ref was accepted")
	}
}
