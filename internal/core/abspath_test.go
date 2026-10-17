package core

import "testing"

func TestCheckAbsPath(t *testing.T) {
	for _, ok := range []string{
		`C:\work\x`, `c:/work/x`, `D:\`, `\\host\share\x`, `\\?\C:\work\x`, `\??\C:\work\x`, `/work/x`, `/`,
	} {
		if err := checkAbsPath(ok); err != nil {
			t.Errorf("%q rejected: %v", ok, err)
		}
	}
	for name, bad := range map[string]string{
		"empty":                   "",
		"blank":                   "  ",
		"NUL":                     "/work\x00x",
		"drive-relative":          `\work\x`,
		"single backslash":        `\`,
		"relative":                `work\x`,
		"dot relative":            `.\x`,
		"drive letter no slash":   `C:work`,
		"drive letter only":       `C:`,
		"non-letter drive":        `1:\x`,
		"question mark one slash": `\?\C:\x`,
	} {
		if err := checkAbsPath(bad); err == nil {
			t.Errorf("%s: %q accepted", name, bad)
		}
	}
}
