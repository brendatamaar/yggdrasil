package main

import (
	"io"
	"io/fs"
	"net/http"
	"path"
	"strings"
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

// uiHandler serves the embedded single-page app. Unknown paths without a file
// extension fall back to index.html so client-side routes survive a reload.
func uiHandler(dist fs.FS) http.Handler {
	if _, err := fs.Stat(dist, "index.html"); err != nil {
		return http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
			w.Header().Set("Content-Type", "text/html; charset=utf-8")
			_, _ = io.WriteString(w, notBuiltPage)
		})
	}

	files := http.FileServerFS(dist)
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		name := strings.TrimPrefix(path.Clean(r.URL.Path), "/")
		if name != "" && path.Ext(name) == "" {
			if _, err := fs.Stat(dist, name); err != nil {
				r = r.Clone(r.Context())
				r.URL.Path = "/"
			}
		}
		files.ServeHTTP(w, r)
	})
}
