package devservers

import (
	"slices"
	"strings"

	"github.com/brendatamaar/yggdrasil/internal/core"
)

// MaxPort is the highest TCP port.
const MaxPort = 65535

// Valid reports whether p is in 1..65535.
func (p Port) Valid() bool { return p >= 1 && p <= MaxPort }

// NormalizePorts turns the ports the OS reported into a sorted, unique list.
// Ports outside 1..65535 are dropped; dropped counts them (duplicates are not
// counted). The result is never nil.
func NormalizePorts(raw []int) (ports []Port, dropped int) {
	ports = make([]Port, 0, len(raw))
	for _, n := range raw {
		p := Port(n)
		if !p.Valid() {
			dropped++
			continue
		}
		ports = append(ports, p)
	}
	return sortUnique(ports), dropped
}

// sortUnique returns ports sorted without duplicates (a new slice, never nil).
func sortUnique(ports []Port) []Port {
	out := slices.Clone(ports)
	if out == nil {
		out = []Port{}
	}
	slices.Sort(out)
	return slices.Compact(out)
}

// normalizeValid is NormalizePorts for ports already typed.
func normalizeValid(in []Port) []Port {
	out := make([]Port, 0, len(in))
	for _, p := range in {
		if p.Valid() {
			out = append(out, p)
		}
	}
	return sortUnique(out)
}

// OutsideBlock returns the ports that lie outside block, sorted. A server in
// a main checkout has no block and so no warnings (docs/design/devservers.md,
// open question 2).
func OutsideBlock(ports []Port, block core.PortBlock) []Port {
	if block.Empty() {
		return nil
	}
	var out []Port
	for _, p := range sortUnique(ports) {
		if !block.Contains(int(p)) {
			out = append(out, p)
		}
	}
	return out
}

// Warnings are the observed ports of a running server that lie outside its
// block (DV-02). They are derived, never stored.
func (r Running) Warnings() []Port { return OutsideBlock(r.Ports, r.Block) }

// maxLine bounds how much of an output line is scanned.
const maxLine = 4096

// ParseAnnouncement reads the loopback ports a line of server output
// announces: localhost:3000, 127.0.0.1:8000, [::1]:5000, as in "Local:
// http://localhost:5173/", "Now listening on: http://localhost:5000" and
// "Uvicorn running on http://127.0.0.1:8000". Other hosts (0.0.0.0, LAN
// addresses, names) are ignored, and so are ports outside 1..65535. The
// result is sorted and unique, and nil when the line announces nothing.
func ParseAnnouncement(line string) []Port {
	if len(line) > maxLine {
		line = line[:maxLine]
	}
	line = strings.ToLower(stripANSI(line))
	var found []Port
	for _, host := range []string{"localhost", "127.0.0.1", "[::1]"} {
		offset := 0
		for {
			i := strings.Index(line[offset:], host)
			if i < 0 {
				break
			}
			at := offset + i
			end := at + len(host)
			offset = end
			if at > 0 && hostChar(line[at-1]) {
				continue // "my.localhost", "xlocalhost"
			}
			if end >= len(line) || line[end] != ':' {
				continue
			}
			j := end + 1
			for j < len(line) && line[j] >= '0' && line[j] <= '9' {
				j++
			}
			digits := line[end+1 : j]
			if digits == "" || len(digits) > 5 || (j < len(line) && isLetter(line[j])) {
				continue
			}
			n := 0
			for k := 0; k < len(digits); k++ {
				n = n*10 + int(digits[k]-'0')
			}
			if p := Port(n); p.Valid() {
				found = append(found, p)
			}
		}
	}
	if len(found) == 0 {
		return nil
	}
	return sortUnique(found)
}

func hostChar(c byte) bool {
	return isLetter(c) || (c >= '0' && c <= '9') || c == '.' || c == '-' || c == '_'
}

func isLetter(c byte) bool { return c >= 'a' && c <= 'z' || c >= 'A' && c <= 'Z' }

// stripANSI removes ESC [ ... final-byte sequences, so a colored "3000" is
// one number. A lone ESC is dropped.
func stripANSI(s string) string {
	if !strings.Contains(s, "\x1b") {
		return s
	}
	var b strings.Builder
	for i := 0; i < len(s); i++ {
		if s[i] != 0x1b {
			b.WriteByte(s[i])
			continue
		}
		if i+1 < len(s) && s[i+1] == '[' {
			i += 2
			for i < len(s) && (s[i] < 0x40 || s[i] > 0x7e) {
				i++
			}
		}
	}
	return b.String()
}
