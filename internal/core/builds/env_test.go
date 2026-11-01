package builds

import (
	"fmt"
	"strings"
	"testing"
)

func TestStepSpecEnvValidate(t *testing.T) {
	ok := StepSpec{RepoID: "r", Name: "n", Argv: []string{"x"}, Env: []string{"CI=true", "A_1=", "_B=a=b"}}
	if err := ok.Validate(); err != nil {
		t.Fatal(err)
	}
	many := make([]string, MaxEnv+1)
	for i := range many {
		many[i] = fmt.Sprintf("K%d=v", i)
	}
	bad := map[string][]string{
		"no equals":        {"ABC"},
		"empty key":        {"=v"},
		"key with dash":    {"A-B=v"},
		"key with digit 1": {"1A=v"},
		"key with space":   {"A B=v"},
		"NUL in value":     {"A=b\x00c"},
		"newline in value": {"A=b\nc"},
		"long value":       {"A=" + strings.Repeat("x", MaxEnvValueLen+1)},
		"long key":         {strings.Repeat("A", MaxEnvKeyLen+1) + "=v"},
		"duplicate key":    {"A=1", "A=2"},
		"too many":         many,
	}
	for name, env := range bad {
		s := ok
		s.Env = env
		if s.Validate() == nil {
			t.Errorf("%s: accepted", name)
		}
	}
}
