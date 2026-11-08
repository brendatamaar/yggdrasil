package builds_test

import (
	"slices"
	"strings"
	"testing"
)

// Invariant 3 for verification and bootstrap steps: both run as builds, so the
// Start spec the runner receives carries the clean environment. Tokens that
// the Yggdrasil process itself holds never appear in it.
func TestInvariant_3_build_steps_get_no_token_vars(t *testing.T) {
	leaks := map[string]string{
		"GITHUB_TOKEN": "ghp_aaaaaaaaaaaaaaaaaaaaaaaa", "GITLAB_TOKEN": "glpat-bbbbbbbbbbbbbbbbbbbb",
		"OPENAI_API_KEY": "sk-cccccccccccccccccccc", "NPM_TOKEN": "eeeeeeeeeeeeeeeeeeee",
		"VIRTUAL_ENV": "C:/venv-leak", "PYTHONPATH": "C:/pp-leak", "CONDA_PREFIX": "C:/conda-leak",
	}
	for k, v := range leaks {
		t.Setenv(k, v)
	}
	h := newHarness(t)
	h.start()
	id := h.enqueue(steps(exit(0), exit(0))) // a chain, as verify and bootstrap enqueue
	h.finished(id)
	got := h.fake.Started()
	if len(got) == 0 {
		t.Fatal("nothing started")
	}
	for i, sp := range got {
		for _, kv := range sp.Env {
			name, val, _ := strings.Cut(kv, "=")
			if v, bad := leaks[strings.ToUpper(name)]; bad || slices.Contains(valuesOf(leaks), val) {
				t.Errorf("step %d env has %s=%q (%s)", i, name, val, v)
			}
		}
	}
}

func valuesOf(m map[string]string) []string {
	out := make([]string, 0, len(m))
	for _, v := range m {
		out = append(out, v)
	}
	return out
}
