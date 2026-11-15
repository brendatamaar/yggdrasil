package hosts

import (
	"context"
	"encoding/base64"
	"net/http"
	"net/url"
	"strings"
	"unicode"
	"unicode/utf8"

	corehosts "github.com/brendatamaar/yggdrasil/internal/core/hosts"
)

var _ PushAuthorizer = (*ConfirmedHosts)(nil)

// Authorize adds credentials only to a safe request to the configured HTTPS
// clone origin. The API origin (api.github.com) is deliberately insufficient.
// The publish transport must disable redirects or reauthorize each destination.
func (h *ConfirmedHosts) Authorize(ctx context.Context, id corehosts.InstanceID, req *http.Request) error {
	if err := ctx.Err(); err != nil {
		return err
	}
	inst, token, err := h.instance(id)
	if err != nil {
		return err
	}
	if inst.Kind != corehosts.KindGitHub {
		return ErrContextUnsupported
	}
	if !validPushDestination(inst.Base, req) {
		return ErrContextScope
	}
	if strings.ContainsFunc(token, func(r rune) bool { return r <= ' ' || r == 0x7f }) {
		return NeedToken()
	}
	if req.Header == nil {
		req.Header = make(http.Header)
	}
	req.Header.Set("Authorization", "Basic "+base64.StdEncoding.EncodeToString([]byte("x-access-token:"+token)))
	return nil
}

func validPushDestination(base string, req *http.Request) bool {
	if req == nil || req.URL == nil || req.RequestURI != "" {
		return false
	}
	own, err := url.Parse(base)
	if err != nil || own.Scheme != "https" || own.Host == "" || own.User != nil || own.RawQuery != "" || own.Fragment != "" {
		return false
	}
	u := req.URL
	if u.Scheme != "https" || u.Host != own.Host || u.User != nil || u.Opaque != "" || u.OmitHost || u.Fragment != "" || u.RawFragment != "" || u.ForceQuery {
		return false
	}
	if req.Host != "" && req.Host != u.Host {
		return false
	}
	parsed, err := url.Parse(u.String())
	if err != nil || parsed.Host != own.Host || parsed.Scheme != "https" {
		return false
	}
	if !utf8.ValidString(u.Path) || !strings.HasPrefix(u.Path, "/") || strings.ContainsFunc(u.Path, func(r rune) bool { return unicode.IsControl(r) || unicode.IsSpace(r) || r == '\\' || r == '%' }) {
		return false
	}
	if u.RawPath != "" && u.EscapedPath() != u.RawPath {
		return false
	}
	for _, segment := range strings.Split(u.Path, "/") {
		if segment == "." || segment == ".." {
			return false
		}
	}
	// A configured deployment prefix must remain in scope too.
	prefix := strings.TrimRight(own.Path, "/")
	if prefix != "" && !strings.HasPrefix(u.Path, prefix+"/") {
		return false
	}
	if u.RawQuery != "" {
		q, err := url.ParseQuery(u.RawQuery)
		if err != nil || len(q) != 1 || len(q["service"]) != 1 {
			return false
		}
		service := q.Get("service")
		if service != "git-receive-pack" && service != "git-upload-pack" {
			return false
		}
	}
	return true
}

// NewPushTransport checks every outgoing request, including redirect requests
// made by http.Client, before the network sees it. A foreign redirect therefore
// fails authorization even when a caller uses the default redirect policy.
func NewPushTransport(authorizer PushAuthorizer, instance corehosts.InstanceID, base http.RoundTripper) http.RoundTripper {
	if base == nil {
		base = http.DefaultTransport
	}
	return pushTransport{authorizer: authorizer, instance: instance, base: base}
}

type pushTransport struct {
	authorizer PushAuthorizer
	instance   corehosts.InstanceID
	base       http.RoundTripper
}

func (p pushTransport) RoundTrip(req *http.Request) (*http.Response, error) {
	if req == nil {
		return nil, ErrContextScope
	}
	if p.authorizer == nil {
		if req.Body != nil {
			_ = req.Body.Close()
		}
		return nil, ErrContextScope
	}
	outgoing := req.Clone(req.Context())
	if err := p.authorizer.Authorize(outgoing.Context(), p.instance, outgoing); err != nil {
		if outgoing.Body != nil {
			_ = outgoing.Body.Close()
		}
		return nil, err
	}
	return p.base.RoundTrip(outgoing)
}
