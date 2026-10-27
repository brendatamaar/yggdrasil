package api

import (
	"context"
	"encoding/json"
	"net/http"
	"testing"

	"github.com/brendatamaar/yggdrasil/internal/apperr"
	"github.com/brendatamaar/yggdrasil/internal/core"
)

type fakeComposer struct {
	prompts   []SavedPromptDTO
	err       error
	mentionIn struct {
		project core.ProjectID
		q       string
		limit   int
	}
	draftIn DraftRequest
}

func (f *fakeComposer) SavedPrompts(context.Context) ([]SavedPromptDTO, error) {
	return f.prompts, f.err
}

func (f *fakeComposer) CreatePrompt(_ context.Context, name, text string) (SavedPromptDTO, error) {
	return SavedPromptDTO{ID: "prm_1", Name: name, Text: text}, f.err
}

func (f *fakeComposer) UpdatePrompt(_ context.Context, id, name, text string) (SavedPromptDTO, error) {
	return SavedPromptDTO{ID: id, Name: name, Text: text}, f.err
}

func (f *fakeComposer) DeletePrompt(context.Context, string) error { return f.err }

func (f *fakeComposer) Mentions(_ context.Context, p core.ProjectID, q string, limit int) (MentionsResponse, error) {
	f.mentionIn.project, f.mentionIn.q, f.mentionIn.limit = p, q, limit
	return MentionsResponse{}, f.err
}

func (f *fakeComposer) Models(context.Context) ([]ModelsResponse, error) {
	return []ModelsResponse{{Agent: "claude-code", Models: []ModelDTO{{ID: "haiku", Label: "Haiku"}}, AllowCustom: true}, {Agent: "mock"}}, f.err
}

func (f *fakeComposer) Draft(_ context.Context, r DraftRequest) (DraftResponse, error) {
	f.draftIn = r
	return DraftResponse{Text: "feat: x"}, f.err
}

func composerEnv(t *testing.T, f *fakeComposer) *env {
	return newEnv(t, func(d *Deps) { d.Composer = f })
}

func TestComposerPromptRoutes(t *testing.T) {
	f := &fakeComposer{}
	e := composerEnv(t, f)
	resp, data := e.do(t, "GET", "/api/composer/prompts", nil)
	var list SavedPromptsResponse
	if resp.StatusCode != 200 || json.Unmarshal(data, &list) != nil || list.Prompts == nil {
		t.Fatalf("list %d %s", resp.StatusCode, data)
	}
	resp, data = e.do(t, "POST", "/api/composer/prompts", SavePromptRequest{Name: "n", Text: "t"})
	var p SavedPromptDTO
	if resp.StatusCode != 201 || json.Unmarshal(data, &p) != nil || p.Name != "n" {
		t.Fatalf("create %d %s", resp.StatusCode, data)
	}
	resp, data = e.do(t, "PUT", "/api/composer/prompts/prm_9", UpdatePromptRequest{Name: "m", Text: "u"})
	if resp.StatusCode != 200 || json.Unmarshal(data, &p) != nil || p.ID != "prm_9" || p.Name != "m" {
		t.Fatalf("update %d %s", resp.StatusCode, data)
	}
	if resp, data = e.do(t, "DELETE", "/api/composer/prompts/prm_9", nil); resp.StatusCode != 204 {
		t.Fatalf("delete %d %s", resp.StatusCode, data)
	}
	if resp, _ = e.do(t, "POST", "/api/composer/prompts", map[string]any{"name": "n", "text": "t", "extra": 1}); resp.StatusCode != 400 {
		t.Fatalf("unknown field: %d", resp.StatusCode)
	}
}

func TestComposerErrorsMapByClass(t *testing.T) {
	f := &fakeComposer{err: apperr.New(apperr.NotFound, "That saved prompt doesn't exist (any more).", nil)}
	e := composerEnv(t, f)
	resp, data := e.do(t, "DELETE", "/api/composer/prompts/x", nil)
	var b ErrorBody
	if resp.StatusCode != 404 || json.Unmarshal(data, &b) != nil || b.Message == "" {
		t.Fatalf("%d %s", resp.StatusCode, data)
	}
	f.err = apperr.New(apperr.Invalid, "Give the prompt a name.", nil)
	if resp, _ = e.do(t, "POST", "/api/composer/prompts", SavePromptRequest{}); resp.StatusCode != 400 {
		t.Fatalf("invalid: %d", resp.StatusCode)
	}
	f.err = context.DeadlineExceeded
	if resp, _ = e.do(t, "GET", "/api/composer/prompts", nil); resp.StatusCode != 500 {
		t.Fatalf("unclassified: %d", resp.StatusCode)
	}
}

func TestComposerMentionsParsesQuery(t *testing.T) {
	f := &fakeComposer{}
	e := composerEnv(t, f)
	resp, data := e.do(t, "GET", "/api/composer/mentions?project="+string(e.project.ID)+"&q=main&limit=500", nil)
	var m MentionsResponse
	if resp.StatusCode != 200 || json.Unmarshal(data, &m) != nil || m.Mentions == nil {
		t.Fatalf("%d %s", resp.StatusCode, data)
	}
	if f.mentionIn.q != "main" || f.mentionIn.limit != 50 || f.mentionIn.project != e.project.ID {
		t.Fatalf("input %+v", f.mentionIn)
	}
	for _, q := range []string{"", "?project=a%20b", "?project=" + string(e.project.ID) + "&limit=0", "?project=" + string(e.project.ID) + "&limit=x"} {
		if resp, _ = e.do(t, "GET", "/api/composer/mentions"+q, nil); resp.StatusCode != 400 {
			t.Errorf("query %q: %d", q, resp.StatusCode)
		}
	}
}

func TestComposerModelsFilterByAgent(t *testing.T) {
	e := composerEnv(t, &fakeComposer{})
	var out ModelsListResponse
	resp, data := e.do(t, "GET", "/api/composer/models", nil)
	if resp.StatusCode != 200 || json.Unmarshal(data, &out) != nil || len(out.Agents) != 2 {
		t.Fatalf("%d %s", resp.StatusCode, data)
	}
	resp, data = e.do(t, "GET", "/api/composer/models?agent=mock", nil)
	if resp.StatusCode != 200 || json.Unmarshal(data, &out) != nil || len(out.Agents) != 1 || out.Agents[0].Agent != "mock" {
		t.Fatalf("%d %s", resp.StatusCode, data)
	}
	if resp, _ = e.do(t, "GET", "/api/composer/models?agent=nope", nil); resp.StatusCode != 404 {
		t.Fatalf("unknown agent: %d", resp.StatusCode)
	}
}

func TestComposerDraftValidatesIDs(t *testing.T) {
	f := &fakeComposer{}
	e := composerEnv(t, f)
	ok := DraftRequest{Kind: "commit_message", ProjectID: e.project.ID, RepoID: e.repo.ID, Agent: "mock"}
	resp, data := e.do(t, "POST", "/api/composer/drafts", ok)
	var r DraftResponse
	if resp.StatusCode != 200 || json.Unmarshal(data, &r) != nil || r.Text == "" || f.draftIn.RepoID != e.repo.ID {
		t.Fatalf("%d %s", resp.StatusCode, data)
	}
	bad := ok
	bad.RepoID = "a b"
	if resp, _ = e.do(t, "POST", "/api/composer/drafts", bad); resp.StatusCode != 400 {
		t.Fatalf("bad repo: %d", resp.StatusCode)
	}
	bad = ok
	bad.TaskID = "a b"
	if resp, _ = e.do(t, "POST", "/api/composer/drafts", bad); resp.StatusCode != 400 {
		t.Fatalf("bad task: %d", resp.StatusCode)
	}
}

func TestComposerRoutesNeedHostAndOrigin(t *testing.T) {
	e := composerEnv(t, &fakeComposer{})
	resp, _ := e.do(t, "GET", "/api/composer/prompts", nil, func(r *http.Request) { r.Host = "evil.example" })
	if resp.StatusCode != 403 {
		t.Fatalf("foreign host: %d", resp.StatusCode)
	}
	resp, _ = e.do(t, "POST", "/api/composer/prompts", SavePromptRequest{Name: "n", Text: "t"}, func(r *http.Request) { r.Header.Set("Origin", "http://evil.example") })
	if resp.StatusCode != 403 {
		t.Fatalf("foreign origin: %d", resp.StatusCode)
	}
}
