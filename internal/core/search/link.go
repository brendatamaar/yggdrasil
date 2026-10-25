package search

import (
	"fmt"
	"strconv"
	"strings"
)

// Editor is a closed set of editors the browser can open through a URL.
type Editor string

const (
	EditorVSCode Editor = "vscode"
	EditorCursor Editor = "cursor"
	EditorIdea   Editor = "idea"
)

// ParseEditor accepts only the closed set.
func ParseEditor(s string) (Editor, error) {
	e := Editor(s)
	switch e {
	case EditorVSCode, EditorCursor, EditorIdea:
		return e, nil
	}
	return "", fmt.Errorf("editor %q is not supported: use vscode, cursor or idea", s)
}

// EditorURL builds the link that opens absPath at line and column in the
// editor. Paths use "/" in the URL even on Windows, and every byte outside the
// unreserved set is percent-encoded. A line or column under 1 becomes 1.
//
//	vscode://file/<path>:<line>:<col>  cursor://file/<path>:<line>:<col>
//	idea://open?file=<path>&line=<line>
func EditorURL(e Editor, absPath string, line, col int) (string, error) {
	if _, err := ParseEditor(string(e)); err != nil {
		return "", err
	}
	if strings.TrimSpace(absPath) == "" || strings.ContainsRune(absPath, 0) {
		return "", fmt.Errorf("editor link needs a file path")
	}
	line, col = max(line, 1), max(col, 1)
	p := strings.ReplaceAll(absPath, "\\", "/")
	if e == EditorIdea {
		return "idea://open?file=" + escape(p, false) + "&line=" + strconv.Itoa(line), nil
	}
	return string(e) + "://file/" + escape(strings.TrimLeft(p, "/"), true) + ":" + strconv.Itoa(line) + ":" + strconv.Itoa(col), nil
}

const hexDigits = "0123456789ABCDEF"

// escape percent-encodes everything but unreserved characters and "/". With
// driveColon, the colon of a leading drive letter ("C:") stays as it is.
func escape(s string, driveColon bool) string {
	var b strings.Builder
	for i := 0; i < len(s); i++ {
		c := s[i]
		switch {
		case c >= 'a' && c <= 'z', c >= 'A' && c <= 'Z', c >= '0' && c <= '9',
			c == '-', c == '.', c == '_', c == '~', c == '/':
			b.WriteByte(c)
		case c == ':' && driveColon && i == 1 && isLetter(s[0]) && (len(s) == 2 || s[2] == '/'):
			b.WriteByte(c)
		default:
			b.WriteByte('%')
			b.WriteByte(hexDigits[c>>4])
			b.WriteByte(hexDigits[c&15])
		}
	}
	return b.String()
}

func isLetter(c byte) bool { return c >= 'a' && c <= 'z' || c >= 'A' && c <= 'Z' }
