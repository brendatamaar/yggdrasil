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
		"url as object":   {Repo: "rep_1", Kind: ContextIssue, ObjectID: "https://x/issues/1"},
		"zero":            {Repo: "rep_1", Kind: ContextIssue, ObjectID: "0"},
		"negative":        {Repo: "rep_1", Kind: ContextIssue, ObjectID: "-3"},
		"leading zero":    {Repo: "rep_1", Kind: ContextIssue, ObjectID: "007"},
		"list with id":    {Repo: "rep_1", Kind: ContextIssues, ObjectID: "1"},
		"issue with head": {Repo: "rep_1", Kind: ContextIssue, ObjectID: "1", Head: head},
		"short head":      {Repo: "rep_1", Kind: ContextMRDiff, ObjectID: "1", Head: "abc"},
		"upper head":      {Repo: "rep_1", Kind: ContextMRDiff, ObjectID: "1", Head: strings.ToUpper(head)},
		"no repo":         {Kind: ContextIssues},
		"unknown kind":    {Repo: "rep_1", Kind: "files", ObjectID: "1"},
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

// ContextStep is a documented stub: it changes nothing and asks for nothing.
func TestContextStepIsInert(t *testing.T) {
	e := ContextEntry{Key: ContextKey{Repo: "rep_1", Kind: ContextIssues}, State: CtxEmpty{}}
	for _, in := range []ContextInput{
		ContextRequested{Key: e.Key, Scope: ScopeTracked}, ContextFetched{Attempt: 1}, ContextFetchFailed{Attempt: 1},
		ContextWake{Attempt: 1}, ContextCancelled{Attempt: 1}, ContextScopeChanged{Scope: ScopeUntracked}, ContextConfigChanged{},
	} {
		got, cmds := ContextStep(e, in)
		if _, ok := got.State.(CtxEmpty); !ok || got.Seq != 0 || len(cmds) != 0 {
			t.Errorf("ContextStep(%T) = %+v, %v", in, got, cmds)
		}
	}
}
