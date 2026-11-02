package api

import (
	"github.com/brendatamaar/yggdrasil/internal/core"
	"github.com/brendatamaar/yggdrasil/internal/core/verify"
)

// Go-only wiring (excluded from tygo): the Phase 4 services, which may be set
// after the server exists, and the hub publishers.
//
// The verification and change-set services publish to the hub, and the server
// needs the services to answer: Deps may be left empty and the services bound
// later with SetPhase4Services. Until then the endpoints answer 501.

// Phase4Services are the services of the Phase 4 areas. A nil field answers
// 501 for its endpoints.
type Phase4Services struct {
	Verification VerificationService
	Review       ReviewService
	ChangeSets   ChangeSetService
	Landing      LandingService
	// WorktreeDiff serves GET /api/tasks/{id}/repos/{repo}/diff.
	WorktreeDiff WorktreeDiffer
}

// SetPhase4Services binds (or replaces) the Phase 4 services. Safe to call
// while the server runs.
func (s *Server) SetPhase4Services(p Phase4Services) { s.p4.Store(&p) }

// phase4 returns the bound services; the zero value when none are bound.
func (s *Server) phase4() *Phase4Services {
	if p := s.p4.Load(); p != nil {
		return p
	}
	return &Phase4Services{}
}

var (
	_ verifyPublisher = (*Server)(nil)
)

// verifyPublisher is verify.Publisher (kept as a local alias so this file
// documents what the server provides).
type verifyPublisher interface {
	PublishVerification(id verify.VerificationID, task core.TaskID, repo core.RepoID, state string)
}

// PublishVerification tells clients on the "verifications" topic that a
// verification changed, and refreshes the board (its verification badge).
// Neither message holds a log, path or command.
func (s *Server) PublishVerification(id verify.VerificationID, task core.TaskID, repo core.RepoID, state string) {
	s.hub.publishNext(verificationsTopic{}, VerificationChangedMessage{VerificationID: string(id), TaskID: task, RepoID: repo, State: state})
	s.hub.publishNext(boardTopic{}, BoardChangedMessage{TaskID: task})
}

// PublishChangeSet tells clients on the "changesets" topic that a change set
// changed.
func (s *Server) PublishChangeSet(id core.ChangeSetID, task core.TaskID, _ core.ChangeSetStatus) {
	s.hub.publishNext(changeSetsTopic{}, ChangeSetChangedMessage{ChangeSetID: id, TaskID: task, Kind: "changeset"})
}

// PublishNotes tells clients that the review notes of a task changed. It
// carries no note text.
func (s *Server) PublishNotes(task core.TaskID) {
	s.hub.publishNext(changeSetsTopic{}, ChangeSetChangedMessage{TaskID: task, Kind: "notes"})
}
