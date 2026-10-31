package mcp

import (
	"context"

	"github.com/brendatamaar/yggdrasil/internal/core"
)

// NoDevServers is the DevServers of Phase 3: dev servers do not exist yet, so
// none is ever running and every log request is "no such dev server".
type NoDevServers struct{}

var _ DevServers = NoDevServers{}

// List reports none running.
func (NoDevServers) List(context.Context, core.ProjectID) ([]DevServer, error) { return nil, nil }

// Log reports that there is no such dev server.
func (NoDevServers) Log(context.Context, core.ProjectID, string, int) (string, error) {
	return "", ErrNoDevServer
}
