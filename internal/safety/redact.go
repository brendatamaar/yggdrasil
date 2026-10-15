package safety

import (
	"context"
	"fmt"
	"log/slog"
	"regexp"
	"slices"
	"strings"
)

// Redacted replaces every secret found in text.
const Redacted = "[REDACTED]"

var (
	// "Bearer abc", "Basic dXNlcjpwYXNz". "Basic" needs 8+ characters so
	// ordinary prose such as "Basic use" stays readable.
	reBearer = regexp.MustCompile(`(?i)\b(Bearer\s+)[A-Za-z0-9._~+/=-]+`)
	reBasic  = regexp.MustCompile(`(\bBasic\s+)[A-Za-z0-9+/=]{8,}`)
	// scheme://user:password@host
	reURLCreds = regexp.MustCompile(`([A-Za-z][A-Za-z0-9+.-]*://)[^\s/@:"']+:[^\s/@"']*@`)
	// ghp_, gho_, ghu_, ghs_, ghr_, github_pat_, glpat-, sk-ant-
	reTokens = regexp.MustCompile(`\b(?:gh[pousr]_[A-Za-z0-9]{10,}|github_pat_[A-Za-z0-9_]{10,}|glpat-[A-Za-z0-9_-]{10,}|sk-ant-[A-Za-z0-9_-]{10,})`)
	// token=..., "password": "...", GITHUB_TOKEN=..., api_key: ...
	rePairs = regexp.MustCompile(`(?i)([A-Za-z0-9_.-]*(?:token|password|passwd|secret|api[_-]?key)["']?\s*[:=]\s*)("[^"]*"|'[^']*'|[^\s&,;"']+)`)
)

type redactor struct {
	secrets []string
}

// NewRedactor returns a Redactor that also masks the exact values in
// secrets, such as the tokens Yggdrasil holds. Empty values are ignored.
func NewRedactor(secrets ...string) Redactor {
	s := slices.DeleteFunc(slices.Clone(secrets), func(v string) bool { return v == "" })
	// Longest first, so a secret that contains another one is masked whole.
	slices.SortFunc(s, func(a, b string) int { return len(b) - len(a) })
	return redactor{secrets: s}
}

// Redact implements Redactor.
func (r redactor) Redact(s string) string {
	for _, sec := range r.secrets {
		s = strings.ReplaceAll(s, sec, Redacted)
	}
	s = reBearer.ReplaceAllString(s, "${1}"+Redacted)
	s = reBasic.ReplaceAllString(s, "${1}"+Redacted)
	s = reURLCreds.ReplaceAllString(s, "${1}"+Redacted+"@")
	s = reTokens.ReplaceAllString(s, Redacted)
	return rePairs.ReplaceAllStringFunc(s, func(m string) string {
		sub := rePairs.FindStringSubmatch(m)
		// Keep the quotes so redacted JSON stays valid.
		switch sub[2][0] {
		case '"':
			return sub[1] + `"` + Redacted + `"`
		case '\'':
			return sub[1] + `'` + Redacted + `'`
		}
		return sub[1] + Redacted
	})
}

// logHandler redacts the message and the string attributes of every record
// before the wrapped handler sees them (invariant 4).
type logHandler struct {
	next slog.Handler
	r    Redactor
}

// NewLogHandler wraps next so that log output never holds a token.
func NewLogHandler(next slog.Handler, r Redactor) slog.Handler {
	return logHandler{next: next, r: r}
}

func (h logHandler) Enabled(ctx context.Context, l slog.Level) bool { return h.next.Enabled(ctx, l) }

func (h logHandler) Handle(ctx context.Context, rec slog.Record) error {
	out := slog.NewRecord(rec.Time, rec.Level, h.r.Redact(rec.Message), rec.PC)
	rec.Attrs(func(a slog.Attr) bool {
		out.AddAttrs(h.attr(a))
		return true
	})
	return h.next.Handle(ctx, out)
}

func (h logHandler) WithAttrs(attrs []slog.Attr) slog.Handler {
	red := make([]slog.Attr, len(attrs))
	for i, a := range attrs {
		red[i] = h.attr(a)
	}
	return logHandler{next: h.next.WithAttrs(red), r: h.r}
}

func (h logHandler) WithGroup(name string) slog.Handler {
	return logHandler{next: h.next.WithGroup(name), r: h.r}
}

func (h logHandler) attr(a slog.Attr) slog.Attr {
	a.Value = a.Value.Resolve()
	if looksSecret(a.Key) && a.Value.Kind() != slog.KindGroup {
		return slog.String(a.Key, Redacted)
	}
	switch a.Value.Kind() {
	case slog.KindString:
		a.Value = slog.StringValue(h.r.Redact(a.Value.String()))
	case slog.KindGroup:
		g := a.Value.Group()
		red := make([]slog.Attr, len(g))
		for i, ga := range g {
			red[i] = h.attr(ga)
		}
		a.Value = slog.GroupValue(red...)
	case slog.KindAny:
		switch v := a.Value.Any().(type) {
		case error:
			a.Value = slog.StringValue(h.r.Redact(v.Error()))
		case fmt.Stringer:
			a.Value = slog.StringValue(h.r.Redact(v.String()))
		}
	default:
	}
	return a
}
