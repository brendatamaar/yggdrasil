package changesets

import (
	"strings"
	"unicode/utf8"
)

// maxURLBytes caps a link in a description.
const maxURLBytes = 2048

// safeWebURL is the local equivalent of core/proposals.SafeWebURL (domains do
// not import each other), plus a length cap and no angle brackets, so a link
// cannot break out of the one-line description: http or https, a host, no
// userinfo, no spaces, control characters or backslashes.
func safeWebURL(u string) bool {
	if u == "" || len(u) > maxURLBytes || !utf8.ValidString(u) {
		return false
	}
	for _, r := range u {
		if r < 0x20 || r == 0x7f || (r >= 0x80 && r < 0xa0) || r == ' ' || r == '\\' || r == '<' || r == '>' || isLineBreak(r) {
			return false
		}
	}
	scheme, authority, ok := splitAuthority(u)
	if !ok {
		return false
	}
	scheme = strings.ToLower(scheme)
	if (scheme != "http" && scheme != "https") || strings.Contains(authority, "@") {
		return false
	}
	return validHostPort(authority)
}

func splitAuthority(u string) (scheme, authority string, ok bool) {
	i := strings.Index(u, "://")
	if i <= 0 {
		return "", "", false
	}
	scheme, tail := u[:i], u[i+3:]
	if end := strings.IndexAny(tail, "/?#"); end >= 0 {
		tail = tail[:end]
	}
	return scheme, tail, true
}

func validHostPort(hp string) bool {
	if hp == "" {
		return false
	}
	host, port := hp, ""
	if strings.HasPrefix(hp, "[") {
		j := strings.Index(hp, "]")
		if j < 0 || (hp[j+1:] != "" && !strings.HasPrefix(hp[j+1:], ":")) || hp[j+1:] == ":" {
			return false
		}
		host, port = hp[:j+1], strings.TrimPrefix(hp[j+1:], ":")
	} else if i := strings.LastIndex(hp, ":"); i >= 0 {
		host, port = hp[:i], hp[i+1:]
		if port == "" {
			return false
		}
	}
	if host == "" || host == "[]" || strings.HasPrefix(host, "-") {
		return false
	}
	for _, c := range port {
		if c < '0' || c > '9' {
			return false
		}
	}
	if strings.HasPrefix(host, "[") {
		return true
	}
	for _, c := range host {
		ok := c >= 'a' && c <= 'z' || c >= 'A' && c <= 'Z' || c >= '0' && c <= '9' || c == '.' || c == '-' || c == '_'
		if !ok {
			return false
		}
	}
	return true
}

// hostName returns the lower-case host of "host", "host:port", "[v6]" or
// "[v6]:port".
func hostName(hp string) string {
	if strings.HasPrefix(hp, "[") {
		if j := strings.Index(hp, "]"); j >= 0 {
			return strings.ToLower(hp[:j+1])
		}
		return ""
	}
	if i := strings.LastIndex(hp, ":"); i >= 0 {
		hp = hp[:i]
	}
	return strings.ToLower(hp)
}

// linkURLOK reports whether u may appear in a description: a safe web URL on
// the remote's host.
func linkURLOK(u, remoteHost string) bool {
	if !safeWebURL(u) {
		return false
	}
	_, authority, _ := splitAuthority(u)
	h := hostName(remoteHost)
	return h != "" && hostName(authority) == h
}
