package maven

import (
	"io"
	"strings"
)

type charsetError string

func (e charsetError) Error() string { return "unsupported pom.xml encoding " + string(e) }

// charsetReader accepts the encodings a pom.xml realistically has besides
// UTF-8: ISO-8859-1 (each byte is the code point of the same value) and
// US-ASCII. Anything else is refused with a plain error.
func charsetReader(label string, in io.Reader) (io.Reader, error) {
	switch strings.ToLower(strings.TrimSpace(label)) {
	case "utf-8", "utf8", "us-ascii", "ascii":
		return in, nil
	case "iso-8859-1", "iso8859-1", "latin1", "latin-1", "l1":
		b, err := io.ReadAll(in)
		if err != nil {
			return nil, err
		}
		rs := make([]rune, len(b))
		for i, c := range b {
			rs[i] = rune(c)
		}
		return strings.NewReader(string(rs)), nil
	}
	return nil, charsetError(label)
}
