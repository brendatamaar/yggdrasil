// Package find holds the small file-lookup helpers the toolchain modules
// share. It is not a toolchain; modules import it, never each other.
package find

import (
	"io/fs"
	"path"
	"sort"
	"strings"
)

// Exists reports whether name is a regular file in fsys.
func Exists(fsys fs.FS, name string) bool {
	st, err := fs.Stat(fsys, name)
	return err == nil && st.Mode().IsRegular()
}

// Dir returns the folder that holds any of names: the repo root ("."), else
// the first immediate sub folder in name order (skipping dot folders and
// node_modules, target, build, dist, vendor). The second result is the file
// found there.
func Dir(fsys fs.FS, names ...string) (dir, file string, ok bool) {
	for _, n := range names {
		if Exists(fsys, n) {
			return ".", n, true
		}
	}
	entries, err := fs.ReadDir(fsys, ".")
	if err != nil {
		return "", "", false
	}
	var subs []string
	for _, e := range entries {
		n := e.Name()
		if !e.IsDir() || strings.HasPrefix(n, ".") {
			continue
		}
		switch n {
		case "node_modules", "target", "build", "dist", "vendor":
			continue
		}
		subs = append(subs, n)
	}
	sort.Strings(subs)
	for _, s := range subs {
		for _, n := range names {
			if Exists(fsys, path.Join(s, n)) {
				return s, n, true
			}
		}
	}
	return "", "", false
}

// Join joins dir and name with forward slashes, dropping a "." dir.
func Join(dir, name string) string {
	if dir == "." || dir == "" {
		return name
	}
	return dir + "/" + name
}
