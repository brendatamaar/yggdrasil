package core

import (
	"errors"
	"testing"
)

func TestIDValidate(t *testing.T) {
	if err := TaskID("t_1").Validate(); err != nil {
		t.Fatalf("valid id rejected: %v", err)
	}
	for _, bad := range []string{"", "a b", "a/b", "a\\b", "a\n"} {
		if err := TaskID(bad).Validate(); !errors.Is(err, ErrInvalidID) {
			t.Errorf("TaskID(%q).Validate() = %v, want ErrInvalidID", bad, err)
		}
	}
}

func TestEnumValidate(t *testing.T) {
	if err := ModeCode.Validate(); err != nil {
		t.Errorf("ModeCode: %v", err)
	}
	if err := TaskMode("yolo").Validate(); err == nil {
		t.Error("unknown mode accepted")
	}
	if err := TaskStatus("").Validate(); err == nil {
		t.Error("empty status accepted")
	}
	if err := ProposalKind("rm_rf").Validate(); err == nil {
		t.Error("unknown proposal kind accepted")
	}
}

func TestTaskStatusTerminal(t *testing.T) {
	for _, s := range []TaskStatus{TaskDone, TaskFailed, TaskCancelled} {
		if !s.Terminal() {
			t.Errorf("%s should be terminal", s)
		}
	}
	for _, s := range []TaskStatus{TaskDraft, TaskQueued, TaskRunning, TaskNeedsYou, TaskReview} {
		if s.Terminal() {
			t.Errorf("%s should not be terminal", s)
		}
	}
}

func TestTaskValidate(t *testing.T) {
	task := Task{
		ID: "t_1", Scope: ScopeRepo, Mode: ModeAsk, Status: TaskDraft,
		RepoIDs: []RepoID{"r_1"},
	}
	if err := task.Validate(); err != nil {
		t.Fatalf("valid task rejected: %v", err)
	}
	self := task
	id := task.ID
	self.ParentTaskID = &id
	if err := self.Validate(); err == nil {
		t.Error("task that is its own parent accepted")
	}
	none := task
	none.RepoIDs = nil
	if err := none.Validate(); err == nil {
		t.Error("task without repos accepted")
	}
}
