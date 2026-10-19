package hosts

import (
	"fmt"
	"net/url"
	"os"
	"path/filepath"
	"regexp"
	"slices"
	"sort"
	"strconv"
	"strings"

	corehosts "github.com/brendatamaar/yggdrasil/internal/core/hosts"
)

// EnvFileName is the file in the data dir that holds the host config.
const EnvFileName = ".env"

// maxHostNumber bounds the n of YGG_HOST_n probed in the process environment.
const maxHostNumber = 64

// InstanceConfig is one configured host. The token is not part of it.
type InstanceConfig struct {
	ID   corehosts.InstanceID
	Kind corehosts.Kind
	// Base is "https://git.acme.dev", without a trailing slash.
	Base string
}

// Problem is a config line that was skipped. Reason never holds the value of
// the line (a token could be in it).
type Problem struct {
	// Line is the 1-based line of the .env file, or 0 for a process variable.
	Line int
	// Name is the variable, e.g. "YGG_HOST_2".
	Name   string
	Reason string
}

// String reads like "line 3 (YGG_HOST_2): the kind is not gitlab, github or gitea".
func (p Problem) String() string {
	where := "environment"
	if p.Line > 0 {
		where = fmt.Sprintf("line %d", p.Line)
	}
	if p.Name != "" {
		where += " (" + p.Name + ")"
	}
	return where + ": " + p.Reason
}

// Config is the parsed host config. Tokens are kept apart: they stay in the
// shell, are added to the redactor and are only ever sent in request headers.
type Config struct {
	Instances []InstanceConfig
	tokens    map[corehosts.InstanceID]string
	Problems  []Problem
}

// Token returns the instance's token, "" when it has none.
func (c Config) Token(id corehosts.InstanceID) string { return c.tokens[id] }

// Secrets returns every token, for the redactor (invariant 4).
func (c Config) Secrets() []string {
	out := make([]string, 0, len(c.tokens))
	for _, t := range c.tokens {
		out = append(out, t)
	}
	slices.Sort(out)
	return out
}

// CoreInstances converts the config to core instances. The token itself never
// enters the core, only whether there is one.
func (c Config) CoreInstances() []corehosts.Instance {
	out := make([]corehosts.Instance, 0, len(c.Instances))
	for _, in := range c.Instances {
		out = append(out, corehosts.Instance{ID: in.ID, Kind: in.Kind, Base: in.Base, Token: c.tokens[in.ID] != ""})
	}
	return out
}

// Var is one variable of the config. Line is 0 for a process variable.
type Var struct {
	Value string
	Line  int
}

// ParseEnvText reads .env text: "NAME=value" lines, "#" comments (whole-line
// or after whitespace), optional quotes, an optional "export ". Lines that
// are not assignments are reported by number (never echoed).
func ParseEnvText(text string) (map[string]Var, []Problem) {
	vars := map[string]Var{}
	var probs []Problem
	for i, line := range strings.Split(text, "\n") {
		line = strings.TrimSpace(strings.TrimSuffix(line, "\r"))
		if i == 0 {
			line = strings.TrimPrefix(line, "\xef\xbb\xbf")
		}
		if line == "" || strings.HasPrefix(line, "#") {
			continue
		}
		line = strings.TrimSpace(strings.TrimPrefix(line, "export "))
		name, val, ok := strings.Cut(line, "=")
		name = strings.TrimSpace(name)
		if !ok || !validVarName(name) {
			probs = append(probs, Problem{Line: i + 1, Reason: "this line is not NAME=value"})
			continue
		}
		vars[name] = Var{Value: unquote(val), Line: i + 1}
	}
	return vars, probs
}

func validVarName(s string) bool {
	if s == "" {
		return false
	}
	for _, c := range s {
		if c != '_' && (c < 'A' || c > 'Z') && (c < 'a' || c > 'z') && (c < '0' || c > '9') {
			return false
		}
	}
	return true
}

func unquote(v string) string {
	v = strings.TrimSpace(v)
	if len(v) >= 2 && (v[0] == '"' || v[0] == '\'') {
		if end := strings.IndexByte(v[1:], v[0]); end >= 0 {
			return v[1 : 1+end]
		}
	}
	if i := strings.Index(v, " #"); i >= 0 {
		v = v[:i]
	}
	if i := strings.Index(v, "\t#"); i >= 0 {
		v = v[:i]
	}
	if strings.HasPrefix(v, "#") {
		return ""
	}
	return strings.TrimSpace(v)
}

var hostVarRE = regexp.MustCompile(`^YGG_HOST_([0-9]{1,4})(_TOKEN)?$`)

// defaults are known without config (public repos, low rate limits).
var defaults = []InstanceConfig{
	{ID: "github.com", Kind: corehosts.KindGitHub, Base: "https://github.com"},
	{ID: "gitlab.com", Kind: corehosts.KindGitLab, Base: "https://gitlab.com"},
}

// ParseConfig turns the variables YGG_HOST_n="<kind> <base URL>" and
// YGG_HOST_n_TOKEN into a Config (boundary parser). It skips and reports a
// line with an unknown kind, a base that is not https (http only for
// localhost), a duplicate instance, or a token without a host. github.com and
// gitlab.com are added when not configured.
func ParseConfig(vars map[string]Var) Config {
	cfg := Config{tokens: map[corehosts.InstanceID]string{}}
	type numbered struct {
		n     int
		host  string
		hostV Var
		tokV  *Var
	}
	byN := map[int]*numbered{}
	for name, v := range vars {
		m := hostVarRE.FindStringSubmatch(name)
		if m == nil {
			continue
		}
		n, _ := strconv.Atoi(m[1])
		e := byN[n]
		if e == nil {
			e = &numbered{n: n}
			byN[n] = e
		}
		if m[2] != "" {
			v := v
			e.tokV = &v
		} else {
			e.host, e.hostV = name, v
		}
	}
	nums := make([]int, 0, len(byN))
	for n := range byN {
		nums = append(nums, n)
	}
	sort.Ints(nums)
	seen := map[corehosts.InstanceID]bool{}
	for _, n := range nums {
		e := byN[n]
		if e.host == "" {
			cfg.Problems = append(cfg.Problems, Problem{Line: e.tokV.Line, Name: fmt.Sprintf("YGG_HOST_%d_TOKEN", n), Reason: "there is a token but no YGG_HOST_" + strconv.Itoa(n) + " line for it"})
			continue
		}
		inst, err := parseInstance(e.hostV.Value)
		if err != nil {
			cfg.Problems = append(cfg.Problems, Problem{Line: e.hostV.Line, Name: e.host, Reason: err.Error()})
			continue
		}
		if seen[inst.ID] {
			cfg.Problems = append(cfg.Problems, Problem{Line: e.hostV.Line, Name: e.host, Reason: "this host is already configured by another line"})
			continue
		}
		seen[inst.ID] = true
		cfg.Instances = append(cfg.Instances, inst)
		if e.tokV != nil && e.tokV.Value != "" {
			if strings.ContainsFunc(e.tokV.Value, func(r rune) bool { return r <= ' ' || r == 0x7f }) {
				cfg.Problems = append(cfg.Problems, Problem{Line: e.tokV.Line, Name: fmt.Sprintf("YGG_HOST_%d_TOKEN", n), Reason: "the token has spaces or control characters, so it was ignored"})
				continue
			}
			cfg.tokens[inst.ID] = e.tokV.Value
		}
	}
	for _, d := range defaults {
		if !seen[d.ID] {
			cfg.Instances = append(cfg.Instances, d)
		}
	}
	return cfg
}

func parseInstance(v string) (InstanceConfig, error) {
	f := strings.Fields(v)
	if len(f) != 2 {
		return InstanceConfig{}, fmt.Errorf("expected \"<kind> <https URL>\", like \"gitlab https://git.acme.dev\"")
	}
	kind := corehosts.Kind(strings.ToLower(f[0]))
	if kind.Validate() != nil {
		return InstanceConfig{}, fmt.Errorf("the kind must be gitlab, github or gitea")
	}
	u, err := url.Parse(f[1])
	if err != nil || u.Host == "" {
		return InstanceConfig{}, fmt.Errorf("the base URL needs a scheme and a host")
	}
	host := strings.ToLower(u.Hostname())
	switch u.Scheme {
	case "https":
	case "http":
		if host != "localhost" && host != "127.0.0.1" {
			return InstanceConfig{}, fmt.Errorf("the base URL must be https (http is only accepted for localhost)")
		}
	default:
		return InstanceConfig{}, fmt.Errorf("the base URL must be https")
	}
	if u.User != nil || u.RawQuery != "" || u.Fragment != "" || strings.Contains(host, ":") || host == "" {
		return InstanceConfig{}, fmt.Errorf("the base URL must be only scheme, host, optional port and path")
	}
	id := host
	if p := u.Port(); p != "" {
		if _, err := parsePort(p); err != nil {
			return InstanceConfig{}, fmt.Errorf("the port is not valid")
		}
		id += ":" + p
	}
	base := u.Scheme + "://" + id + strings.TrimRight(u.EscapedPath(), "/")
	instID := corehosts.InstanceID(id)
	if instID.Validate() != nil {
		return InstanceConfig{}, fmt.Errorf("the host name is not valid")
	}
	return InstanceConfig{ID: instID, Kind: kind, Base: base}, nil
}

// FormatConfig writes cfg as .env text that ParseConfig reads back to the same
// instances (defaults that were added are written too). Tokens are not
// written unless withTokens is set.
func FormatConfig(cfg Config, withTokens bool) string {
	var b strings.Builder
	for i, in := range cfg.Instances {
		fmt.Fprintf(&b, "YGG_HOST_%d=%s %s\n", i+1, in.Kind, in.Base)
		if t := cfg.tokens[in.ID]; withTokens && t != "" {
			fmt.Fprintf(&b, "YGG_HOST_%d_TOKEN=%s\n", i+1, t)
		}
	}
	return b.String()
}

// EnvPath returns the file the config is read from: YGG_ENV_FILE if set,
// otherwise <dataDir>/.env. A .env in a clone folder is never read, because
// it could be committed.
func EnvPath(dataDir string, lookup func(string) (string, bool)) string {
	if lookup == nil {
		lookup = os.LookupEnv
	}
	if p, ok := lookup("YGG_ENV_FILE"); ok && strings.TrimSpace(p) != "" {
		return filepath.Clean(strings.TrimSpace(p))
	}
	return filepath.Join(dataDir, EnvFileName)
}

// LoadConfig reads the .env file (missing is fine) and the named process
// variables YGG_HOST_n and YGG_HOST_n_TOKEN (they win over the file). It reads
// only those names, never the whole environment.
func LoadConfig(path string, lookup func(string) (string, bool)) (Config, error) {
	if lookup == nil {
		lookup = os.LookupEnv
	}
	var vars map[string]Var
	var probs []Problem
	data, err := os.ReadFile(path) //nolint:gosec // the path is the .env file in Yggdrasil's data dir, or YGG_ENV_FILE set by the user
	switch {
	case err == nil:
		vars, probs = ParseEnvText(string(data))
	case os.IsNotExist(err):
		vars = map[string]Var{}
	default:
		return Config{}, fmt.Errorf("read the host config %s: %w", path, err)
	}
	for n := 1; n <= maxHostNumber; n++ {
		for _, name := range []string{fmt.Sprintf("YGG_HOST_%d", n), fmt.Sprintf("YGG_HOST_%d_TOKEN", n)} {
			if v, ok := lookup(name); ok {
				vars[name] = Var{Value: v}
			}
		}
	}
	cfg := ParseConfig(vars)
	cfg.Problems = append(probs, cfg.Problems...)
	return cfg, nil
}
