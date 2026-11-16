package git

import (
	"errors"
	"fmt"
	"net/url"
	"strconv"
	"strings"
)

// ErrCredentialURL is returned for a remote URL that carries, or could carry, a
// credential: userinfo, a query string, a fragment or an "@". Yggdrasil never
// writes such a URL anywhere and never echoes one (invariant 4).
var ErrCredentialURL = errors.New("remote URL must not carry credentials")

// ErrBadRemoteURL is returned for a remote URL that is not a plain, canonical
// HTTPS URL of a repository.
var ErrBadRemoteURL = errors.New("remote URL is not a valid https repository URL")

const maxRemoteURL = 2048

// ValidateRemoteURL parses outside data into the one shape the new-repo
// helpers accept: https://host[:port]/path with no userinfo, query, fragment,
// "@", dot segments, empty segments, whitespace, control characters or
// backslashes. It returns the canonical form (lower-case host). Accepted input
// always parses back to itself: ValidateRemoteURL(canonical) == canonical.
//
// The error never contains the input, so a token typed into a URL is not echoed.
func ValidateRemoteURL(raw string) (string, error) {
	if raw == "" || len(raw) > maxRemoteURL {
		return "", fmt.Errorf("%w: wrong length", ErrBadRemoteURL)
	}
	for i := 0; i < len(raw); i++ {
		c := raw[i]
		switch {
		case c <= 0x20 || c == 0x7f || c >= 0x80:
			return "", fmt.Errorf("%w: control, space or non-ASCII character", ErrBadRemoteURL)
		case c == '\\':
			return "", fmt.Errorf("%w: backslash", ErrBadRemoteURL)
		case c == '@' || c == '?' || c == '#':
			return "", fmt.Errorf("%w: userinfo, query or fragment", ErrCredentialURL)
		}
	}
	u, err := url.Parse(raw)
	if err != nil {
		return "", fmt.Errorf("%w: unparsable", ErrBadRemoteURL)
	}
	if u.User != nil || u.RawQuery != "" || u.ForceQuery || u.Fragment != "" || u.RawFragment != "" {
		return "", fmt.Errorf("%w", ErrCredentialURL)
	}
	if !strings.EqualFold(u.Scheme, "https") || u.Opaque != "" {
		return "", fmt.Errorf("%w: only https is allowed", ErrBadRemoteURL)
	}
	host := strings.ToLower(u.Hostname())
	if host == "" || strings.ContainsAny(host, "%[]") || strings.HasPrefix(host, ".") || strings.HasPrefix(host, "-") {
		return "", fmt.Errorf("%w: host", ErrBadRemoteURL)
	}
	if p := u.Port(); p != "" {
		n, err := strconv.Atoi(p)
		if err != nil || n < 1 || n > 65535 || strconv.Itoa(n) != p {
			return "", fmt.Errorf("%w: port", ErrBadRemoteURL)
		}
	} else if strings.HasSuffix(u.Host, ":") {
		return "", fmt.Errorf("%w: port", ErrBadRemoteURL)
	}
	path := u.EscapedPath()
	if !strings.HasPrefix(path, "/") || len(path) < 2 || strings.HasSuffix(path, "/") {
		return "", fmt.Errorf("%w: path", ErrBadRemoteURL)
	}
	for seg := range strings.SplitSeq(path[1:], "/") {
		dec, err := url.PathUnescape(seg)
		if err != nil || seg == "" || dec == "" || dec == "." || dec == ".." ||
			strings.ContainsAny(dec, "/\\?#@:") || strings.EqualFold(dec, ".git") {
			return "", fmt.Errorf("%w: path segment", ErrBadRemoteURL)
		}
		for i := 0; i < len(dec); i++ {
			if dec[i] <= 0x20 || dec[i] == 0x7f {
				return "", fmt.Errorf("%w: path segment", ErrBadRemoteURL)
			}
		}
	}
	canon := "https://" + strings.ToLower(u.Host) + path
	// Canonical form must be stable.
	if u2, err := url.Parse(canon); err != nil || u2.EscapedPath() != path || strings.ToLower(u2.Host) != strings.ToLower(u.Host) {
		return "", fmt.Errorf("%w: not canonical", ErrBadRemoteURL)
	}
	return canon, nil
}
