package composer

import (
	"strings"
	"testing"
)

func TestParsePromptRejectsEachRule(t *testing.T) {
	cases := map[string][2]string{
		"empty name":      {"  ", "x"},
		"long name":       {strings.Repeat("a", MaxPromptName+1), "x"},
		"control in name": {"a\nb", "x"},
		"empty body":      {"n", " \n"},
		"long body":       {"n", strings.Repeat("a", MaxPromptBody+1)},
		"nul in body":     {"n", "a\x00b"},
		"bad utf8 name":   {"a\xffb", "x"},
	}
	for name, c := range cases {
		if _, err := ParsePrompt(c[0], c[1]); err == nil {
			t.Errorf("%s: accepted", name)
		}
	}
}

func TestParsePromptLimitsAreInclusive(t *testing.T) {
	if _, err := ParsePrompt(strings.Repeat("é", MaxPromptName), strings.Repeat("a", MaxPromptBody)); err != nil {
		t.Fatalf("at the limits: %v", err)
	}
}

func TestParsePromptRoundTrip(t *testing.T) {
	p, err := ParsePrompt("  Fix tests ", "line1\r\nline2")
	if err != nil {
		t.Fatal(err)
	}
	if p.Name != "Fix tests" {
		t.Fatalf("name %q", p.Name)
	}
	again, err := ParsePrompt(p.Name, p.Text)
	if err != nil || again != p {
		t.Fatalf("round trip: %+v %v", again, err)
	}
	list := []StoredPrompt{{ID: "prm_1", Name: p.Name, Text: p.Text}}
	raw, err := FormatPrompts(list)
	if err != nil {
		t.Fatal(err)
	}
	back, err := ParsePrompts(raw)
	if err != nil || len(back) != 1 || back[0] != list[0] {
		t.Fatalf("stored round trip: %+v %v", back, err)
	}
}

func TestParsePromptsRejectsDamage(t *testing.T) {
	for _, raw := range []string{
		`not json`,
		`[{"id":"","name":"a","text":"b"}]`,
		`[{"id":"1","name":"a","text":"b"},{"id":"1","name":"c","text":"d"}]`,
		`[{"id":"1","name":" a","text":"b"}]`,
	} {
		if _, err := ParsePrompts(raw); err == nil {
			t.Errorf("accepted %q", raw)
		}
	}
	var sb strings.Builder
	sb.WriteString("[")
	for i := 0; i <= MaxPrompts; i++ {
		if i > 0 {
			sb.WriteString(",")
		}
		sb.WriteString(`{"id":"` + strings.Repeat("x", i+1) + `","name":"n","text":"t"}`)
	}
	sb.WriteString("]")
	if _, err := ParsePrompts(sb.String()); err == nil {
		t.Error("accepted too many prompts")
	}
}

func FuzzParsePrompt(f *testing.F) {
	f.Add("name", "body")
	f.Add("", "\x00")
	f.Fuzz(func(t *testing.T, name, text string) {
		p, err := ParsePrompt(name, text)
		if err != nil {
			return
		}
		if err := p.Validate(); err != nil {
			t.Fatalf("accepted but invalid: %+v: %v", p, err)
		}
		if q, err := ParsePrompt(p.Name, p.Text); err != nil || q != p {
			t.Fatalf("round trip broke: %+v %v", q, err)
		}
	})
}
