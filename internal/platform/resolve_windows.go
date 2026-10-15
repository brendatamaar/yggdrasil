//go:build windows

package platform

import (
	"path/filepath"
	"strings"
)

const (
	mvnwFile    = "mvnw.cmd"
	gradlewFile = "gradlew.bat"
	venvPython  = `.venv\Scripts\python.exe`
)

const defaultPathExt = ".COM;.EXE;.BAT;.CMD"

// candidates lists the files base can mean: base itself when it already has a
// PATHEXT extension, else base plus each extension. A bare name is never run
// (npm the shell script is not npm.cmd).
func candidates(base string, env []string) []string {
	pathExt := envGet(env, "PATHEXT")
	if pathExt == "" {
		pathExt = defaultPathExt
	}
	var exts []string
	for e := range strings.SplitSeq(pathExt, ";") {
		if e != "" {
			exts = append(exts, e)
		}
	}
	for _, e := range exts {
		if strings.EqualFold(filepath.Ext(base), e) {
			return []string{base}
		}
	}
	out := make([]string, 0, len(exts))
	for _, e := range exts {
		out = append(out, base+e)
	}
	return out
}

func isExecutable(path string) bool { return isRegular(path) }
