package api

import (
	"time"

	"github.com/brendatamaar/yggdrasil/internal/core"
)

// DTOs of dev servers (DV-01 to DV-03, docs/design/devservers.md).
//
//	GET  /api/devservers                      DevServersResponse
//	POST /api/devservers/{id}/start|stop|restart   DevServerDTO
//	GET  /api/devservers/{id}/log?tail=N      DevServerLogResponse
//
// The id names a server (repo, optional worktree, command). Clients never
// send a folder path.

// TopicDevServers carries DevServerChangedMessage: any server changed.
const TopicDevServers = "devservers"

// TopicDevServerPrefix starts "devserver:<id>", which carries
// DevServerChangedMessage and DevServerLogMessage for one server.
const TopicDevServerPrefix = "devserver:"

// DevServerStatus is the closed set of server states.
type DevServerStatus string

// The closed set of DevServerStatus values.
const (
	DevServerStopped  DevServerStatus = "stopped"
	DevServerStarting DevServerStatus = "starting"
	DevServerRunning  DevServerStatus = "running"
	DevServerStopping DevServerStatus = "stopping"
	DevServerFailed   DevServerStatus = "failed"
	DevServerOrphaned DevServerStatus = "orphaned"
)

// DevServerPortDTO is one listening port. OutsideBlock is true when the port
// lies outside the worktree's block (DV-02 warning).
type DevServerPortDTO struct {
	Port         int  `json:"port"`
	Announced    bool `json:"announced"`
	OutsideBlock bool `json:"outsideBlock"`
}

// DevServerDTO is one dev server. Message is a plain-language reason for a
// failed server; it holds no paths.
type DevServerDTO struct {
	ID         core.DevServerID   `json:"id"`
	RepoID     core.RepoID        `json:"repoId"`
	RepoName   string             `json:"repoName"`
	WorktreeID core.WorktreeID    `json:"worktreeId,omitempty"`
	Command    string             `json:"command"`
	Status     DevServerStatus    `json:"status"`
	Ports      []DevServerPortDTO `json:"ports"`
	BlockBase  int                `json:"blockBase,omitempty"`
	BlockSize  int                `json:"blockSize,omitempty"`
	Message    string             `json:"message,omitempty"`
	Since      *time.Time         `json:"since,omitempty"`
}

// DevServersResponse is the body of GET /api/devservers.
type DevServersResponse struct {
	Servers []DevServerDTO `json:"servers"`
}

// DevServerChangedMessage is the data of a message on TopicDevServers and on
// "devserver:<id>": the server's new state.
type DevServerChangedMessage struct {
	Server DevServerDTO `json:"server"`
}

// DevServerLogMessage is one log line on "devserver:<id>". Lines are redacted.
type DevServerLogMessage struct {
	ID     core.DevServerID `json:"id"`
	Seq    uint64           `json:"seq"`
	Stream string           `json:"stream"` // "stdout" or "stderr"
	Line   string           `json:"line"`
}

// DevServerLogResponse is the body of GET /api/devservers/{id}/log.
// Incomplete is true when output was lost (a grandchild held the pipe).
type DevServerLogResponse struct {
	Lines      []DevServerLogMessage `json:"lines"`
	Incomplete bool                  `json:"incomplete"`
}
