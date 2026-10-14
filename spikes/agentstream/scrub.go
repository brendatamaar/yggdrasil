package agentstream

import (
	"bufio"
	"cmp"
	"fmt"
	"io"
	"regexp"
	"slices"
	"strings"
)

var (
	uuidRe = regexp.MustCompile(`(?i)\b[0-9a-f]{8}-[0-9a-f]{4}-[0-9a-f]{4}-[0-9a-f]{4}-[0-9a-f]{12}\b`)
	// Message, tool-use and request ids. UNVERIFIED prefixes.
	idRe = regexp.MustCompile(`\b(msg|toolu|srvtoolu|req)_[A-Za-z0-9]{6,}\b`)
)

type literal struct{ old, new string }

// Scrubber turns a live capture into a fixture: literal replacements (home
// path, user name, host name, run dir), then session UUIDs and message/tool
// ids replaced by stable placeholders, so parent links still match. Use one
// per capture.
type Scrubber struct {
	lits []literal
	ids  map[string]string
	n    map[string]int
}

// NewScrubber takes old→new literal replacements. Each is also applied in its
// JSON-escaped (\\) and forward-slash forms. Matching is case-sensitive.
func NewScrubber(repl map[string]string) *Scrubber {
	s := &Scrubber{ids: map[string]string{}, n: map[string]int{}}
	seen := map[string]bool{}
	for from, to := range repl {
		if from == "" {
			continue
		}
		for _, f := range []func(string) string{
			func(v string) string { return v },
			func(v string) string { return strings.ReplaceAll(v, `\`, `\\`) },
			func(v string) string { return strings.ReplaceAll(v, `\`, `/`) },
		} {
			if o := f(from); !seen[o] {
				seen[o] = true
				s.lits = append(s.lits, literal{o, f(to)})
			}
		}
	}
	// Longest first, so a path wins over the user name inside it.
	slices.SortFunc(s.lits, func(a, b literal) int {
		return cmp.Or(cmp.Compare(len(b.old), len(a.old)), cmp.Compare(a.old, b.old))
	})
	return s
}

// Line scrubs one line.
func (s *Scrubber) Line(line string) string {
	for _, l := range s.lits {
		line = strings.ReplaceAll(line, l.old, l.new)
	}
	line = uuidRe.ReplaceAllStringFunc(line, func(m string) string {
		return s.placeholder("uuid", strings.ToLower(m), func(n int) string {
			return fmt.Sprintf("00000000-0000-4000-8000-%012d", n)
		})
	})
	return idRe.ReplaceAllStringFunc(line, func(m string) string {
		prefix, _, _ := strings.Cut(m, "_")
		return s.placeholder(prefix, m, func(n int) string { return fmt.Sprintf("%s_scrub%04d", prefix, n) })
	})
}

func (s *Scrubber) placeholder(kind, key string, format func(int) string) string {
	if p, ok := s.ids[key]; ok {
		return p
	}
	s.n[kind]++
	p := format(s.n[kind])
	s.ids[key] = p
	return p
}

// Copy scrubs r line by line into w.
func (s *Scrubber) Copy(r io.Reader, w io.Writer) error {
	sc := bufio.NewScanner(r)
	sc.Buffer(make([]byte, 0, 64<<10), MaxLineBytes)
	bw := bufio.NewWriter(w)
	for sc.Scan() {
		if _, err := bw.WriteString(s.Line(sc.Text()) + "\n"); err != nil {
			return fmt.Errorf("write scrubbed line: %w", err)
		}
	}
	if err := sc.Err(); err != nil {
		return fmt.Errorf("read capture: %w", err)
	}
	if err := bw.Flush(); err != nil {
		return fmt.Errorf("write scrubbed capture: %w", err)
	}
	return nil
}
