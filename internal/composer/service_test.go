package composer

import (
	"context"
	"errors"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/brendatamaar/yggdrasil/internal/apperr"
	"github.com/brendatamaar/yggdrasil/internal/db"
	"github.com/brendatamaar/yggdrasil/internal/safety"
)

func newStore(t *testing.T) *db.Store {
	t.Helper()
	d, err := db.Open(context.Background(), filepath.Join(t.TempDir(), "y.db"))
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = d.Close() })
	return db.NewStore(d, nil)
}

func newService(t *testing.T, st *db.Store, mod func(*Config)) *Service {
	t.Helper()
	c := Config{
		Store: st, Env: safety.NewEnvBuilder(), Redactor: safety.NewRedactor(),
		Agents: []string{"claude-code", "mock"},
		Now:    func() time.Time { return time.Now().UTC() }, NewID: db.NewID,
	}
	if mod != nil {
		mod(&c)
	}
	s, err := New(c)
	if err != nil {
		t.Fatal(err)
	}
	return s
}

func class(err error) apperr.Class {
	c, _ := apperr.ClassOf(err)
	return c
}

func TestPromptsCreateUpdateDeleteAreAudited(t *testing.T) {
	ctx := context.Background()
	st := newStore(t)
	s := newService(t, st, nil)
	if list, err := s.SavedPrompts(ctx); err != nil || len(list) != 0 {
		t.Fatalf("empty: %v %v", list, err)
	}
	a, err := s.CreatePrompt(ctx, "Fix tests", "run the tests and fix them")
	if err != nil {
		t.Fatal(err)
	}
	b, err := s.CreatePrompt(ctx, "Explain", "explain this")
	if err != nil {
		t.Fatal(err)
	}
	if a.ID == b.ID || a.ID == "" {
		t.Fatalf("ids %q %q", a.ID, b.ID)
	}
	if _, err := s.UpdatePrompt(ctx, a.ID, "Fix the tests", "new text"); err != nil {
		t.Fatal(err)
	}
	if err := s.DeletePrompt(ctx, b.ID); err != nil {
		t.Fatal(err)
	}
	list, _ := s.SavedPrompts(ctx)
	if len(list) != 1 || list[0].Name != "Fix the tests" || list[0].Text != "new text" {
		t.Fatalf("list %+v", list)
	}
	audits, err := st.ListAudit(ctx, "setting:"+PromptSettingKey, 50)
	if err != nil {
		t.Fatal(err)
	}
	var actions []string
	for _, e := range audits {
		actions = append(actions, e.Action)
		if strings.Contains(string(e.Detail), "new text") {
			t.Errorf("audit holds the prompt text: %s", e.Detail)
		}
	}
	if len(actions) != 4 {
		t.Fatalf("want 4 audit entries (create, create, update, delete), got %v", actions)
	}
}

func TestPromptsErrorClasses(t *testing.T) {
	ctx := context.Background()
	s := newService(t, newStore(t), nil)
	if _, err := s.CreatePrompt(ctx, "", "x"); class(err) != apperr.Invalid {
		t.Errorf("bad name: %v", err)
	}
	if _, err := s.UpdatePrompt(ctx, "nope", "n", "t"); class(err) != apperr.NotFound {
		t.Errorf("update missing: %v", err)
	}
	if err := s.DeletePrompt(ctx, "nope"); class(err) != apperr.NotFound {
		t.Errorf("delete missing: %v", err)
	}
}

func TestPromptsCapAt200(t *testing.T) {
	ctx := context.Background()
	s := newService(t, newStore(t), nil)
	for i := 0; i < MaxPrompts; i++ {
		if _, err := s.CreatePrompt(ctx, "p", "t"); err != nil {
			t.Fatalf("create %d: %v", i, err)
		}
	}
	_, err := s.CreatePrompt(ctx, "p", "t")
	if class(err) != apperr.Conflict {
		t.Fatalf("201st prompt: %v", err)
	}
}

func TestDamagedPromptsAreUnavailableNotSilent(t *testing.T) {
	ctx := context.Background()
	st := newStore(t)
	if err := st.Commit(ctx, func(tx *db.Tx) error {
		_, err := tx.SetSetting(ctx, PromptSettingKey, "{broken", db.SettingNew)
		return err
	}); err != nil {
		t.Fatal(err)
	}
	s := newService(t, st, nil)
	_, err := s.SavedPrompts(ctx)
	if class(err) != apperr.Unavailable || errors.Is(err, db.ErrNotFound) {
		t.Fatalf("damaged: %v", err)
	}
}
