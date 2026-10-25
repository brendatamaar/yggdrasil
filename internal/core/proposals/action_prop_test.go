package proposals

import (
	"math/rand"
	"reflect"
	"strings"
	"testing"
)

// mutations returns, for each field of the action struct, a copy with just
// that field changed. It works by reflection so a field added later is
// covered without editing the test.
func mutations(t *testing.T, a Action) map[string]Action {
	t.Helper()
	out := map[string]Action{}
	v := reflect.ValueOf(a)
	for i := 0; i < v.NumField(); i++ {
		c := reflect.New(v.Type()).Elem()
		c.Set(v)
		f := c.Field(i)
		switch f.Kind() {
		case reflect.String:
			f.SetString(f.String() + "x")
		case reflect.Slice:
			f.Set(reflect.Append(f, reflect.ValueOf(RelPath("extra/file.txt"))))
		case reflect.Interface: // Target
			switch tg := f.Interface().(type) {
			case InWorktree:
				f.Set(reflect.ValueOf(InWorktree{ID: tg.ID + "x"}))
			case MainCheckout:
				f.Set(reflect.ValueOf(MainCheckout{Repo: tg.Repo + "x"}))
			}
		default:
			t.Fatalf("%T field %s has kind %s that the test cannot mutate", a, v.Type().Field(i).Name, f.Kind())
		}
		out[v.Type().Field(i).Name] = c.Interface().(Action)
	}
	return out
}

// P8: the hash is a function of the action alone and covers every field.
func TestPropHashCoversEveryField(t *testing.T) {
	forSeeds(t, func(t *testing.T, r *rand.Rand) {
		seed := r.Int63()
		a, b := genAction(rand.New(rand.NewSource(seed))), genAction(rand.New(rand.NewSource(seed)))
		h := Hash(a)
		if len(h) != 64 || h != Hash(b) {
			t.Fatalf("equal actions, hashes %q and %q", h, Hash(b))
		}
		for field, m := range mutations(t, a) {
			if Hash(m) == h {
				t.Errorf("%T: changing %s does not change the hash", a, field)
			}
		}
	})
}

// Different kinds with look-alike fields never collide, and neither do
// actions that differ only in how fields are split.
func TestHashSeparatesFields(t *testing.T) {
	w := InWorktree{ID: "w"}
	if Hash(Push{Where: w, Branch: "a", Remote: "bc"}) == Hash(Push{Where: w, Branch: "ab", Remote: "c"}) {
		t.Error("field boundary not part of the encoding")
	}
	if Hash(Commit{Where: w, Paths: []RelPath{"a", "b"}}) == Hash(Commit{Where: w, Paths: []RelPath{"a;1:b"}}) {
		t.Error("path boundary not part of the encoding")
	}
	if Hash(DeleteFiles{Where: w, Paths: []RelPath{"a"}}) == Hash(Commit{Where: w, Paths: []RelPath{"a"}}) {
		t.Error("kind not part of the encoding")
	}
	if Hash(Push{Where: InWorktree{ID: "x"}}) == Hash(Push{Where: MainCheckout{Repo: "x"}}) {
		t.Error("target type not part of the encoding")
	}
	if Hash(nil) != "" {
		t.Error("nil action has a hash")
	}
}

// P9: the dialog text names every path, branch, remote, message and target.
func TestPropDescribeShowsEverything(t *testing.T) {
	forSeeds(t, func(t *testing.T, r *rand.Rand) {
		a := genAction(r)
		var sb strings.Builder
		for _, l := range Describe(a) {
			sb.WriteString(l.Text)
			sb.WriteByte('\n')
		}
		text := sb.String()
		need := func(s string) {
			t.Helper()
			if s != "" && !strings.Contains(text, s) {
				t.Errorf("%T: dialog text does not show %q:\n%s", a, s, text)
			}
		}
		target := func(tg Target) {
			switch v := tg.(type) {
			case InWorktree:
				need(string(v.ID))
			case MainCheckout:
				need(string(v.Repo))
			}
		}
		switch v := a.(type) {
		case Commit:
			target(v.Where)
			for _, p := range v.Paths {
				need(string(p))
			}
			need(v.Message)
			need(v.ExpectHead)
			if !strings.Contains(text, plural(len(v.Paths), "file", "files")) {
				t.Errorf("file count missing:\n%s", text)
			}
		case Push:
			target(v.Where)
			need(v.Branch)
			need(v.Remote)
			need(v.ExpectHead)
		case MergeRequest:
			target(v.Where)
			need(v.Branch)
			need(v.Remote)
			need(v.TargetBranch)
			need(v.Title)
			need(v.Description)
			need("The push is part of this merge request")
			need("The description is one line")
		case AgentFile:
			target(v.Where)
			need(string(v.Path))
			need(v.Content)
		case DeleteFiles:
			target(v.Where)
			for _, p := range v.Paths {
				need(string(p))
			}
			need("_to_delete")
		case Install:
			need(string(v.Repo))
			need(v.Step)
		}
	})
}

func TestDescribeNilAndKinds(t *testing.T) {
	if Describe(nil) != nil {
		t.Error("nil action described")
	}
	for _, a := range []Action{Commit{}, Push{}, MergeRequest{}, AgentFile{}, DeleteFiles{}, Install{}} {
		if len(Describe(a)) == 0 {
			t.Errorf("%T has no description", a)
		}
	}
}
