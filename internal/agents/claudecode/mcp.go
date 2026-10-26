package claudecode

import (
	"encoding/json"
	"errors"
	"fmt"
	"net/url"
	"os"
	"path/filepath"
	"strings"
)

// mcpServerName is the name Yggdrasil's MCP server has in the CLI. The tools
// then appear as mcp__ygg__<tool>.
const mcpServerName = "ygg"

// mcpFileName is the config file's name inside the run folder.
const mcpFileName = "ygg-mcp.json"

type mcpFile struct {
	Servers map[string]mcpServer `json:"mcpServers"`
}

type mcpServer struct {
	Type    string            `json:"type"`
	URL     string            `json:"url"`
	Headers map[string]string `json:"headers"`
}

// WriteMCPConfig implements agents.MCPConfigurer: it writes the CLI's MCP
// config into dir, with an http server named "ygg" and the bearer token in a
// header. The token goes into this file only, never onto a command line or
// into an event or log (invariant 4); errors here never repeat it. The file is
// created for its owner only, and the adapter never reads it back.
func (*Adapter) WriteMCPConfig(dir, rawURL, token string) (string, error) {
	u, err := url.Parse(rawURL)
	if err != nil || (u.Scheme != "http" && u.Scheme != "https") || u.Host == "" || u.User != nil {
		return "", &UserError{Message: "The address of Yggdrasil's agent tool server isn't valid."}
	}
	if token == "" || strings.ContainsAny(token, "\r\n\x00 \t\"\\") {
		return "", &UserError{Message: "The access key for Yggdrasil's agent tool server isn't valid."}
	}
	if dir == "" || !filepath.IsAbs(dir) {
		return "", errors.New("claudecode: the MCP config folder must be an absolute path")
	}
	data, err := json.MarshalIndent(mcpFile{Servers: map[string]mcpServer{
		mcpServerName: {Type: "http", URL: u.String(), Headers: map[string]string{"Authorization": "Bearer " + token}},
	}}, "", "  ")
	if err != nil {
		return "", errors.New("claudecode: encode the MCP config")
	}
	path := filepath.Join(dir, mcpFileName)
	if err := os.WriteFile(path, append(data, '\n'), 0o600); err != nil {
		return "", fmt.Errorf("write the MCP config: %w", err)
	}
	// The file may have existed with other permissions; make them owner-only.
	if err := os.Chmod(path, 0o600); err != nil {
		return "", errors.New("claudecode: restrict the MCP config to its owner")
	}
	return path, nil
}
