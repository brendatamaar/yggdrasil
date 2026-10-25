package proposals

import (
	"errors"
	"strings"
	"unicode/utf8"
)

// ErrNoMRURL means the remote's output holds no merge request link.
var ErrNoMRURL = errors.New("no merge request link in the remote's output")

// ErrUnsafeURL means a link or remote URL failed the safety rules.
var ErrUnsafeURL = errors.New("unsafe URL")

func plainText(s string) bool {
	return utf8.ValidString(s) && !hasControl(s) && !strings.ContainsAny(s, " \\")
}

// splitAuthority splits "scheme://authority/rest" at the authority; ok is
// false when there is no "://".
func splitAuthority(u string) (scheme, authority, rest string, ok bool) {
	i := strings.Index(u, "://")
	if i <= 0 {
		return "", "", "", false
	}
	scheme, tail := u[:i], u[i+3:]
	end := strings.IndexAny(tail, "/?#")
	if end < 0 {
		return scheme, tail, "", true
	}
	return scheme, tail[:end], tail[end:], true
}

func validHostPort(hp string) bool {
	if hp == "" {
		return false
	}
	host, port := hp, ""
	if strings.HasPrefix(hp, "[") {
		j := strings.Index(hp, "]")
		if j < 0 {
			return false
		}
		host, port = hp[:j+1], strings.TrimPrefix(hp[j+1:], ":")
		if hp[j+1:] != "" && !strings.HasPrefix(hp[j+1:], ":") {
			return false
		}
	} else if i := strings.LastIndex(hp, ":"); i >= 0 {
		host, port = hp[:i], hp[i+1:]
		if port == "" {
			return false
		}
	}
	if host == "" || host == "[]" {
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

// SafeWebURL accepts only http or https links with a host and no userinfo
// (no user, password or token before the host), no spaces, control characters
// or backslashes.
func SafeWebURL(u string) bool {
	if !plainText(u) {
		return false
	}
	scheme, authority, _, ok := splitAuthority(u)
	if !ok {
		return false
	}
	scheme = strings.ToLower(scheme)
	if scheme != "http" && scheme != "https" {
		return false
	}
	if strings.Contains(authority, "@") {
		return false
	}
	return validHostPort(authority)
}

// ParsePushOutput finds the merge request link GitLab prints after a push with
// merge-request push options ("remote: View merge request for ...:" and the
// link on that line or the next one). The link must pass SafeWebURL.
// ErrNoMRURL means the push output has no such link; the caller then reports
// "pushed; open the MR from the repo page".
func ParsePushOutput(out string) (Result, error) {
	lines := strings.Split(strings.ReplaceAll(out, "\r\n", "\n"), "\n")
	for i, raw := range lines {
		line := strings.TrimSpace(strings.TrimPrefix(strings.TrimSpace(raw), "remote:"))
		if !strings.Contains(strings.ToLower(line), "view merge request") {
			continue
		}
		candidates := []string{line}
		if i+1 < len(lines) {
			candidates = append(candidates, strings.TrimSpace(strings.TrimPrefix(strings.TrimSpace(lines[i+1]), "remote:")))
		}
		for _, c := range candidates {
			for _, f := range strings.Fields(c) {
				f = strings.TrimRight(f, ".,;:")
				if len(f) > 7 && (strings.HasPrefix(strings.ToLower(f), "http://") || strings.HasPrefix(strings.ToLower(f), "https://")) {
					if !SafeWebURL(f) {
						return Result{}, ErrUnsafeURL
					}
					return Result{MRURL: f}, nil
				}
			}
		}
	}
	return Result{}, ErrNoMRURL
}

// Remote is a checked remote URL, split into parts that carry no secret.
type Remote struct {
	// Scheme is "https", "http", "ssh" or "scp" (user@host:path).
	Scheme string
	Host   string
	Path   string
}

func validSSHUser(u string) bool {
	if u == "" {
		return false
	}
	for _, c := range u {
		ok := c >= 'a' && c <= 'z' || c >= 'A' && c <= 'Z' || c >= '0' && c <= '9' || c == '.' || c == '_' || c == '-'
		if !ok {
			return false
		}
	}
	return true
}

// CheckRemote accepts http(s) URLs without any userinfo, ssh URLs whose
// userinfo is a plain user name (no password or token), and scp-style
// user@host:path. A query or fragment is refused because tokens travel there.
// Everything else (file:, ext::, local paths, drives) is refused.
func CheckRemote(raw string) (Remote, error) {
	bad := func() (Remote, error) {
		return Remote{}, ErrUnsafeURL
	}
	if raw == "" || !plainText(raw) || strings.HasPrefix(raw, "-") {
		return bad()
	}
	if scheme, authority, rest, ok := splitAuthority(raw); ok {
		scheme = strings.ToLower(scheme)
		if strings.ContainsAny(rest, "?#") {
			return bad()
		}
		hp := authority
		switch scheme {
		case "http", "https":
			if strings.Contains(authority, "@") {
				return bad()
			}
		case "ssh":
			if i := strings.LastIndex(authority, "@"); i >= 0 {
				if !validSSHUser(authority[:i]) {
					return bad()
				}
				hp = authority[i+1:]
			}
		default:
			return bad()
		}
		if !validHostPort(hp) {
			return bad()
		}
		return Remote{Scheme: scheme, Host: hp, Path: rest}, nil
	}
	// scp-style: user@host:path
	at := strings.Index(raw, "@")
	colon := strings.Index(raw, ":")
	if at <= 0 || colon <= at+1 || strings.Contains(raw, "://") {
		return bad()
	}
	user, host, path := raw[:at], raw[at+1:colon], raw[colon+1:]
	if !validSSHUser(user) || !validHostPort(host) || strings.Contains(host, ":") || path == "" ||
		strings.ContainsAny(path, "?#") {
		return bad()
	}
	return Remote{Scheme: "scp", Host: host, Path: path}, nil
}
