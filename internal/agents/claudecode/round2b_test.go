package claudecode

import (
	"errors"
	"strings"
	"testing"
)

func TestRemoveRunFolderRetriesOnceThenGivesUp(t *testing.T) {
	calls := 0
	removeRunFolder("x", func(string) error { calls++; return errors.New("locked") }, 0)
	if calls != 2 {
		t.Errorf("RemoveAll called %d times, want 2", calls)
	}
	calls = 0
	removeRunFolder("x", func(string) error {
		calls++
		if calls == 1 {
			return errors.New("locked")
		}
		return nil
	}, 0)
	if calls != 2 {
		t.Errorf("a retry that works: %d calls", calls)
	}
}

func TestBatchArgMessageNamesTheKindOfFolder(t *testing.T) {
	if !strings.Contains(batchArgMessage, "project folder") || !strings.Contains(batchArgMessage, "temp folder") ||
		strings.ContainsAny(batchArgMessage, `\/`) && strings.Contains(batchArgMessage, ":\\") {
		t.Errorf("message = %q", batchArgMessage)
	}
}
