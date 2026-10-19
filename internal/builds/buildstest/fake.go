// Package buildstest has an in-memory builds.BuildStore and the contract
// suite both it and the SQLite store must pass.
package buildstest

import (
	"context"
	"fmt"
	"maps"
	"slices"
	"strings"
	"sync"
	"time"

	"github.com/brendatamaar/yggdrasil/internal/builds"
	"github.com/brendatamaar/yggdrasil/internal/core"
	corebuilds "github.com/brendatamaar/yggdrasil/internal/core/builds"
	"github.com/brendatamaar/yggdrasil/internal/db"
)

type buildRec struct {
	state   corebuilds.State
	version int64
}

type presetRec struct {
	p       corebuilds.Preset
	version int64
}

type data struct {
	builds  map[core.BuildID]buildRec
	presets map[corebuilds.PresetID]presetRec
	audit   []core.AuditEntry
}

func (d data) clone() data {
	return data{builds: maps.Clone(d.builds), presets: maps.Clone(d.presets), audit: slices.Clone(d.audit)}
}

// Fake is an in-memory builds.BuildStore. Commit works on a copy and keeps it
// only when fn succeeds, so a failed Commit leaves no trace.
type Fake struct {
	mu          sync.Mutex
	d           data
	repoProject map[core.RepoID]core.ProjectID
}

// NewFake returns an empty store. repoProject says which project owns each
// repo, for the project filter of List.
func NewFake(repoProject map[core.RepoID]core.ProjectID) *Fake {
	return &Fake{d: data{builds: map[core.BuildID]buildRec{}, presets: map[corebuilds.PresetID]presetRec{}}, repoProject: repoProject}
}

// Audit returns the committed audit entries.
func (f *Fake) Audit() []core.AuditEntry {
	f.mu.Lock()
	defer f.mu.Unlock()
	return slices.Clone(f.d.audit)
}

type fakeTx struct{ d *data }

// Commit implements builds.BuildStore.
func (f *Fake) Commit(_ context.Context, fn func(builds.BuildTx) error) error {
	f.mu.Lock()
	defer f.mu.Unlock()
	work := f.d.clone()
	if err := fn(&fakeTx{d: &work}); err != nil {
		return err
	}
	f.d = work
	return nil
}

func notFound(what string) error { return fmt.Errorf("%s: %w", what, db.ErrNotFound) }
func conflict(what string) error { return fmt.Errorf("%s: %w", what, db.ErrConflict) }

func (t *fakeTx) LoadBuild(_ context.Context, id core.BuildID) (corebuilds.State, int64, error) {
	r, ok := t.d.builds[id]
	if !ok {
		return nil, 0, notFound("load build " + string(id))
	}
	return r.state, r.version, nil
}

// common is the data every stored state has.
func common(s corebuilds.State) (id core.BuildID, spec corebuilds.Spec, at time.Time, ok bool) {
	switch st := s.(type) {
	case corebuilds.Queued:
		return st.ID, st.Spec, st.QueuedAt, true
	case corebuilds.Running:
		return st.ID, st.Spec, st.QueuedAt, true
	case corebuilds.Cancelling:
		return st.ID, st.Spec, st.QueuedAt, true
	case corebuilds.Finished:
		return st.ID, st.Spec, st.QueuedAt, true
	}
	return "", corebuilds.Spec{}, time.Time{}, false
}

func (t *fakeTx) SaveBuild(_ context.Context, s corebuilds.State, expected int64) (int64, error) {
	id, spec, _, ok := common(s)
	if !ok {
		return 0, fmt.Errorf("a %T has no stored form", s)
	}
	if err := spec.Validate(); err != nil {
		return 0, err
	}
	cur, exists := t.d.builds[id]
	if expected == db.NewBuild {
		if exists {
			return 0, conflict("save build " + string(id))
		}
		t.d.builds[id] = buildRec{state: s}
		return 0, nil
	}
	if !exists {
		return 0, notFound("save build " + string(id))
	}
	if cur.version != expected {
		return 0, conflict("save build " + string(id))
	}
	t.d.builds[id] = buildRec{state: s, version: expected + 1}
	return expected + 1, nil
}

func (t *fakeTx) AppendAudit(_ context.Context, e core.AuditEntry) error {
	if err := e.ID.Validate(); err != nil {
		return err
	}
	if slices.ContainsFunc(t.d.audit, func(x core.AuditEntry) bool { return x.ID == e.ID }) {
		return conflict("audit entry " + string(e.ID))
	}
	t.d.audit = append(t.d.audit, e)
	return nil
}

func (t *fakeTx) nameTaken(p corebuilds.Preset) bool {
	for id, r := range t.d.presets {
		if id != p.ID && r.p.ProjectID == p.ProjectID && r.p.Name == p.Name {
			return true
		}
	}
	return false
}

func (t *fakeTx) InsertPreset(_ context.Context, p corebuilds.Preset, _ time.Time) error {
	if err := p.Validate(); err != nil {
		return err
	}
	if _, ok := t.d.presets[p.ID]; ok || t.nameTaken(p) {
		return conflict("insert build preset " + string(p.ID))
	}
	t.d.presets[p.ID] = presetRec{p: p}
	return nil
}

func (t *fakeTx) UpdatePreset(_ context.Context, p corebuilds.Preset, expected int64) (int64, error) {
	if err := p.Validate(); err != nil {
		return 0, err
	}
	cur, ok := t.d.presets[p.ID]
	if !ok {
		return 0, notFound("update build preset " + string(p.ID))
	}
	if cur.version != expected || t.nameTaken(p) {
		return 0, conflict("update build preset " + string(p.ID))
	}
	t.d.presets[p.ID] = presetRec{p: p, version: expected + 1}
	return expected + 1, nil
}

func (f *Fake) snapshot() data {
	f.mu.Lock()
	defer f.mu.Unlock()
	return f.d.clone()
}

func (f *Fake) filtered(keep func(corebuilds.State) bool, oldestFirst bool) []corebuilds.State {
	var out []corebuilds.State
	for _, r := range f.snapshot().builds {
		if keep(r.state) {
			out = append(out, r.state)
		}
	}
	slices.SortFunc(out, func(a, b corebuilds.State) int {
		ia, _, ta, _ := common(a)
		ib, _, tb, _ := common(b)
		c := ta.Compare(tb)
		if c == 0 {
			c = strings.Compare(string(ia), string(ib))
		}
		if oldestFirst {
			return c
		}
		return -c
	})
	return out
}

// ListQueued implements builds.BuildStore.
func (f *Fake) ListQueued(context.Context) ([]corebuilds.QueuedRef, error) {
	out := []corebuilds.QueuedRef{}
	for _, s := range f.filtered(func(s corebuilds.State) bool { _, ok := s.(corebuilds.Queued); return ok }, true) {
		q := s.(corebuilds.Queued)
		out = append(out, corebuilds.QueuedRef{ID: q.ID, QueuedAt: q.QueuedAt})
	}
	return out, nil
}

func active(s corebuilds.State) bool {
	switch s.(type) {
	case corebuilds.Running, corebuilds.Cancelling:
		return true
	}
	return false
}

// CountRunning implements builds.BuildStore.
func (f *Fake) CountRunning(context.Context) (int, error) {
	return len(f.filtered(active, true)), nil
}

// ListActive implements builds.BuildStore.
func (f *Fake) ListActive(context.Context) ([]corebuilds.State, error) {
	return f.filtered(active, true), nil
}

// Get implements builds.BuildStore.
func (f *Fake) Get(_ context.Context, id core.BuildID) (corebuilds.State, int64, error) {
	r, ok := f.snapshot().builds[id]
	if !ok {
		return nil, 0, notFound("get build " + string(id))
	}
	return r.state, r.version, nil
}

// List implements builds.BuildStore.
func (f *Fake) List(_ context.Context, fl db.BuildFilter) ([]corebuilds.State, error) {
	if fl.Status != "" {
		if err := fl.Status.Validate(); err != nil {
			return nil, err
		}
	}
	limit := fl.Limit
	if limit <= 0 {
		limit = 200
	}
	limit = min(limit, 1000)
	out := f.filtered(func(s corebuilds.State) bool {
		if fl.Status != "" && corebuilds.Status(s) != fl.Status {
			return false
		}
		_, spec, _, _ := common(s)
		for _, st := range spec.Steps {
			if (fl.Repo == "" || st.RepoID == fl.Repo) && (fl.Project == "" || f.repoProject[st.RepoID] == fl.Project) {
				return true
			}
		}
		return false
	}, false)
	if len(out) > limit {
		out = out[:limit]
	}
	return out, nil
}

// GetPreset implements builds.BuildStore.
func (f *Fake) GetPreset(_ context.Context, id corebuilds.PresetID) (corebuilds.Preset, int64, error) {
	r, ok := f.snapshot().presets[id]
	if !ok {
		return corebuilds.Preset{}, 0, notFound("get build preset " + string(id))
	}
	return r.p, r.version, nil
}

// ListPresets implements builds.BuildStore.
func (f *Fake) ListPresets(_ context.Context, project core.ProjectID) ([]corebuilds.Preset, error) {
	out := []corebuilds.Preset{}
	for _, r := range f.snapshot().presets {
		if r.p.ProjectID == project {
			out = append(out, r.p)
		}
	}
	slices.SortFunc(out, func(a, b corebuilds.Preset) int {
		if c := strings.Compare(a.Name, b.Name); c != 0 {
			return c
		}
		return strings.Compare(string(a.ID), string(b.ID))
	})
	return out, nil
}

var _ builds.BuildStore = (*Fake)(nil)
