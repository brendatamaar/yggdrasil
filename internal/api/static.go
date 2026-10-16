package api

import (
	"bytes"
	"io"
	"io/fs"
	"mime"
	"net/http"
	"path"
	"strings"
	"time"
)

const notBuiltPage = `<!doctype html>
<html lang="en">
<head><meta charset="utf-8"><title>Yggdrasil</title></head>
<body>
<h1>Yggdrasil</h1>
<p>The frontend isn't built into this binary. Run <code>task build</code> and start <code>ygg</code> again.</p>
</body>
</html>
`

// zeroTime makes http.ServeContent skip Last-Modified: the assets live in the
// binary.
var zeroTime time.Time

type asset struct {
	data []byte
	mime string
}

// assets is the allowlist of files the server may return: every regular file
// of the embedded build, read once at startup. Nothing is looked up on disk
// per request, so a crafted path can only miss (it never reaches a file), and
// Windows device names (CON, NUL...) are not in the map.
type assets map[string]asset

func loadAssets(fsys fs.FS) (assets, error) {
	out := assets{}
	if fsys == nil {
		return out, nil
	}
	err := fs.WalkDir(fsys, ".", func(p string, d fs.DirEntry, err error) error {
		if err != nil {
			return err
		}
		if d.IsDir() || !d.Type().IsRegular() || path.Base(p) == ".gitkeep" {
			return nil
		}
		f, err := fsys.Open(p)
		if err != nil {
			return err
		}
		defer f.Close()
		data, err := io.ReadAll(f)
		if err != nil {
			return err
		}
		out["/"+p] = asset{data: data, mime: mimeFor(p)}
		return nil
	})
	return out, err
}

// mimeFor picks the Content-Type from the extension alone. The Windows
// registry often maps .js and .mjs to text/plain, which browsers refuse as
// module scripts.
func mimeFor(name string) string {
	switch strings.ToLower(path.Ext(name)) {
	case ".js", ".mjs":
		return "text/javascript; charset=utf-8"
	case ".css":
		return "text/css; charset=utf-8"
	case ".html":
		return "text/html; charset=utf-8"
	case ".json", ".map":
		return "application/json; charset=utf-8"
	case ".svg":
		return "image/svg+xml"
	case ".wasm":
		return "application/wasm"
	}
	if t := mime.TypeByExtension(path.Ext(name)); t != "" {
		return t
	}
	return "application/octet-stream"
}

// windowsDevice reports whether a path segment is a reserved Windows device
// name, with or without an extension (CON, con.txt, COM1, LPT3...).
func windowsDevice(segment string) bool {
	base := strings.ToUpper(segment)
	if i := strings.IndexByte(base, '.'); i >= 0 {
		base = base[:i]
	}
	base = strings.TrimRight(base, " ")
	switch base {
	case "CON", "PRN", "AUX", "NUL":
		return true
	}
	if len(base) == 4 && (strings.HasPrefix(base, "COM") || strings.HasPrefix(base, "LPT")) {
		return base[3] >= '0' && base[3] <= '9'
	}
	return false
}

// cleanAssetPath normalises a request path and rejects what must never be
// looked up: traversal, backslashes, drive letters and device names.
func cleanAssetPath(p string) (string, bool) {
	if strings.ContainsAny(p, "\\:\x00") || strings.Contains(p, "..") {
		return "", false
	}
	clean := path.Clean("/" + p)
	for _, seg := range strings.Split(clean, "/") {
		if seg != "" && windowsDevice(seg) {
			return "", false
		}
	}
	return clean, true
}

// serveUI serves the single-page app. A path without a file extension that is
// not a file falls back to index.html, so client-side routes survive a reload.
func (s *Server) serveUI(w http.ResponseWriter, r *http.Request) {
	if r.Method != http.MethodGet && r.Method != http.MethodHead {
		writeError(w, http.StatusMethodNotAllowed, ErrBadRequest, "That request type isn't supported here.", "")
		return
	}
	clean, ok := cleanAssetPath(r.URL.Path)
	if !ok {
		writeError(w, http.StatusNotFound, ErrNotFound, "That page doesn't exist.", "")
		return
	}
	index, hasIndex := s.assets["/index.html"]
	if !hasIndex {
		w.Header().Set("Content-Type", "text/html; charset=utf-8")
		_, _ = io.WriteString(w, notBuiltPage)
		return
	}
	a, found := s.assets[clean]
	if clean == "/" {
		a, found = index, true
	}
	if !found {
		if path.Ext(clean) != "" {
			writeError(w, http.StatusNotFound, ErrNotFound, "That file doesn't exist.", "")
			return
		}
		a = index
	}
	w.Header().Set("Content-Type", a.mime)
	if strings.HasPrefix(clean, "/assets/") {
		w.Header().Set("Cache-Control", "public, max-age=31536000, immutable")
	} else {
		w.Header().Set("Cache-Control", "no-cache")
	}
	http.ServeContent(w, r, "", zeroTime, bytes.NewReader(a.data))
}
