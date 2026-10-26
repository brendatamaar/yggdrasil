package safety

import (
	"bytes"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"os"
	"path/filepath"

	"github.com/brendatamaar/yggdrasil/internal/core/guard"
)

// MaxAllowlistRules bounds a stored allowlist.
const MaxAllowlistRules = 200

// maxPolicyFile bounds a policy file the hook reads.
const maxPolicyFile = 1 << 20

type ruleWire struct {
	Program string   `json:"program"`
	Args    []string `json:"args"`
	Tail    string   `json:"tail"`
}

// ValidateRule checks one stored allowlist rule. The rules live in the pure
// core (guard.CommandRule.Validate), so the hook's policy parser applies the
// same ones; this wrapper keeps the settings-side name.
func ValidateRule(r guard.CommandRule) error { return r.Validate() }


// ParseAllowlist reads the JSON stored in the settings table: an array of
// {"program","args","tail"}. It refuses unknown fields, trailing data, more
// than MaxAllowlistRules rules, and any rule ValidateRule refuses.
func ParseAllowlist(data []byte) ([]guard.CommandRule, error) {
	dec := json.NewDecoder(bytes.NewReader(data))
	dec.DisallowUnknownFields()
	var w []ruleWire
	if err := dec.Decode(&w); err != nil {
		return nil, fmt.Errorf("parse allowlist: %w", err)
	}
	if _, err := dec.Token(); !errors.Is(err, io.EOF) {
		return nil, errors.New("parse allowlist: data after the list")
	}
	if len(w) > MaxAllowlistRules {
		return nil, fmt.Errorf("parse allowlist: more than %d rules", MaxAllowlistRules)
	}
	rules := make([]guard.CommandRule, 0, len(w))
	for i, r := range w {
		rule := guard.CommandRule{Program: r.Program, Tail: guard.TailRule(r.Tail)}
		if len(r.Args) > 0 {
			rule.Args = r.Args
		}
		if err := ValidateRule(rule); err != nil {
			return nil, fmt.Errorf("parse allowlist: rule %d: %w", i+1, err)
		}
		rules = append(rules, rule)
	}
	return rules, nil
}

// FormatAllowlist is the inverse of ParseAllowlist for every valid list:
// ParseAllowlist(FormatAllowlist(r)) equals r (nil and empty Args are the
// same). It returns nil if a rule is invalid.
func FormatAllowlist(rules []guard.CommandRule) []byte {
	if len(rules) > MaxAllowlistRules {
		return nil
	}
	w := make([]ruleWire, 0, len(rules))
	for _, r := range rules {
		if ValidateRule(r) != nil {
			return nil
		}
		w = append(w, ruleWire{Program: r.Program, Args: r.Args, Tail: string(r.Tail)})
	}
	out, err := json.Marshal(w)
	if err != nil {
		return nil
	}
	return out
}

// WritePolicyFile writes the policy for one run into a new folder under
// parent (os.MkdirTemp creates it 0700) and returns the file path (0600, never overwritten).
// parent should be Yggdrasil's own per-run temp folder under the user
// profile. The path goes to the hook through its config, never on a command
// line. On Windows the mode bits are not enforced; the user-profile temp
// folder is private to the user.
func WritePolicyFile(parent string, p guard.Policy) (string, error) {
	if err := p.Validate(); err != nil {
		return "", fmt.Errorf("write policy: %w", err)
	}
	dir, err := os.MkdirTemp(parent, "ygg-policy-*")
	if err != nil {
		return "", fmt.Errorf("write policy: create folder: %w", err)
	}
	file := filepath.Join(dir, "policy.json")
	f, err := os.OpenFile(file, os.O_WRONLY|os.O_CREATE|os.O_EXCL, 0o600) //nolint:gosec // fresh private folder
	if err != nil {
		return "", fmt.Errorf("write policy: create file: %w", err)
	}
	data := guard.FormatPolicy(p)
	if data == nil {
		_ = f.Close()
		return "", errors.New("write policy: the policy could not be encoded")
	}
	if _, err := f.Write(data); err != nil {
		_ = f.Close()
		return "", fmt.Errorf("write policy: %w", err)
	}
	if err := f.Close(); err != nil {
		return "", fmt.Errorf("write policy: %w", err)
	}
	return file, nil
}

// ReadPolicyFile reads and validates a policy file (at most 1 MiB).
func ReadPolicyFile(path string) (guard.Policy, error) {
	f, err := os.Open(path) //nolint:gosec // the caller passes the file WritePolicyFile made
	if err != nil {
		return guard.Policy{}, fmt.Errorf("read policy: %w", err)
	}
	defer f.Close()
	data, err := io.ReadAll(io.LimitReader(f, maxPolicyFile+1))
	if err != nil {
		return guard.Policy{}, fmt.Errorf("read policy: %w", err)
	}
	if len(data) > maxPolicyFile {
		return guard.Policy{}, errors.New("read policy: the file is too large")
	}
	return guard.ParsePolicy(data)
}
