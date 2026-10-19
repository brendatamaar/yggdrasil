package hosts

import (
	"errors"
	"fmt"
	"net/url"
	"strings"
	"unicode"
	"unicode/utf8"

	corehosts "github.com/brendatamaar/yggdrasil/internal/core/hosts"
)

// RemoteRef is a git remote URL, parsed. It never holds credentials: the
// parser drops them and only sets HadCredentials, a health flag.
type RemoteRef struct {
	// Scheme is "https", "http" (localhost only) or "ssh" (ssh://, git+ssh://
	// and the scp-like form).
	Scheme string
	// Host is lower case.
	Host string
	// Port is the explicit port, or 0.
	Port int
	// Project is "group/sub/repo" without ".git" and without slashes at the ends.
	Project corehosts.ProjectPath
	// HadCredentials: the URL held a user and password, or a user name in an
	// https URL (use a credential helper instead).
	HadCredentials bool
}

// InstanceID is the ID of the instance this remote points at. For web URLs the
// port is part of it; an ssh port is not the API's port, so it is left out.
func (r RemoteRef) InstanceID() corehosts.InstanceID {
	if (r.Scheme == "https" || r.Scheme == "http") && r.Port != 0 && !(r.Scheme == "https" && r.Port == 443) && !(r.Scheme == "http" && r.Port == 80) {
		return corehosts.InstanceID(fmt.Sprintf("%s:%d", r.Host, r.Port))
	}
	return corehosts.InstanceID(r.Host)
}

// Format writes the remote as a URL: "https://host[:port]/project.git" or
// "ssh://git@host[:port]/project.git". ParseRemote(Format(r)) == r (without
// credentials).
func (r RemoteRef) Format() string {
	host := r.Host
	if r.Port != 0 {
		host = fmt.Sprintf("%s:%d", host, r.Port)
	}
	user := ""
	if r.Scheme == "ssh" {
		user = "git@"
	}
	return r.Scheme + "://" + user + host + "/" + string(r.Project) + ".git"
}

// FormatSCP writes the scp-like form "git@host:project.git" (ssh, no port).
func (r RemoteRef) FormatSCP() string {
	return "git@" + r.Host + ":" + string(r.Project) + ".git"
}

// ErrRemote is wrapped by every ParseRemote rejection. Its text never holds
// the raw URL, which can carry credentials.
var ErrRemote = errors.New("unsupported git remote")

func remoteErr(reason string) error { return fmt.Errorf("%w: %s", ErrRemote, reason) }

// ParseRemote parses the URL of a git remote (boundary parser). It accepts
// https (and http for localhost), ssh://, git+ssh:// and scp-like
// user@host:path, and rejects file paths, other schemes, empty or ".." project
// paths.
func ParseRemote(raw string) (RemoteRef, error) {
	raw = strings.TrimSpace(raw)
	if raw == "" {
		return RemoteRef{}, remoteErr("the remote is empty")
	}
	if strings.ContainsFunc(raw, func(r rune) bool { return r <= ' ' || r == 0x7f }) {
		return RemoteRef{}, remoteErr("it contains spaces or control characters")
	}
	if strings.Contains(raw, "://") {
		return parseURLRemote(raw)
	}
	return parseSCPRemote(raw)
}

func parseURLRemote(raw string) (RemoteRef, error) {
	u, err := url.Parse(raw)
	if err != nil {
		return RemoteRef{}, remoteErr("it is not a valid URL")
	}
	ref := RemoteRef{Host: strings.ToLower(u.Hostname())}
	switch strings.ToLower(u.Scheme) {
	case "https":
		ref.Scheme = "https"
	case "http":
		if ref.Host != "localhost" && ref.Host != "127.0.0.1" {
			return RemoteRef{}, remoteErr("plain http is only accepted for localhost")
		}
		ref.Scheme = "http"
	case "ssh", "git+ssh":
		ref.Scheme = "ssh"
	case "file":
		return RemoteRef{}, remoteErr("it is a local path")
	default:
		return RemoteRef{}, remoteErr("the scheme is not https or ssh")
	}
	if !validHost(ref.Host) {
		return RemoteRef{}, remoteErr("it has no usable host")
	}
	if u.Port() != "" {
		p, err := parsePort(u.Port())
		if err != nil {
			return RemoteRef{}, err
		}
		ref.Port = p
	}
	if u.User != nil {
		if _, hasPw := u.User.Password(); hasPw || ref.Scheme != "ssh" {
			ref.HadCredentials = true
		}
	}
	if u.RawQuery != "" || u.Fragment != "" || u.ForceQuery {
		return RemoteRef{}, remoteErr("it has a query or fragment")
	}
	proj, err := cleanProject(u.Path)
	if err != nil {
		return RemoteRef{}, err
	}
	ref.Project = proj
	return ref, nil
}

func parsePort(s string) (int, error) {
	n := 0
	for _, c := range s {
		if c < '0' || c > '9' || n > 65535 {
			return 0, remoteErr("the port is not valid")
		}
		n = n*10 + int(c-'0')
	}
	if n < 1 || n > 65535 {
		return 0, remoteErr("the port is not valid")
	}
	return n, nil
}

func parseSCPRemote(raw string) (RemoteRef, error) {
	if strings.HasPrefix(raw, "/") || strings.HasPrefix(raw, ".") || strings.HasPrefix(raw, "~") || strings.HasPrefix(raw, `\`) {
		return RemoteRef{}, remoteErr("it is a local path")
	}
	i := strings.Index(raw, ":")
	if i < 0 {
		return RemoteRef{}, remoteErr("it is not a URL")
	}
	hostPart, path := raw[:i], raw[i+1:]
	// "C:\repo" and "C:/repo" are drive letters, not hosts.
	if len(hostPart) == 1 {
		return RemoteRef{}, remoteErr("it is a local path")
	}
	if strings.Contains(hostPart, "/") || strings.Contains(hostPart, `\`) {
		return RemoteRef{}, remoteErr("it is not a URL")
	}
	host := hostPart
	if at := strings.LastIndex(host, "@"); at >= 0 {
		host = host[at+1:]
	}
	host = strings.ToLower(host)
	if !validHost(host) {
		return RemoteRef{}, remoteErr("it has no valid host")
	}
	proj, err := cleanProject(path)
	if err != nil {
		return RemoteRef{}, err
	}
	return RemoteRef{Scheme: "ssh", Host: host, Project: proj}, nil
}

// cleanProject normalizes a URL path to a project path: "/a/b.git/" -> "a/b".
func cleanProject(p string) (corehosts.ProjectPath, error) {
	if !utf8.ValidString(p) || strings.ContainsFunc(p, func(r rune) bool { return unicode.IsControl(r) || unicode.IsSpace(r) || r == utf8.RuneError }) {
		return "", remoteErr("the project path is not valid")
	}
	p = strings.Trim(p, "/")
	p = strings.TrimSuffix(p, ".git")
	p = strings.Trim(p, "/")
	if p == "" {
		return "", remoteErr("it has no project path")
	}
	segs := strings.Split(p, "/")
	for _, s := range segs {
		if s == "" || s == "." || s == ".." {
			return "", remoteErr("the project path is not valid")
		}
		if strings.ContainsAny(s, `\?#%`) {
			return "", remoteErr("the project path is not valid")
		}
	}
	if len(segs) < 2 {
		return "", remoteErr("the project path needs an owner and a repo name")
	}
	return corehosts.ProjectPath(p), nil
}

// validHost accepts letters, digits, '-', '.' and '_' only: no port, userinfo,
// brackets or URL syntax can hide in a host.
func validHost(h string) bool {
	if h == "" || strings.HasPrefix(h, ".") || strings.HasSuffix(h, ".") {
		return false
	}
	for _, c := range h {
		if !(c >= 'a' && c <= 'z' || c >= '0' && c <= '9' || c == '-' || c == '.' || c == '_') {
			return false
		}
	}
	return true
}
