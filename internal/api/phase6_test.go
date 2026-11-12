package api

import (
	"context"
	"reflect"
	"slices"
	"strings"
	"testing"

	"github.com/brendatamaar/yggdrasil/internal/core"
	"github.com/brendatamaar/yggdrasil/internal/core/actions"
	"github.com/brendatamaar/yggdrasil/internal/core/newrepos"
	"github.com/brendatamaar/yggdrasil/internal/core/notify"
)

// The string enums of the DTOs mirror the core sets they come from. If a core
// set gains a value, this fails until the DTO (and so the generated TypeScript)
// follows, so the frontend build, not a request, catches the drift.
func TestPhase6DTOEnumsMatchCore(t *testing.T) {
	strs := func(in ...string) []string { slices.Sort(in); return in }
	var kinds []string
	for _, k := range actions.Kinds() {
		kinds = append(kinds, string(k))
	}
	dtoKinds := strs(string(ActionDiagnosePipeline), string(ActionFixPipeline), string(ActionReviewMR), string(ActionProposeCommits),
		string(ActionPullAndCheck), string(ActionImportIssue), string(ActionDiagnoseBuild), string(ActionDiagnoseRepo), string(ActionProposeIgnore))
	if !slices.Equal(strs(kinds...), dtoKinds) {
		t.Errorf("action kinds: core %v, DTO %v", strs(kinds...), dtoKinds)
	}
	var reasons []string
	for _, r := range actions.Reasons() {
		reasons = append(reasons, string(r))
	}
	dtoReasons := strs(string(ReasonUnreadable), string(ReasonConflicts), string(ReasonDiverged), string(ReasonFailedCI),
		string(ReasonFailedBuild), string(ReasonBehind), string(ReasonDirty))
	if !slices.Equal(strs(reasons...), dtoReasons) {
		t.Errorf("attention reasons: core %v, DTO %v", strs(reasons...), dtoReasons)
	}
	var statuses []string
	for _, s := range notify.Statuses() {
		statuses = append(statuses, string(s))
	}
	dtoStatuses := strs(string(NoticeNeedsYou), string(NoticeVerificationPassed), string(NoticeVerificationFailed),
		string(NoticeReadyForReview), string(NoticeDone), string(NoticeFailed), string(NoticeCancelled))
	if !slices.Equal(strs(statuses...), dtoStatuses) {
		t.Errorf("notice statuses: core %v, DTO %v", strs(statuses...), dtoStatuses)
	}
	if string(VisibilityPrivate) != string(newrepos.VisibilityPrivate) || string(VisibilityPublic) != string(newrepos.VisibilityPublic) ||
		string(OwnerUser) != string(newrepos.OwnerUser) || string(OwnerOrg) != string(newrepos.OwnerOrg) {
		t.Error("new repo visibility or owner kinds differ from core/newrepos")
	}
	for _, s := range newrepos.InitStages() {
		if s.Validate() != nil {
			t.Errorf("init stage %q", s)
		}
	}
}

func TestPhase6TopicsParseAndAreClosed(t *testing.T) {
	for topic, want := range map[string]wsTopic{TopicNotices: noticesTopic{}, TopicActions: actionsTopic{}, TopicNewRepos: newReposTopic{}} {
		got, err := parseTopic(topic)
		if err != nil || got != want || got.String() != topic {
			t.Errorf("parseTopic(%q) = %v, %v", topic, got, err)
		}
	}
	if TopicNotices != "notices" || TopicActions != "actions" || TopicNewRepos != "newrepos" {
		t.Error("a Phase 6 wire constant changed: the frontend subscribes with these strings")
	}
	for _, bad := range []string{"notices:1", "notice", "newrepo", "actions:x", "Notices"} {
		if _, err := parseTopic(bad); err == nil {
			t.Errorf("parseTopic(%q) accepted", bad)
		}
	}
}

// A notification can carry nothing but the title, the closed status and
// metadata: there is no free-text, prompt, path or log field (docs/AGENTS.md
// 5.9). The test lists every field so adding one is a deliberate act.
func TestNoticeDTOHasNoBody(t *testing.T) {
	var names []string
	for f := range reflect.TypeFor[NoticeDTO]().Fields() {
		names = append(names, f.Name)
	}
	slices.Sort(names)
	want := []string{"CreatedAt", "Cursor", "ID", "Revision", "Status", "TaskID", "TaskTitle"}
	if !slices.Equal(names, want) {
		t.Errorf("NoticeDTO fields = %v, want %v", names, want)
	}
	var msg []string
	for f := range reflect.TypeFor[NoticeChangedMessage]().Fields() {
		msg = append(msg, f.Name)
	}
	if !slices.Equal(msg, []string{"NoticeID", "Cursor"}) {
		t.Errorf("NoticeChangedMessage fields = %v: it must carry IDs and cursors only", msg)
	}
	// The task status message gained only a revision.
	var ts []string
	for f := range reflect.TypeFor[TaskStatusMessage]().Fields() {
		ts = append(ts, f.Name)
	}
	if !slices.Equal(ts, []string{"TaskID", "Title", "Status", "Revision"}) {
		t.Errorf("TaskStatusMessage fields = %v", ts)
	}
}

// No Phase 6 request carries a host URL, a filesystem path or a token: IDs and
// plain values only. A field whose name suggests otherwise fails the test.
func TestPhase6RequestsCarryNoPathURLOrToken(t *testing.T) {
	for _, typ := range []reflect.Type{
		reflect.TypeFor[ActionRequestDTO](), reflect.TypeFor[ActionExecuteRequest](), reflect.TypeFor[ImportIssueDraftRequest](),
		reflect.TypeFor[CreateIgnoreProposalRequest](), reflect.TypeFor[StandupPolishRequest](), reflect.TypeFor[NewRepoPreviewRequest](),
		reflect.TypeFor[ReserveNewRepoRequest](), reflect.TypeFor[PublishPreviewRequest](), reflect.TypeFor[PublishNewRepoRequest](),
		reflect.TypeFor[StartDraftRequest](),
	} {
		for f := range typ.Fields() {
			n := strings.ToLower(f.Name)
			for _, bad := range []string{"url", "path", "token", "password", "secret", "cwd", "dir"} {
				if strings.Contains(n, bad) {
					t.Errorf("%s.%s looks like a %s", typ.Name(), f.Name, bad)
				}
			}
		}
	}
}

func TestPhase6ServicesDefaultToNilAndCanBeBound(t *testing.T) {
	var s Server
	if p := s.phase6(); p == nil || p.Actions != nil || p.Notices != nil || p.NewRepos != nil {
		t.Fatalf("the default services = %+v", p)
	}
	s.SetPhase6Services(Phase6Services{Notices: noticeStub{}})
	if s.phase6().Notices == nil || s.phase6().Actions != nil {
		t.Error("SetPhase6Services did not bind exactly what it was given")
	}
}

type noticeStub struct{}

func (noticeStub) Notices(_ context.Context, _ int64, _ int) (NoticesResponse, error) {
	return NoticesResponse{}, nil
}

func TestPhase6HubPublishesAfterTheCaller(t *testing.T) {
	e := newEnv(t)
	var hub Phase6Hub = e.srv
	hub.PublishNotice(NoticeChangedMessage{NoticeID: "ntc_1", Cursor: 1})
	hub.PublishNotice(NoticeChangedMessage{NoticeID: "ntc_2", Cursor: 2})
	hub.PublishAction(ActionChangedMessage{RequestID: "req_1", State: ActionStateCompleted})
	hub.PublishNewRepo(NewRepoChangedMessage{OperationID: "op_1", State: NewRepoReady})
	e.srv.hub.mu.Lock()
	defer e.srv.hub.mu.Unlock()
	if e.srv.hub.seq[TopicNotices] != 2 || e.srv.hub.seq[TopicActions] != 1 || e.srv.hub.seq[TopicNewRepos] != 1 {
		t.Errorf("topic sequences = %v", e.srv.hub.seq)
	}
	if _, ok := e.srv.hub.seq[TopicRepos]; ok {
		t.Error("a new-repo message published the repos topic with counts it does not have")
	}
}

var _ = core.TaskID("")
