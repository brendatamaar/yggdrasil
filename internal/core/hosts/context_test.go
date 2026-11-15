package hosts

import (
	"strings"
	"testing"
)

func TestContextKeyValidate(t *testing.T) {
	head := strings.Repeat("a", 40)
	good := []ContextKey{
		{Repo: "rep_1", Kind: ContextFailedJobs, ObjectID: "12345", Head: head},
		{Repo: "rep_1", Kind: ContextFailedJobs, ObjectID: "7"},
		{Repo: "rep_1", Kind: ContextIssues},
		{Repo: "rep_1", Kind: ContextIssue, ObjectID: "42"},
		{Repo: "rep_1", Kind: ContextMRDiff, ObjectID: "9", Head: head},
	}
	for _, k := range good {
		if err := k.Validate(); err != nil {
			t.Errorf("%+v refused: %v", k, err)
		}
	}
	bad := map[string]ContextKey{
		"invalid UTF-8 repo": {Repo: "\x80", Kind: ContextIssues},
		"url as object":      {Repo: "rep_1", Kind: ContextIssue, ObjectID: "https://x/issues/1"},
		"zero":               {Repo: "rep_1", Kind: ContextIssue, ObjectID: "0"},
		"negative":           {Repo: "rep_1", Kind: ContextIssue, ObjectID: "-3"},
		"leading zero":       {Repo: "rep_1", Kind: ContextIssue, ObjectID: "007"},
		"list with id":       {Repo: "rep_1", Kind: ContextIssues, ObjectID: "1"},
		"issue with head":    {Repo: "rep_1", Kind: ContextIssue, ObjectID: "1", Head: head},
		"mr diff no head":    {Repo: "rep_1", Kind: ContextMRDiff, ObjectID: "1"},
		"short head":         {Repo: "rep_1", Kind: ContextMRDiff, ObjectID: "1", Head: "abc"},
		"upper head":         {Repo: "rep_1", Kind: ContextMRDiff, ObjectID: "1", Head: strings.ToUpper(head)},
		"no repo":            {Kind: ContextIssues},
		"unknown kind":       {Repo: "rep_1", Kind: "files", ObjectID: "1"},
	}
	for name, k := range bad {
		if k.Validate() == nil {
			t.Errorf("%s was accepted", name)
		}
	}
}

func TestContextBackoffAndBounds(t *testing.T) {
	if ContextBackoff(1) != 1e9 || ContextBackoff(2) != 3e9 || ContextBackoff(9) != 3e9 {
		t.Error("the backoff is 1 s then 3 s")
	}
	if ContextMaxPromptBytes >= 100<<10 {
		t.Error("the prompt bound must stay under the 100 KiB task prompt limit")
	}
	for _, r := range []ContextBlockReason{ContextNoToken, ContextForbidden, ContextNotFound, ContextUnsupported, ContextRetryExhausted, ContextUntracked, ContextTooLarge} {
		if r.Validate() != nil {
			t.Errorf("%s does not validate", r)
		}
	}
}
