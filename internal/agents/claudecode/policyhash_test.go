package claudecode

import (
	"encoding/json"
	"os"
	"path/filepath"
	"strings"
	"testing"
)

func (h *hookEnv) read() map[string]any {
	return h.tool("Read", map[string]any{"file_path": filepath.Join(h.repo, "a.go")})
}

// writeRaw writes the config as given, without sealing.
func (h *hookEnv) writeRaw(cfg HookConfig) {
	h.t.Helper()
	data, err := json.Marshal(cfg)
	if err != nil {
		h.t.Fatal(err)
	}
	_ = os.Chmod(h.cfgPath, 0o600)
	if err := os.WriteFile(h.cfgPath, data, 0o600); err != nil {
		h.t.Fatal(err)
	}
}

func TestHookDeniesAfterPolicyRewrite(t *testing.T) {
	h := newHookEnv(t, 0, 0)
	if code, stderr := h.call("PreToolUse", h.read()); code != 0 {
		t.Fatalf("untouched run denied: %d %s", code, stderr)
	}
	orig, err := os.ReadFile(h.policy)
	if err != nil {
		t.Fatal(err)
	}
	_ = os.Chmod(h.policy, 0o600)
	if err := os.WriteFile(h.policy, append([]byte(" "), orig...), 0o600); err != nil {
		t.Fatal(err)
	}
	code, stderr := h.call("PreToolUse", h.read())
	if code != 2 {
		t.Fatalf("call after a policy rewrite: exit %d, want 2", code)
	}
	if strings.Contains(stderr, h.policy) || strings.Contains(stderr, h.repo) {
		t.Errorf("reason shows a path: %q", stderr)
	}
	// The call that would restore the bytes is judged first, so it is denied
	// too, and both denials are recorded.
	if code, _ := h.call("PreToolUse", h.read()); code != 2 {
		t.Errorf("restoring call was allowed")
	}
	if err := os.WriteFile(h.policy, orig, 0o600); err != nil {
		t.Fatal(err)
	}
	recs, _ := readHookLog(h.log)
	denied := 0
	for _, r := range recs {
		if r.Decision == decisionDeny {
			denied++
		}
	}
	if denied != 2 {
		t.Errorf("recorded denies = %d, want 2", denied)
	}
}

func TestHookDeniesTamperedHookConfig(t *testing.T) {
	for name, mutate := range map[string]func(c *HookConfig){
		"hash changed":       func(c *HookConfig) { c.PolicySHA256 = strings.Repeat("0", 64) },
		"hash removed":       func(c *HookConfig) { c.PolicySHA256, c.SelfCheck = "", "" },
		"self check removed": func(c *HookConfig) { c.SelfCheck = "" },
		"path changed":       func(c *HookConfig) { c.PolicyPath += "x" },
	} {
		h := newHookEnv(t, 0, 0)
		var cfg HookConfig
		data, _ := os.ReadFile(h.cfgPath)
		if err := json.Unmarshal(data, &cfg); err != nil {
			t.Fatal(err)
		}
		mutate(&cfg)
		h.writeRaw(cfg)
		if code, _ := h.call("PreToolUse", h.read()); code != 2 {
			t.Errorf("%s: exit %d, want 2", name, code)
		}
	}
}
