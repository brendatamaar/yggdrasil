package core

import (
	"errors"
	"strings"
	"unicode/utf8"
)

// MaxHostInstanceLen is the one length limit, in bytes, of a git host instance
// ("git.acme.test:8443") wherever one is accepted: the API boundary, MCP
// arguments, proposals and the new-repository flow.
const MaxHostInstanceLen = 255

// ErrInvalidHostInstance is returned by ValidateHostInstance.
var ErrInvalidHostInstance = errors.New("invalid host instance")

// ValidateHostInstance is the single syntax rule for a host instance:
// host[:port], lower case, valid UTF-8, at most MaxHostInstanceLen bytes. The
// port is optional and, when given, a decimal number from 1 to 65535. It
// refuses credentials ("me@host"), paths, query strings, schemes
// ("https://host"), whitespace and control characters. The caller wraps the
// error into its own plain-language message.
func ValidateHostInstance(v string) error {
	if v == "" || len(v) > MaxHostInstanceLen || v != strings.ToLower(v) || !utf8.ValidString(v) ||
		strings.ContainsFunc(v, func(c rune) bool {
			return c <= ' ' || c == 0x7f || (c >= 0x80 && c <= 0x9f) || c == '/' || c == '\\' || c == '@' || c == '?' || c == '#'
		}) {
		return ErrInvalidHostInstance
	}
	host, port, hasPort := v, "", false
	if strings.HasPrefix(v, "[") {
		// A bracketed IPv6 literal, optionally followed by :port.
		j := strings.Index(v, "]")
		if j < 0 {
			return ErrInvalidHostInstance
		}
		host, port = v[:j+1], v[j+1:]
		if port != "" {
			if port[0] != ':' {
				return ErrInvalidHostInstance
			}
			port, hasPort = port[1:], true
		}
		if host == "[]" {
			return ErrInvalidHostInstance
		}
	} else if i := strings.Index(v, ":"); i >= 0 {
		host, port, hasPort = v[:i], v[i+1:], true
	}
	if host == "" || (!strings.HasPrefix(host, "[") && strings.ContainsAny(host, ":[]")) {
		return ErrInvalidHostInstance
	}
	if hasPort && !validPort(port) {
		return ErrInvalidHostInstance
	}
	return nil
}

func validPort(p string) bool {
	if p == "" || len(p) > 5 {
		return false
	}
	n := 0
	for _, c := range p {
		if c < '0' || c > '9' {
			return false
		}
		n = n*10 + int(c-'0')
	}
	return n >= 1 && n <= 65535
}
