//go:build !windows

package platform

import (
	"encoding/json"
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"slices"
	"strings"
	"syscall"
	"time"
)

// orphanRecord is the content of a pid file. Start identifies the process
// instance (its start time), so a pid that the OS has reused for another
// program is not mistaken for the recorded one.
type orphanRecord struct {
	Pid   int    `json:"pid"`
	Start string `json:"start"`
}

const (
	// orphanDir is the folder inside the data directory that holds the pid files.
	orphanDir        = "orphans"
	orphanExt        = ".json"
	orphanKillWait   = 3 * time.Second
	orphanPollPeriod = 20 * time.Millisecond
)

func orphanPath(dataDir, id string) string {
	return filepath.Join(dataDir, orphanDir, id+orphanExt)
}

// RecordProcess writes the pid file for id (docs: DV-03). The file holds the
// pid and the process start time. Start returns processes that lead their own
// process group, so StopOrphan can kill the whole tree later.
func RecordProcess(dataDir, id string, pid int) error {
	if err := CheckProcessID(id); err != nil {
		return err
	}
	if pid <= 1 {
		return fmt.Errorf("platform: cannot record process id %d", pid)
	}
	start, err := processStart(pid)
	if err != nil {
		return fmt.Errorf("record process %s: %w", id, err)
	}
	data, err := json.Marshal(orphanRecord{Pid: pid, Start: start})
	if err != nil {
		return fmt.Errorf("record process %s: %w", id, err)
	}
	path := orphanPath(dataDir, id)
	if err := os.MkdirAll(filepath.Dir(path), 0o700); err != nil {
		return fmt.Errorf("record process %s: %w", id, err)
	}
	tmp := path + ".tmp"
	if err := os.WriteFile(tmp, data, 0o600); err != nil {
		return fmt.Errorf("record process %s: %w", id, err)
	}
	if err := os.Rename(tmp, path); err != nil {
		_ = os.Remove(tmp) // our own temp file
		return fmt.Errorf("record process %s: %w", id, err)
	}
	return nil
}

// ForgetProcess removes the pid file for id. A missing file is not an error.
func ForgetProcess(dataDir, id string) error {
	if err := CheckProcessID(id); err != nil {
		return err
	}
	if err := os.Remove(orphanPath(dataDir, id)); err != nil && !errors.Is(err, os.ErrNotExist) {
		return fmt.Errorf("forget process %s: %w", id, err)
	}
	return nil
}

// ListOrphans returns the recorded processes that are still the recorded
// ones, sorted by id. A pid file whose process has ended (or whose pid now
// belongs to another program) is stale; it is removed, since it is a file we
// wrote and it no longer describes anything. Files that are not valid pid files
// are left alone.
func ListOrphans(dataDir string) ([]Orphan, error) {
	entries, err := os.ReadDir(filepath.Join(dataDir, orphanDir))
	if errors.Is(err, os.ErrNotExist) {
		return nil, nil
	}
	if err != nil {
		return nil, fmt.Errorf("list orphans: %w", err)
	}
	var out []Orphan
	for _, e := range entries {
		id, ok := strings.CutSuffix(e.Name(), orphanExt)
		if !ok || e.IsDir() || CheckProcessID(id) != nil {
			continue
		}
		rec, err := readOrphan(dataDir, id)
		if err != nil {
			continue
		}
		if !isRecorded(rec) {
			_ = os.Remove(orphanPath(dataDir, id)) // stale pid file we wrote
			continue
		}
		out = append(out, Orphan{ID: id, Pid: rec.Pid})
	}
	slices.SortFunc(out, func(a, b Orphan) int { return strings.Compare(a.ID, b.ID) })
	return out, nil
}

// StopOrphan kills the process tree recorded for id, after checking that the
// pid is still the recorded process. If the process is gone or the pid was
// reused, nothing is killed and the stale pid file is removed. The tree is the
// process group (Start gives every process its own); if the process does not
// lead a group, only that process is killed.
func StopOrphan(dataDir, id string) error {
	if err := CheckProcessID(id); err != nil {
		return err
	}
	rec, err := readOrphan(dataDir, id)
	if errors.Is(err, os.ErrNotExist) {
		return nil
	}
	if err != nil {
		return fmt.Errorf("stop orphan %s: %w", id, err)
	}
	if isRecorded(rec) {
		target := rec.Pid
		if pgid, err := syscall.Getpgid(rec.Pid); err == nil && pgid == rec.Pid {
			target = -rec.Pid
		}
		if err := syscall.Kill(target, syscall.SIGKILL); err != nil && !errors.Is(err, syscall.ESRCH) {
			return fmt.Errorf("stop orphan %s: %w", id, err)
		}
		if !waitGone(rec, orphanKillWait) {
			return fmt.Errorf("stop orphan %s: process %d is still running", id, rec.Pid)
		}
	}
	return ForgetProcess(dataDir, id)
}

func readOrphan(dataDir, id string) (orphanRecord, error) {
	data, err := os.ReadFile(orphanPath(dataDir, id)) //nolint:gosec // id is validated by CheckProcessID
	if err != nil {
		return orphanRecord{}, err
	}
	var rec orphanRecord
	if err := json.Unmarshal(data, &rec); err != nil {
		return orphanRecord{}, fmt.Errorf("read pid file for %s: %w", id, err)
	}
	if rec.Pid <= 1 || rec.Start == "" {
		return orphanRecord{}, fmt.Errorf("read pid file for %s: incomplete record", id)
	}
	return rec, nil
}

// isRecorded reports whether rec.Pid is running and is the same process
// instance that was recorded.
func isRecorded(rec orphanRecord) bool {
	start, err := processStart(rec.Pid)
	return err == nil && start == rec.Start
}

func waitGone(rec orphanRecord, timeout time.Duration) bool {
	deadline := time.Now().Add(timeout)
	for {
		if !isRecorded(rec) {
			return true
		}
		if time.Now().After(deadline) {
			return false
		}
		time.Sleep(orphanPollPeriod)
	}
}
