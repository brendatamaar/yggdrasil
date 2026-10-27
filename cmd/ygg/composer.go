package main

import (
	"log/slog"
	"maps"
	"slices"
	"time"

	"github.com/brendatamaar/yggdrasil/internal/api"
	"github.com/brendatamaar/yggdrasil/internal/composer"
	"github.com/brendatamaar/yggdrasil/internal/db"
	"github.com/brendatamaar/yggdrasil/internal/git"
	"github.com/brendatamaar/yggdrasil/internal/safety"
	"github.com/brendatamaar/yggdrasil/internal/tasks"
)

// newComposer builds the composer backend (saved prompts, @ mentions, models,
// drafts). It returns nil, which the API answers with "not available", when
// the service can't be built.
func newComposer(store *db.Store, adapters adapterRegistry, taskSvc *tasks.Service, g *git.Git, redactor safety.Redactor, log *slog.Logger) api.ComposerService {
	svc, err := composer.New(composer.Config{
		Store: store, Tasks: taskSvc, Git: g, Env: safety.NewEnvBuilder(), Redactor: redactor,
		Agents: slices.Sorted(maps.Keys(adapters)),
		Now:    func() time.Time { return time.Now().UTC() }, NewID: db.NewID,
	})
	if err != nil {
		log.Error("start the composer", "err", err)
		return nil
	}
	return svc
}
