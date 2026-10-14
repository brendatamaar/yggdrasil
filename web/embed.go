// Package web embeds the built frontend (web/dist) into the ygg binary.
//
// dist/ is produced by `task build:web`; only dist/.gitkeep is committed, so
// the Go build works before the frontend has been built.
package web

import "embed"

// Dist holds the frontend build output under the "dist" prefix.
//
//go:embed all:dist
var Dist embed.FS
