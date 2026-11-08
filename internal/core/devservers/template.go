package devservers

import (
	"errors"
	"fmt"
	"maps"
	"slices"
	"strconv"
	"strings"
	"unicode"

	"github.com/brendatamaar/yggdrasil/internal/core"
)

// Limits of a template.
const (
	MaxArgs   = 64
	MaxEnv    = 64
	maxArgLen = 4096
)

// Environment names the core builds for a block.
const (
	EnvPort     = "PORT"
	EnvPortBase = "YGG_PORT_BASE"
	envPortN    = "YGG_PORT_"
)

// ErrBadTemplate is wrapped by every template rejection.
var ErrBadTemplate = errors.New("bad dev server command")

func badTemplate(format string, a ...any) error {
	return fmt.Errorf("%w: %s", ErrBadTemplate, fmt.Sprintf(format, a...))
}

// placeholder is one {port} or {port:n} found in an argument.
type placeholder struct {
	start, end int // byte range in the argument
	index      int // 0 for {port}
}

// scanPlaceholders finds the placeholders of arg. A group like {name} that is
// neither {port} nor {port:n} is an error; text with other braces (JSON,
// shell globs with a comma) is left alone.
func scanPlaceholders(arg string) ([]placeholder, error) {
	var out []placeholder
	for i := 0; i < len(arg); i++ {
		if arg[i] != '{' {
			continue
		}
		j := strings.IndexByte(arg[i:], '}')
		if j < 0 {
			continue
		}
		body := arg[i+1 : i+j]
		if !wordLike(body) || (i > 0 && arg[i-1] == '$') { // ${HOME} is the shell's
			continue
		}
		p := placeholder{start: i, end: i + j + 1}
		switch {
		case body == "port":
		case strings.HasPrefix(body, "port:"):
			digits := body[len("port:"):]
			n, err := strconv.Atoi(digits)
			if err != nil || digits == "" || (len(digits) > 1 && digits[0] == '0') || n < 0 || n >= core.PortBlockSize {
				return nil, badTemplate("%s needs a number from 0 to %d", arg[p.start:p.end], core.PortBlockSize-1)
			}
			p.index = n
		default:
			return nil, badTemplate("unknown placeholder %s (use {port} or {port:n})", arg[p.start:p.end])
		}
		out = append(out, p)
		i = p.end - 1
	}
	return out, nil
}

// wordLike is true for text such as "port", "host" or "port:1": letters,
// digits, '_' and ':' starting with a letter or '_'.
func wordLike(s string) bool {
	if s == "" {
		return false
	}
	for i, r := range s {
		switch {
		case r == '_' || r >= 'a' && r <= 'z' || r >= 'A' && r <= 'Z':
		case i > 0 && (r == ':' || r >= '0' && r <= '9'):
		default:
			return false
		}
	}
	return true
}

func hasControl(s string) bool {
	return strings.ContainsFunc(s, func(r rune) bool { return r < 0x20 || r == 0x7f || unicode.Is(unicode.Cc, r) })
}

var secretMarkers = []string{"TOKEN", "SECRET", "PASSWORD", "PASSWD", "API_KEY", "APIKEY", "CREDENTIAL", "PRIVATE_KEY"}

func secretName(name string) bool {
	up := strings.ReplaceAll(strings.ToUpper(name), "-", "_")
	if strings.HasSuffix(up, "_KEY") {
		return true
	}
	for _, m := range secretMarkers {
		if strings.Contains(up, m) {
			return true
		}
	}
	return false
}

func envNameOK(name string) bool {
	if name == "" {
		return false
	}
	for i, r := range name {
		if r != '_' && (r < 'A' || r > 'Z') && (r < 'a' || r > 'z') && (i == 0 || r < '0' || r > '9') {
			return false
		}
	}
	return true
}

func reservedEnv(name string) bool {
	up := strings.ToUpper(name)
	return up == EnvPort || up == EnvPortBase || strings.HasPrefix(up, envPortN)
}

// ParseTemplate checks the argv (from the toolchain or the repo override) and
// the extra environment of a dev server command and returns a Template. It
// rejects: an empty argv; more than 64 arguments; an empty program, or a
// {port} placeholder in Argv[0]; NUL or control characters; an unknown
// placeholder; {port:n} with n outside the block size; environment names that
// are invalid, reserved for the port variables, or token-like.
func ParseTemplate(argv []string, env map[string]string) (Template, error) {
	if len(argv) == 0 {
		return Template{}, badTemplate("the command is empty")
	}
	if len(argv) > MaxArgs {
		return Template{}, badTemplate("the command has more than %d arguments", MaxArgs)
	}
	if strings.TrimSpace(argv[0]) == "" {
		return Template{}, badTemplate("the program name is empty")
	}
	for i, a := range argv {
		if len(a) > maxArgLen {
			return Template{}, badTemplate("argument %d is too long", i)
		}
		if hasControl(a) {
			return Template{}, badTemplate("argument %d has a control character", i)
		}
		ph, err := scanPlaceholders(a)
		if err != nil {
			return Template{}, err
		}
		if i == 0 && len(ph) > 0 {
			return Template{}, badTemplate("the program name cannot hold a port placeholder")
		}
	}
	if len(env) > MaxEnv {
		return Template{}, badTemplate("more than %d environment variables", MaxEnv)
	}
	for k, v := range env {
		switch {
		case !envNameOK(k):
			return Template{}, badTemplate("environment name %q is not valid", k)
		case reservedEnv(k):
			return Template{}, badTemplate("%s is set by Yggdrasil from the port block", k)
		case secretName(k):
			return Template{}, badTemplate("%s looks like a token and is not passed to dev servers", k)
		case hasControl(v) || len(v) > maxArgLen:
			return Template{}, badTemplate("the value of %s is not valid", k)
		}
	}
	t := Template{Argv: slices.Clone(argv)}
	if len(env) > 0 {
		t.Env = maps.Clone(env)
	}
	return t, nil
}

// Format is the inverse of ParseTemplate: ParseTemplate(Format(t)) equals t.
func Format(t Template) (argv []string, env map[string]string) {
	argv = slices.Clone(t.Argv)
	if len(t.Env) > 0 {
		env = maps.Clone(t.Env)
	}
	return argv, env
}

// Expand fills the placeholders and builds the environment for block (D8,
// D11). Every port it writes lies inside the block. With an empty block (a
// main checkout) nothing is injected, and a template that holds a placeholder
// is refused. Env holds the template's own variables plus, for a block, PORT,
// YGG_PORT_BASE and YGG_PORT_0..n; the core never adds any other name.
func Expand(t Template, block core.PortBlock) (Expanded, error) {
	if !block.Empty() {
		if err := block.Validate(); err != nil {
			return Expanded{}, badTemplate("the worktree has no valid port block")
		}
	}
	out := Expanded{Argv: make([]string, len(t.Argv)), Env: map[string]string{}}
	for i, a := range t.Argv {
		ph, err := scanPlaceholders(a)
		if err != nil {
			return Expanded{}, err
		}
		if len(ph) > 0 && i == 0 {
			return Expanded{}, badTemplate("the program name cannot hold a port placeholder")
		}
		var b strings.Builder
		prev := 0
		for _, p := range ph {
			if block.Empty() {
				return Expanded{}, badTemplate("%s needs a port block, and this folder has none", a[p.start:p.end])
			}
			if p.index >= block.Size {
				return Expanded{}, badTemplate("%s is outside the port block of %d ports", a[p.start:p.end], block.Size)
			}
			b.WriteString(a[prev:p.start])
			b.WriteString(strconv.Itoa(block.Base + p.index))
			prev = p.end
		}
		b.WriteString(a[prev:])
		out.Argv[i] = b.String()
	}
	maps.Copy(out.Env, t.Env)
	if !block.Empty() {
		out.Env[EnvPort] = strconv.Itoa(block.Base)
		out.Env[EnvPortBase] = strconv.Itoa(block.Base)
		for i := 0; i < block.Size; i++ {
			out.Env[envPortN+strconv.Itoa(i)] = strconv.Itoa(block.Base + i)
		}
	}
	return out, nil
}
