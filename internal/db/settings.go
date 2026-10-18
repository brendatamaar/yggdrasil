package db

import (
	"context"
	"fmt"
	"strings"

	"github.com/brendatamaar/yggdrasil/internal/db/gen"
)

// Setting is one user setting, such as the editor command (WS-08). Settings
// hold no secrets. Version is for expected-version updates.
type Setting struct {
	Key     string
	Value   string
	Version int64
}

// SettingNew is the expected version for SetSetting when the key must not
// exist yet.
const SettingNew int64 = -1

func checkSettingKey(key string) error {
	if key == "" || strings.ContainsFunc(key, func(r rune) bool { return r <= ' ' || r == 0x7f }) {
		return fmt.Errorf("invalid setting key %q", key)
	}
	return nil
}

// GetSetting returns one setting, or ErrNotFound.
func (s *Store) GetSetting(ctx context.Context, key string) (Setting, error) {
	r, err := s.db.reader().GetSetting(ctx, key)
	if err := found("get setting "+key, err); err != nil {
		return Setting{}, err
	}
	return Setting{Key: r.Key, Value: r.Value, Version: r.Version}, nil
}

// SetSetting stores value if the setting is still at expectedVersion and
// returns the new version. Pass SettingNew to create a setting: it fails with
// ErrConflict if the key exists. A stale version is ErrConflict, a missing
// key ErrNotFound.
func (tx *Tx) SetSetting(ctx context.Context, key, value string, expectedVersion int64) (int64, error) {
	if err := checkSettingKey(key); err != nil {
		return 0, err
	}
	if expectedVersion == SettingNew {
		if err := tx.q.InsertSetting(ctx, gen.InsertSettingParams{Key: key, Value: value}); err != nil {
			return 0, fmt.Errorf("create setting %s: %w", key, err)
		}
		return 0, nil
	}
	n, err := tx.q.UpdateSetting(ctx, gen.UpdateSettingParams{Value: value, Key: key, Version: expectedVersion})
	return bumped("update setting "+key, n, err, expectedVersion, func() error {
		_, err := tx.q.GetSetting(ctx, key)
		return err
	})
}
