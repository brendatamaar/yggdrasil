package tasks

import (
	"fmt"

	"github.com/brendatamaar/yggdrasil/internal/core"
	coretasks "github.com/brendatamaar/yggdrasil/internal/core/tasks"
)

// maxLoggedPaths caps the changed paths written to the log line of an abandon.
const maxLoggedPaths = 20

// violation is an integrity verdict of a read-only turn that is not stored yet
// (SF-07). It lives in memory only, so the recovery of the same process can
// still feed IntegrityViolated before it feeds Recovered.
type violation struct {
	turn   coretasks.TurnID
	audits []auditSpec
}

// summary returns the changed paths (repo-relative, capped) and the outcomes of
// the pending audit entries, passed through redact.
func (v violation) summary(redact func(string) string) (paths, reasons []string) {
	for _, a := range v.audits {
		if o, ok := a.Detail["outcome"]; ok {
			reasons = append(reasons, redact(a.Action+": "+fmt.Sprint(o)))
		}
		if ch, ok := a.Detail["changes"].([]string); ok {
			for _, p := range ch {
				if len(paths) == maxLoggedPaths {
					return paths, reasons
				}
				paths = append(paths, redact(p))
			}
		}
	}
	return paths, reasons
}

func (s *Service) setViolation(id core.TaskID, v violation) {
	s.mu.Lock()
	s.violations[id] = v
	s.mu.Unlock()
}

func (s *Service) clearViolation(id core.TaskID) {
	s.mu.Lock()
	delete(s.violations, id)
	s.mu.Unlock()
}

func (s *Service) pendingViolation(id core.TaskID) (violation, bool) {
	s.mu.Lock()
	defer s.mu.Unlock()
	v, ok := s.violations[id]
	return v, ok
}
