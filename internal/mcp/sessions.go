package mcp

import (
	"crypto/rand"
	"crypto/sha256"
	"crypto/subtle"
	"encoding/base64"
	"errors"
	"fmt"
	"io"
	"sync"

	"github.com/brendatamaar/yggdrasil/internal/core"
)

// Active is a live MCP session: the task and the run attempt it belongs to.
type Active struct {
	Task    core.TaskID
	Attempt int
}

// SecretSink receives every token that is issued, so the redactor masks it in
// logs, events and the audit log (invariant 4). *safety.SecretRedactor is one.
type SecretSink interface{ Add(secrets ...string) }

// Sessions is the registry of per-task bearer tokens (docs/design/mcp.md).
// Only the sha256 of a token is kept; the token itself lives in the agent's
// config file for the run and nowhere else. Safe for concurrent use.
type Sessions struct {
	mu     sync.Mutex
	byHash map[[sha256.Size]byte]Active
	byTask map[core.TaskID][sha256.Size]byte
	rand   io.Reader
	sink   SecretSink
}

// NewSessions returns an empty registry. sink may be nil (tests); rnd defaults
// to crypto/rand.
func NewSessions(sink SecretSink, rnd io.Reader) *Sessions {
	if rnd == nil {
		rnd = rand.Reader
	}
	return &Sessions{
		byHash: map[[sha256.Size]byte]Active{}, byTask: map[core.TaskID][sha256.Size]byte{},
		rand: rnd, sink: sink,
	}
}

// Issue makes a new random token (32 bytes, base64url) for the task's run. A
// token the task had before stops working: a task has one live run.
func (s *Sessions) Issue(task core.TaskID, attempt int) (string, error) {
	if err := task.Validate(); err != nil {
		return "", fmt.Errorf("issue MCP token: %w", err)
	}
	b := make([]byte, 32)
	if _, err := io.ReadFull(s.rand, b); err != nil {
		return "", errors.New("issue MCP token: no random data")
	}
	token := base64.RawURLEncoding.EncodeToString(b)
	h := sha256.Sum256([]byte(token))
	if s.sink != nil {
		s.sink.Add(token)
	}
	s.mu.Lock()
	defer s.mu.Unlock()
	if old, ok := s.byTask[task]; ok {
		delete(s.byHash, old)
	}
	s.byHash[h] = Active{Task: task, Attempt: attempt}
	s.byTask[task] = h
	return token, nil
}

// Lookup finds the session of a token. It compares the hash against every
// stored hash in constant time per entry and does not stop at the first
// match, so timing says nothing about which prefix was right.
func (s *Sessions) Lookup(token string) (Active, bool) {
	if token == "" {
		return Active{}, false
	}
	h := sha256.Sum256([]byte(token))
	s.mu.Lock()
	defer s.mu.Unlock()
	var found Active
	ok := 0
	for k, a := range s.byHash {
		if subtle.ConstantTimeCompare(k[:], h[:]) == 1 {
			found, ok = a, 1
		}
	}
	return found, ok == 1
}

// Live reports whether the task has a live session. It does not look at the
// token.
func (s *Sessions) Live(task core.TaskID) bool {
	s.mu.Lock()
	defer s.mu.Unlock()
	_, ok := s.byTask[task]
	return ok
}

// Revoke ends the task's session. Revoking an unknown task does nothing.
func (s *Sessions) Revoke(task core.TaskID) {
	s.mu.Lock()
	defer s.mu.Unlock()
	if h, ok := s.byTask[task]; ok {
		delete(s.byHash, h)
		delete(s.byTask, task)
	}
}

// Count is the number of live sessions.
func (s *Sessions) Count() int {
	s.mu.Lock()
	defer s.mu.Unlock()
	return len(s.byTask)
}
