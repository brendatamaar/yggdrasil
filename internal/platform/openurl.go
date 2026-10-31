package platform

import (
	"errors"
	"net/url"
	"strconv"
	"strings"
)

// errBadURL is deliberately free of the URL: a launch link carries the session
// token (docs/AGENTS.md section 6, invariant 4).
var errBadURL = errors.New("platform: only http://127.0.0.1:<port>/ links can be opened in the browser")

// launchProc starts a program outside ygg's job; tests replace it.
var launchProc = launchOutsideJob

// OpenURL opens a loopback link (http://127.0.0.1:<port>/...) in the default
// browser, outside ygg's job so closing ygg leaves the browser open (PL-05).
// No shell string is built: the link is one argument of the opener. Neither
// the link nor any part of it appears in a returned error.
func OpenURL(raw string) error {
	if err := checkLoopbackURL(raw); err != nil {
		return err
	}
	exe, argv, env, err := urlOpener(raw)
	if err != nil {
		return redactErr(err, raw)
	}
	if err := launchProc(exe, argv, env); err != nil {
		return redactErr(err, raw)
	}
	return nil
}

// redactErr returns an error whose text holds no part of the link.
func redactErr(err error, raw string) error {
	msg := err.Error()
	u, perr := url.Parse(raw)
	for _, s := range []string{raw, u.RawQuery} {
		if perr == nil && s != "" {
			msg = strings.ReplaceAll(msg, s, "[link]")
		}
	}
	return errors.New("platform: could not open the browser: " + msg)
}

// checkLoopbackURL accepts http://127.0.0.1:<port>[/path][?query][#frag] only.
func checkLoopbackURL(raw string) error {
	if raw == "" {
		return errBadURL
	}
	for i := 0; i < len(raw); i++ {
		if c := raw[i]; c <= ' ' || c == 0x7f || c == '\\' || c >= 0x80 {
			return errBadURL
		}
	}
	u, err := url.Parse(raw)
	if err != nil || !strings.HasPrefix(raw, "http://") {
		return errBadURL
	}
	if u.Scheme != "http" || u.User != nil || u.Opaque != "" || u.Hostname() != "127.0.0.1" {
		return errBadURL
	}
	port, err := strconv.Atoi(u.Port())
	if err != nil || port < 1 || port > 65535 || u.Port() != strconv.Itoa(port) {
		return errBadURL
	}
	if u.Path != "" && u.Path[0] != '/' {
		return errBadURL
	}
	return nil
}
