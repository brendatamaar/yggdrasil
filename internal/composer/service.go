package composer

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"time"

	"github.com/brendatamaar/yggdrasil/internal/api"
	"github.com/brendatamaar/yggdrasil/internal/apperr"
	"github.com/brendatamaar/yggdrasil/internal/audit"
	"github.com/brendatamaar/yggdrasil/internal/core"
	"github.com/brendatamaar/yggdrasil/internal/db"
	"github.com/brendatamaar/yggdrasil/internal/safety"
	"github.com/brendatamaar/yggdrasil/internal/tasks"
)

// TaskRunner is what Draft needs of the task service (*tasks.Service).
type TaskRunner interface {
	Message(ctx context.Context, req tasks.MessageRequest) (tasks.MessageResult, error)
	Cancel(ctx context.Context, id core.TaskID) (core.Task, error)
	Dismiss(ctx context.Context, id core.TaskID) (core.Task, error)
}

// Config wires a Service. Store, Env, Redactor, Now and NewID are required;
// Tasks is required for Draft.
type Config struct {
	Store    *db.Store
	Tasks    TaskRunner
	Env      safety.EnvBuilder
	Redactor safety.Redactor
	// Agents are the names of the registered adapters, for Models.
	Agents []string
	Now    func() time.Time
	NewID  func(prefix string) string

	// DraftTimeout bounds one draft (default 120 s). DraftPoll is how often the
	// task is checked (default 100 ms). ScanTimeout bounds the file listing of
	// one repo (default 5 s).
	DraftTimeout time.Duration
	DraftPoll    time.Duration
	ScanTimeout  time.Duration
	// MaxScanFiles caps the files read per repo (default 200000).
	MaxScanFiles int
}

// Service implements api.ComposerService.
type Service struct{ c Config }

// New returns a Service.
func New(c Config) (*Service, error) {
	if c.Store == nil || c.Env == nil || c.Redactor == nil || c.Now == nil || c.NewID == nil {
		return nil, errors.New("composer: store, environment, redactor, clock and id source are required")
	}
	if c.DraftTimeout == 0 {
		c.DraftTimeout = 120 * time.Second
	}
	if c.DraftPoll == 0 {
		c.DraftPoll = 100 * time.Millisecond
	}
	if c.ScanTimeout == 0 {
		c.ScanTimeout = 5 * time.Second
	}
	if c.MaxScanFiles == 0 {
		c.MaxScanFiles = 200_000
	}
	return &Service{c: c}, nil
}

// invalid turns a rejected input into a 400-class error.
func invalid(err error) error {
	var re *RuleError
	if errors.As(err, &re) {
		return apperr.New(apperr.Invalid, re.Message, err)
	}
	return err
}

// loadPrompts returns the saved prompts and the settings version.
func (s *Service) loadPrompts(ctx context.Context) ([]StoredPrompt, int64, error) {
	set, err := s.c.Store.GetSetting(ctx, PromptSettingKey)
	if errors.Is(err, db.ErrNotFound) {
		return []StoredPrompt{}, db.SettingNew, nil
	}
	if err != nil {
		return nil, 0, fmt.Errorf("read saved prompts: %w", err)
	}
	list, err := ParsePrompts(set.Value)
	if err != nil {
		return nil, 0, apperr.New(apperr.Unavailable, "The saved prompts are damaged and can't be read.", err)
	}
	return list, set.Version, nil
}

// SavedPrompts lists the saved prompts.
func (s *Service) SavedPrompts(ctx context.Context) ([]api.SavedPromptDTO, error) {
	list, _, err := s.loadPrompts(ctx)
	if err != nil {
		return nil, err
	}
	out := make([]api.SavedPromptDTO, len(list))
	for i, p := range list {
		out[i] = dto(p)
	}
	return out, nil
}

func dto(p StoredPrompt) api.SavedPromptDTO {
	return api.SavedPromptDTO{ID: p.ID, Name: p.Name, Text: p.Text}
}

// CreatePrompt adds a prompt.
func (s *Service) CreatePrompt(ctx context.Context, name, text string) (api.SavedPromptDTO, error) {
	p, err := ParsePrompt(name, text)
	if err != nil {
		return api.SavedPromptDTO{}, invalid(err)
	}
	var out StoredPrompt
	err = s.mutate(ctx, "composer.prompt.create", func(list []StoredPrompt) ([]StoredPrompt, StoredPrompt, error) {
		if len(list) >= MaxPrompts {
			return nil, StoredPrompt{}, apperr.New(apperr.Conflict, fmt.Sprintf("You can keep at most %d saved prompts. Delete one first.", MaxPrompts), nil)
		}
		out = StoredPrompt{ID: s.c.NewID("prm"), Name: p.Name, Text: p.Text}
		return append(list, out), out, nil
	})
	return dto(out), err
}

// UpdatePrompt replaces the name and text of a prompt.
func (s *Service) UpdatePrompt(ctx context.Context, id, name, text string) (api.SavedPromptDTO, error) {
	p, err := ParsePrompt(name, text)
	if err != nil {
		return api.SavedPromptDTO{}, invalid(err)
	}
	var out StoredPrompt
	err = s.mutate(ctx, "composer.prompt.update", func(list []StoredPrompt) ([]StoredPrompt, StoredPrompt, error) {
		for i := range list {
			if list[i].ID == id {
				list[i] = StoredPrompt{ID: id, Name: p.Name, Text: p.Text}
				out = list[i]
				return list, out, nil
			}
		}
		return nil, StoredPrompt{}, notFoundPrompt()
	})
	return dto(out), err
}

// DeletePrompt removes a saved prompt (a value in the settings row, not a file).
func (s *Service) DeletePrompt(ctx context.Context, id string) error {
	return s.mutate(ctx, "composer.prompt.delete", func(list []StoredPrompt) ([]StoredPrompt, StoredPrompt, error) {
		for i := range list {
			if list[i].ID == id {
				gone := list[i]
				return append(list[:i:i], list[i+1:]...), gone, nil
			}
		}
		return nil, StoredPrompt{}, notFoundPrompt()
	})
}

func notFoundPrompt() error {
	return apperr.New(apperr.NotFound, "That saved prompt doesn't exist (any more).", nil)
}

// mutate applies f to the current list and stores the result with its audit
// entry in one commit. A concurrent change (stale version) is retried on the
// fresh list a few times.
func (s *Service) mutate(ctx context.Context, action string, f func([]StoredPrompt) ([]StoredPrompt, StoredPrompt, error)) error {
	for try := 0; try < 4; try++ {
		list, ver, err := s.loadPrompts(ctx)
		if err != nil {
			return err
		}
		next, subject, err := f(append([]StoredPrompt(nil), list...))
		if err != nil {
			return err
		}
		raw, err := FormatPrompts(next)
		if err != nil {
			return err
		}
		// The audit entry names the prompt, never its text.
		detail, err := json.Marshal(map[string]string{"id": subject.ID, "name": subject.Name})
		if err != nil {
			return fmt.Errorf("encode audit detail: %w", err)
		}
		e, err := audit.Prepare(core.AuditEntry{Actor: "user", Action: action, Subject: "setting:" + PromptSettingKey, Detail: detail},
			s.c.Redactor, core.AuditID(s.c.NewID("aud")), s.c.Now())
		if err != nil {
			return err
		}
		err = s.c.Store.Commit(ctx, func(tx *db.Tx) error {
			if _, err := tx.SetSetting(ctx, PromptSettingKey, raw, ver); err != nil {
				return err
			}
			return tx.AppendAudit(ctx, e)
		})
		if errors.Is(err, db.ErrConflict) {
			continue
		}
		if err != nil {
			return fmt.Errorf("save prompts: %w", err)
		}
		return nil
	}
	return apperr.New(apperr.Conflict, "The saved prompts changed in the meantime. Reload and try again.", nil)
}
