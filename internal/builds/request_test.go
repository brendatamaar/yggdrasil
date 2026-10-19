package builds

import (
	"fmt"
	"math/rand/v2"
	"reflect"
	"strings"
	"testing"

	"github.com/brendatamaar/yggdrasil/internal/apperr"
	"github.com/brendatamaar/yggdrasil/internal/core"
	corebuilds "github.com/brendatamaar/yggdrasil/internal/core/builds"
)

var known = Known{
	Repos:     map[core.RepoID]struct{}{"r1": {}, "r2": {}},
	Worktrees: map[core.WorktreeID]core.RepoID{"wt1": "r1"},
}

const goodReq = `{"target":"main","steps":[{"repoId":"r1","name":"build","argv":["mvn","-q","package"]}]}`

func steps(n int) string {
	parts := make([]string, n)
	for i := range parts {
		parts[i] = `{"repoId":"r1","name":"s","argv":["x"]}`
	}
	return strings.Join(parts, ",")
}

func TestParseRequestRejects(t *testing.T) {
	cases := map[string]string{
		"not json":             `{`,
		"unknown field":        `{"target":"main","steps":[{"repoId":"r1","name":"b","argv":["x"]}],"extra":1}`,
		"trailing data":        goodReq + `{}`,
		"zero steps":           `{"target":"main","steps":[]}`,
		"no steps":             `{"target":"main"}`,
		"65 steps":             `{"target":"main","steps":[` + steps(65) + `]}`,
		"unknown target":       `{"target":"cloud","steps":[` + steps(1) + `]}`,
		"main with worktree":   `{"target":"main","worktreeId":"wt1","steps":[` + steps(1) + `]}`,
		"unknown repo":         `{"target":"main","steps":[{"repoId":"zzz","name":"b","argv":["x"]}]}`,
		"empty repo":           `{"target":"main","steps":[{"repoId":"","name":"b","argv":["x"]}]}`,
		"unknown worktree":     `{"target":"worktree","worktreeId":"nope","steps":[` + steps(1) + `]}`,
		"worktree missing":     `{"target":"worktree","steps":[` + steps(1) + `]}`,
		"worktree not of repo": `{"target":"worktree","worktreeId":"wt1","steps":[{"repoId":"r2","name":"b","argv":["x"]}]}`,
		"empty argv":           `{"target":"main","steps":[{"repoId":"r1","name":"b","argv":[]}]}`,
		"empty program":        `{"target":"main","steps":[{"repoId":"r1","name":"b","argv":[""]}]}`,
		"NUL in arg":           `{"target":"main","steps":[{"repoId":"r1","name":"b","argv":["x","a\u0000b"]}]}`,
		"ampersand":            `{"target":"main","steps":[{"repoId":"r1","name":"b","argv":["x","a&b"]}]}`,
		"percent":              `{"target":"main","steps":[{"repoId":"r1","name":"b","argv":["x","%PATH%"]}]}`,
		"quote":                `{"target":"main","steps":[{"repoId":"r1","name":"b","argv":["x","a\"b"]}]}`,
		"newline":              `{"target":"main","steps":[{"repoId":"r1","name":"b","argv":["x","a\nb"]}]}`,
		"pipe":                 `{"target":"main","steps":[{"repoId":"r1","name":"b","argv":["x","a|b"]}]}`,
		"empty name":           `{"target":"main","steps":[{"repoId":"r1","name":"  ","argv":["x"]}]}`,
		"name too long":        `{"target":"main","steps":[{"repoId":"r1","name":"` + strings.Repeat("é", 201) + `","argv":["x"]}]}`,
		"bad preset id":        `{"target":"main","presetId":"a b","steps":[` + steps(1) + `]}`,
	}
	for name, body := range cases {
		t.Run(name, func(t *testing.T) {
			_, err := ParseRequest([]byte(body), known)
			if err == nil {
				t.Fatal("accepted")
			}
			if c, ok := apperr.ClassOf(err); !ok || c != apperr.Invalid {
				t.Fatalf("error %v is not apperr.Invalid", err)
			}
			if m, _ := apperr.MessageOf(err); m == "" {
				t.Fatal("no user message")
			}
		})
	}
}

func TestParseRequestAccepts(t *testing.T) {
	long := strings.Repeat("é", 200)
	for _, body := range []string{
		goodReq,
		`{"target":"worktree","worktreeId":"wt1","presetId":"p1","steps":[{"repoId":"r1","name":"` + long + `","argv":["./gradlew.bat","test","--info"]}]}`,
		`{"target":"main","steps":[` + steps(64) + `]}`,
	} {
		spec, err := ParseRequest([]byte(body), known)
		if err != nil {
			t.Fatalf("%v: %s", err, body)
		}
		if err := spec.Validate(); err != nil {
			t.Fatalf("accepted an invalid spec: %v", err)
		}
	}
}

func randSpec(r *rand.Rand) corebuilds.Spec {
	s := corebuilds.Spec{Target: corebuilds.MainCheckout{}}
	repo := core.RepoID(fmt.Sprintf("r%d", 1+r.IntN(2)))
	if r.IntN(2) == 0 {
		s.Target = corebuilds.InWorktree{WorktreeID: "wt1"}
		repo = "r1"
	}
	if r.IntN(2) == 0 {
		s.PresetID = "p1"
	}
	for i := 0; i <= r.IntN(5); i++ {
		s.Steps = append(s.Steps, corebuilds.StepSpec{RepoID: repo, Name: fmt.Sprintf("step ü%d", i), Argv: []string{"tool", fmt.Sprintf("--n=%d", r.IntN(9)), "a b"}})
	}
	return s
}

func TestRequestRoundTrip(t *testing.T) {
	for seed := uint64(0); seed < 300; seed++ {
		spec := randSpec(rand.New(rand.NewPCG(seed, 1)))
		raw, err := FormatRequest(spec)
		if err != nil {
			t.Fatal(err)
		}
		got, err := ParseRequest(raw, known)
		if err != nil || !reflect.DeepEqual(got, spec) {
			t.Fatalf("round trip: err=%v\n got  %+v\n want %+v", err, got, spec)
		}
	}
}

func goodPreset() corebuilds.Preset {
	return corebuilds.Preset{ID: "pre1", ProjectID: "prj1", Name: "Full build", Steps: []corebuilds.StepSpec{
		{RepoID: "r1", Name: "install", Argv: []string{"npm", "ci"}},
	}}
}

func TestPresetRoundTripAndAccepts(t *testing.T) {
	p := goodPreset()
	raw, err := FormatPreset(p)
	if err != nil {
		t.Fatal(err)
	}
	got, err := ParsePreset(raw)
	if err != nil || !reflect.DeepEqual(got, p) {
		t.Fatalf("round trip: %v %+v", err, got)
	}
}

func TestParsePresetRejects(t *testing.T) {
	step := `{"repoId":"r1","name":"s","argv":["x"]}`
	cases := map[string]string{
		"not json":      `[`,
		"unknown field": `{"id":"p","projectId":"j","name":"n","steps":[` + step + `],"x":1}`,
		"no id":         `{"projectId":"j","name":"n","steps":[` + step + `]}`,
		"no project":    `{"id":"p","name":"n","steps":[` + step + `]}`,
		"empty name":    `{"id":"p","projectId":"j","name":"","steps":[` + step + `]}`,
		"name too long": `{"id":"p","projectId":"j","name":"` + strings.Repeat("n", 201) + `","steps":[` + step + `]}`,
		"zero steps":    `{"id":"p","projectId":"j","name":"n","steps":[]}`,
		"65 steps":      `{"id":"p","projectId":"j","name":"n","steps":[` + steps(65) + `]}`,
		"empty argv":    `{"id":"p","projectId":"j","name":"n","steps":[{"repoId":"r1","name":"s","argv":[]}]}`,
		"NUL":           `{"id":"p","projectId":"j","name":"n","steps":[{"repoId":"r1","name":"s","argv":["a\u0000"]}]}`,
		"metachar":      `{"id":"p","projectId":"j","name":"n","steps":[{"repoId":"r1","name":"s","argv":["a>b"]}]}`,
		"bad step name": `{"id":"p","projectId":"j","name":"n","steps":[{"repoId":"r1","name":"","argv":["a"]}]}`,
		"no repo":       `{"id":"p","projectId":"j","name":"n","steps":[{"repoId":"","name":"s","argv":["a"]}]}`,
	}
	for name, body := range cases {
		t.Run(name, func(t *testing.T) {
			_, err := ParsePreset([]byte(body))
			if c, ok := apperr.ClassOf(err); !ok || c != apperr.Invalid {
				t.Fatalf("want apperr.Invalid, got %v", err)
			}
		})
	}
}

func FuzzParseRequest(f *testing.F) {
	f.Add([]byte(goodReq))
	f.Add([]byte(`{"target":"worktree","worktreeId":"wt1","steps":[{"repoId":"r1","name":"b","argv":["x"]}]}`))
	f.Add([]byte(`{`))
	f.Fuzz(func(t *testing.T, raw []byte) {
		spec, err := ParseRequest(raw, known)
		if err != nil {
			if _, ok := apperr.ClassOf(err); !ok {
				t.Fatalf("error without a class: %v", err)
			}
			return
		}
		if err := spec.Validate(); err != nil {
			t.Fatalf("accepted an invalid spec: %v", err)
		}
		out, err := FormatRequest(spec)
		if err != nil {
			t.Fatal(err)
		}
		again, err := ParseRequest(out, known)
		if err != nil || !reflect.DeepEqual(again, spec) {
			t.Fatalf("round trip failed: %v", err)
		}
	})
}

func FuzzParsePreset(f *testing.F) {
	raw, _ := FormatPreset(goodPreset())
	f.Add(raw)
	f.Add([]byte(`{}`))
	f.Fuzz(func(t *testing.T, raw []byte) {
		p, err := ParsePreset(raw)
		if err != nil {
			if _, ok := apperr.ClassOf(err); !ok {
				t.Fatalf("error without a class: %v", err)
			}
			return
		}
		if err := p.Validate(); err != nil {
			t.Fatalf("accepted an invalid preset: %v", err)
		}
		out, err := FormatPreset(p)
		if err != nil {
			t.Fatal(err)
		}
		again, err := ParsePreset(out)
		if err != nil || !reflect.DeepEqual(again, p) {
			t.Fatalf("round trip failed: %v", err)
		}
	})
}
