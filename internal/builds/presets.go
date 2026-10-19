package builds

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"

	"github.com/brendatamaar/yggdrasil/internal/apperr"
	"github.com/brendatamaar/yggdrasil/internal/core"
	corebuilds "github.com/brendatamaar/yggdrasil/internal/core/builds"
	"github.com/brendatamaar/yggdrasil/internal/db"
)

var errPresetName = apperr.New(apperr.Conflict, "A preset with that name already exists in this project. Pick another name.", db.ErrConflict)

// ListPresets returns a project's presets.
func (s *Service) ListPresets(ctx context.Context, project core.ProjectID) ([]corebuilds.Preset, error) {
	if err := project.Validate(); err != nil {
		return nil, invalid("That isn't a project ID.", err)
	}
	ps, err := s.d.Store.ListPresets(ctx, project)
	if err != nil {
		return nil, storeErr(err, "preset list")
	}
	return ps, nil
}

// CreatePreset parses a preset body (projectId, name, steps), stores it and
// audits it in one transaction. A duplicate name in the project is a conflict.
func (s *Service) CreatePreset(ctx context.Context, raw []byte, actor corebuilds.Actor) (corebuilds.Preset, error) {
	if err := actor.Validate(); err != nil {
		return corebuilds.Preset{}, invalid("The preset has no valid actor.", err)
	}
	fields, err := objectFields(raw)
	if err != nil {
		return corebuilds.Preset{}, err
	}
	id := corebuilds.PresetID(s.d.IDs("pre"))
	fields["id"] = json.RawMessage(strconvQuote(string(id)))
	merged, err := json.Marshal(fields)
	if err != nil {
		return corebuilds.Preset{}, invalid("The request is not valid.", err)
	}
	p, err := ParsePreset(merged)
	if err != nil {
		return corebuilds.Preset{}, err
	}
	now := s.d.Clock()
	err = s.d.Store.Commit(ctx, func(tx BuildTx) error {
		if err := tx.InsertPreset(ctx, p, now); err != nil {
			return err
		}
		return s.auditTx(ctx, tx, actor, "build.preset.create", p, map[string]string{"name": p.Name})
	})
	if err != nil {
		return corebuilds.Preset{}, presetErr(err)
	}
	return p, nil
}

// UpdatePreset changes the name and/or steps of a preset. patch holds only
// "name" and "steps".
func (s *Service) UpdatePreset(ctx context.Context, id corebuilds.PresetID, patch []byte, actor corebuilds.Actor) (corebuilds.Preset, error) {
	if err := id.Validate(); err != nil {
		return corebuilds.Preset{}, invalid("That isn't a preset ID.", err)
	}
	if err := actor.Validate(); err != nil {
		return corebuilds.Preset{}, invalid("The preset has no valid actor.", err)
	}
	over, err := objectFields(patch)
	if err != nil {
		return corebuilds.Preset{}, err
	}
	for k := range over {
		if k != "name" && k != "steps" {
			return corebuilds.Preset{}, invalid(fmt.Sprintf("You can only change the name and the steps of a preset, not %q.", k), nil)
		}
	}
	cur, ver, err := s.d.Store.GetPreset(ctx, id)
	if err != nil {
		return corebuilds.Preset{}, storeErr(err, "preset")
	}
	base, err := FormatPreset(cur)
	if err != nil {
		return corebuilds.Preset{}, fmt.Errorf("format preset: %w", err)
	}
	fields, err := objectFields(base)
	if err != nil {
		return corebuilds.Preset{}, err
	}
	for k, v := range over {
		fields[k] = v
	}
	merged, err := json.Marshal(fields)
	if err != nil {
		return corebuilds.Preset{}, invalid("The request is not valid.", err)
	}
	p, err := ParsePreset(merged)
	if err != nil {
		return corebuilds.Preset{}, err
	}
	err = s.d.Store.Commit(ctx, func(tx BuildTx) error {
		if _, err := tx.UpdatePreset(ctx, p, ver); err != nil {
			return err
		}
		return s.auditTx(ctx, tx, actor, "build.preset.update", p, map[string]string{"name": p.Name})
	})
	if err != nil {
		return corebuilds.Preset{}, presetErr(err)
	}
	return p, nil
}

// RunPreset enqueues a build of a preset in the main checkouts. The repos
// must still be tracked.
func (s *Service) RunPreset(ctx context.Context, id corebuilds.PresetID, actor corebuilds.Actor) (core.BuildID, error) {
	if err := id.Validate(); err != nil {
		return "", invalid("That isn't a preset ID.", err)
	}
	p, _, err := s.d.Store.GetPreset(ctx, id)
	if err != nil {
		return "", storeErr(err, "preset")
	}
	known, err := s.d.Locator.Known(ctx)
	if err != nil {
		return "", unavailable("Yggdrasil couldn't read the tracked repos. Try again.", err)
	}
	req, err := FormatRequest(corebuilds.Spec{Target: corebuilds.MainCheckout{}, Steps: p.Steps, PresetID: p.ID})
	if err != nil {
		return "", fmt.Errorf("format preset run: %w", err)
	}
	spec, err := ParseRequest(req, known)
	if err != nil {
		return "", err
	}
	return s.Enqueue(ctx, spec, actor)
}

func (s *Service) auditTx(ctx context.Context, tx BuildTx, actor corebuilds.Actor, action string, p corebuilds.Preset, detail map[string]string) error {
	return s.appendAudit(ctx, tx, corebuilds.WriteAudit{Actor: actor, Action: action, Subject: "preset:" + string(p.ID), Detail: detail})
}

func presetErr(err error) error {
	switch {
	case errors.Is(err, db.ErrConflict):
		return errPresetName
	case errors.Is(err, db.ErrNotFound):
		return apperr.New(apperr.NotFound, "That preset or project doesn't exist.", err)
	}
	return unavailable("Yggdrasil couldn't save the preset. Try again.", err)
}

func objectFields(raw []byte) (map[string]json.RawMessage, error) {
	if len(raw) > maxRequestBytes {
		return nil, invalid("The request is too large. Send at most 1 MiB.", nil)
	}
	var m map[string]json.RawMessage
	if err := json.Unmarshal(raw, &m); err != nil || m == nil {
		return nil, invalid("The request must be a JSON object.", err)
	}
	return m, nil
}

func strconvQuote(s string) string {
	b, _ := json.Marshal(s)
	return string(b)
}
