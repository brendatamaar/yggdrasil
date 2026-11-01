package builds

import (
	"fmt"
	"reflect"
	"strings"
	"testing"
)

func TestParseBuildSettingsDefaultsAndFields(t *testing.T) {
	s, err := ParseBuildSettings([]byte(`{}`))
	if err != nil || !reflect.DeepEqual(s, DefaultBuildSettings()) {
		t.Fatalf("{} = %+v, %v; want the defaults", s, err)
	}
	s, err = ParseBuildSettings([]byte(`{"verify":[{"name":"lint","argv":["npm","run","lint"]}],"fullTests":true,"bootstrap":false}`))
	want := RepoBuildSettings{VerifyOverride: []Command{{Name: "lint", Argv: []string{"npm", "run", "lint"}}}, FullTests: true}
	if err != nil || !reflect.DeepEqual(s, want) {
		t.Fatalf("got %+v, %v; want %+v", s, err, want)
	}
	s, err = ParseBuildSettings([]byte(" \n{\"bootstrap\":true}\n "))
	if err != nil || !s.Bootstrap {
		t.Fatalf("whitespace around the object: %+v %v", s, err)
	}
}

func TestParseBuildSettingsRejectsEachRule(t *testing.T) {
	long := strings.Repeat("a", 201)
	many := make([]string, 65)
	for i := range many {
		many[i] = `{"name":"s","argv":["x"]}`
	}
	cases := map[string]string{
		"empty input":      ``,
		"blank input":      ` `,
		"not json":         `{`,
		"not an object":    `[]`,
		"unknown field":    `{"verifyy":[]}`,
		"unknown in step":  `{"verify":[{"name":"a","argv":["x"],"env":["A=1"]}]}`,
		"trailing data":    `{} {}`,
		"trailing garbage": `{}x`,
		"wrong type":       `{"fullTests":"yes"}`,
		"no name":          `{"verify":[{"argv":["x"]}]}`,
		"blank name":       `{"verify":[{"name":"  ","argv":["x"]}]}`,
		"long name":        `{"verify":[{"name":"` + long + `","argv":["x"]}]}`,
		"no argv":          `{"verify":[{"name":"a"}]}`,
		"empty argv":       `{"verify":[{"name":"a","argv":[]}]}`,
		"empty program":    `{"verify":[{"name":"a","argv":[""]}]}`,
		"NUL":              `{"verify":[{"name":"a","argv":["x","a\u0000b"]}]}`,
		"batch unsafe":     `{"verify":[{"name":"a","argv":["x","a&b"]}]}`,
		"percent":          `{"verify":[{"name":"a","argv":["x","%PATH%"]}]}`,
		"too many steps":   `{"verify":[` + strings.Join(many, ",") + `]}`,
	}
	for name, in := range cases {
		if s, err := ParseBuildSettings([]byte(in)); err == nil {
			t.Errorf("%s: accepted as %+v", name, s)
		}
	}
	// The limits themselves are fine.
	ok := strings.Join(many[:64], ",")
	if _, err := ParseBuildSettings([]byte(`{"verify":[` + ok + `]}`)); err != nil {
		t.Errorf("64 steps: %v", err)
	}
	if _, err := ParseBuildSettings([]byte(`{"verify":[{"name":"` + long[:200] + `","argv":["x"]}]}`)); err != nil {
		t.Errorf("200-rune name: %v", err)
	}
}

func TestFormatBuildSettingsRefusesInvalid(t *testing.T) {
	if _, err := FormatBuildSettings(RepoBuildSettings{VerifyOverride: []Command{{Name: "x"}}}); err == nil {
		t.Fatal("formatted a step without a command")
	}
}

func sampleSettings() []RepoBuildSettings {
	return []RepoBuildSettings{
		DefaultBuildSettings(),
		{},
		{FullTests: true, Bootstrap: true},
		{VerifyOverride: []Command{{Name: "compile", Argv: []string{"mvnw", "-B", "test-compile"}}, {Name: "ünï", Argv: []string{"tool", "a b", "--x=1"}}}, FullTests: true},
	}
}

func TestBuildSettingsRoundTrip(t *testing.T) {
	for i, s := range sampleSettings() {
		b, err := FormatBuildSettings(s)
		if err != nil {
			t.Fatalf("%d: %v", i, err)
		}
		got, err := ParseBuildSettings(b)
		if err != nil || !reflect.DeepEqual(got, s) {
			t.Errorf("%d: parse(format(x)) = %+v, %v; want %+v (json %s)", i, got, err, s, b)
		}
		again, _ := FormatBuildSettings(got)
		if string(again) != string(b) {
			t.Errorf("%d: format is not stable: %s vs %s", i, b, again)
		}
	}
}

func FuzzParseBuildSettings(f *testing.F) {
	for _, s := range sampleSettings() {
		b, _ := FormatBuildSettings(s)
		f.Add(b)
	}
	f.Add([]byte(`{"verify":[{"name":"a","argv":["x","\u0000"]}]}`))
	f.Add([]byte(`{}{}`))
	f.Fuzz(func(t *testing.T, data []byte) {
		s, err := ParseBuildSettings(data)
		if err != nil {
			return
		}
		// accepted implies valid
		if err := s.Validate(); err != nil {
			t.Fatalf("accepted %q but invalid: %v", data, err)
		}
		// and survives a round trip
		b, err := FormatBuildSettings(s)
		if err != nil {
			t.Fatalf("accepted %q but cannot format: %v", data, err)
		}
		got, err := ParseBuildSettings(b)
		if err != nil || fmt.Sprintf("%#v", got) != fmt.Sprintf("%#v", s) {
			t.Fatalf("round trip of %q: %+v, %v", data, got, err)
		}
	})
}
