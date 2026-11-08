//go:build !windows

package platform

import (
	"context"
	"encoding/json"
	"os"
	"os/exec"
	"path/filepath"
	"strconv"
	"testing"
)

func startSleeper(t *testing.T) Process {
	t.Helper()
	ctx, cancel := context.WithCancel(context.Background())
	t.Cleanup(cancel)
	p, err := Start(ctx, helperSpec(t, "sleep"))
	if err != nil {
		t.Fatal(err)
	}
	return p
}

func TestOrphanRecordFileHoldsPidAndStart(t *testing.T) {
	dataDir := filepath.Join(t.TempDir(), "data dir")
	p := startSleeper(t)
	if err := RecordProcess(dataDir, "dev1", p.Pid()); err != nil {
		t.Fatal(err)
	}
	data, err := os.ReadFile(filepath.Join(dataDir, orphanDir, "dev1.json"))
	if err != nil {
		t.Fatal(err)
	}
	var rec orphanRecord
	if err := json.Unmarshal(data, &rec); err != nil {
		t.Fatal(err)
	}
	if rec.Pid != p.Pid() || rec.Start == "" {
		t.Errorf("record = %+v", rec)
	}
}

// A pid file of a process that has ended is stale: it is not an orphan and its
// file (which we wrote) is removed.
func TestListOrphansDropsEndedProcesses(t *testing.T) {
	dataDir := t.TempDir()
	p := startSleeper(t)
	if err := RecordProcess(dataDir, "dev1", p.Pid()); err != nil {
		t.Fatal(err)
	}
	_ = p.Kill()
	_, _ = p.Wait()
	got, err := ListOrphans(dataDir)
	if err != nil || len(got) != 0 {
		t.Fatalf("ListOrphans = %v, %v", got, err)
	}
	if _, err := os.Stat(filepath.Join(dataDir, orphanDir, "dev1.json")); !os.IsNotExist(err) {
		t.Errorf("stale pid file still there: %v", err)
	}
}

// The pid is alive but belongs to another instance than the recorded one (the
// OS reused it): nothing is listed and nothing is killed.
func TestPidReuseIsNotKilled(t *testing.T) {
	dataDir := t.TempDir()
	p := startSleeper(t)
	if err := RecordProcess(dataDir, "dev1", p.Pid()); err != nil {
		t.Fatal(err)
	}
	// Rewrite the record as if it had been made for an older process with this pid.
	path := filepath.Join(dataDir, orphanDir, "dev1.json")
	data, _ := json.Marshal(orphanRecord{Pid: p.Pid(), Start: "an-older-start-time"})
	if err := os.WriteFile(path, data, 0o600); err != nil {
		t.Fatal(err)
	}
	if got, _ := ListOrphans(dataDir); len(got) != 0 {
		t.Fatalf("reused pid listed as orphan: %v", got)
	}
	if !alive(p.Pid()) {
		t.Fatal("ListOrphans killed a reused pid")
	}

	if err := RecordProcess(dataDir, "dev2", p.Pid()); err != nil {
		t.Fatal(err)
	}
	path2 := filepath.Join(dataDir, orphanDir, "dev2.json")
	if err := os.WriteFile(path2, data, 0o600); err != nil {
		t.Fatal(err)
	}
	if err := StopOrphan(dataDir, "dev2"); err != nil {
		t.Fatal(err)
	}
	if !alive(p.Pid()) {
		t.Fatal("StopOrphan killed a process that is not the recorded one")
	}
	if _, err := os.Stat(path2); !os.IsNotExist(err) {
		t.Errorf("pid file of the reused pid not removed: %v", err)
	}
}

// Only the pid files we wrote are ever touched: foreign and corrupt files in
// the folder, and the data directory itself, stay.
func TestOrphansNeverDeleteForeignFiles(t *testing.T) {
	dataDir := t.TempDir()
	dir := filepath.Join(dataDir, orphanDir)
	if err := os.MkdirAll(dir, 0o750); err != nil {
		t.Fatal(err)
	}
	keep := map[string]string{
		filepath.Join(dir, "notes.txt"):    "mine",
		filepath.Join(dir, "corrupt.json"): "{not json",
		filepath.Join(dir, "empty.json"):   "{}",
		filepath.Join(dataDir, "ygg.db"):   "database",
	}
	for p, c := range keep {
		if err := os.WriteFile(p, []byte(c), 0o600); err != nil {
			t.Fatal(err)
		}
	}
	if err := os.Mkdir(filepath.Join(dir, "sub.json"), 0o750); err != nil {
		t.Fatal(err)
	}
	p := startSleeper(t)
	if err := RecordProcess(dataDir, "dev1", p.Pid()); err != nil {
		t.Fatal(err)
	}
	got, err := ListOrphans(dataDir)
	if err != nil || len(got) != 1 || got[0].ID != "dev1" {
		t.Fatalf("ListOrphans = %v, %v", got, err)
	}
	if err := StopOrphan(dataDir, "dev1"); err != nil {
		t.Fatal(err)
	}
	if err := ForgetProcess(dataDir, "dev1"); err != nil {
		t.Fatal(err)
	}
	for p, c := range keep {
		data, err := os.ReadFile(p) //nolint:gosec // test path
		if err != nil || string(data) != c {
			t.Errorf("%s changed: %q, %v", p, data, err)
		}
	}
	if fi, err := os.Stat(filepath.Join(dir, "sub.json")); err != nil || !fi.IsDir() {
		t.Errorf("folder sub.json changed: %v", err)
	}
}

func TestListOrphansWithoutFolder(t *testing.T) {
	got, err := ListOrphans(filepath.Join(t.TempDir(), "never-created"))
	if err != nil || len(got) != 0 {
		t.Fatalf("got %v, %v", got, err)
	}
}

func TestRecordProcessRefusesBadInput(t *testing.T) {
	dataDir := t.TempDir()
	if err := RecordProcess(dataDir, "dev1", 0); err == nil {
		t.Error("pid 0 accepted")
	}
	if err := RecordProcess(dataDir, "dev1", 2147483646); err == nil {
		t.Error("pid of a process that does not run accepted")
	}
	if err := RecordProcess(dataDir, "../escape", os.Getpid()); err == nil {
		t.Error("id with a path accepted")
	}
	if _, err := os.Stat(filepath.Join(dataDir, "escape.json")); err == nil {
		t.Error("file written outside the pid folder")
	}
}

// StopOrphan kills the whole tree: the recorded process and its grandchild.
func TestStopOrphanKillsTree(t *testing.T) {
	dataDir := t.TempDir()
	file := filepath.Join(t.TempDir(), "gc")
	p, err := Start(context.Background(), helperSpec(t, "child", file))
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = p.Kill() })
	gc, err := strconv.Atoi(waitFile(t, file))
	if err != nil {
		t.Fatal(err)
	}
	if err := RecordProcess(dataDir, "dev1", p.Pid()); err != nil {
		t.Fatal(err)
	}
	if err := StopOrphan(dataDir, "dev1"); err != nil {
		t.Fatal(err)
	}
	if !waitDead(p.Pid()) || !waitDead(gc) {
		t.Fatalf("tree survived StopOrphan (child alive: %v, grandchild alive: %v)", alive(p.Pid()), alive(gc))
	}
	if got, _ := ListOrphans(dataDir); len(got) != 0 {
		t.Errorf("ListOrphans after Stop = %v", got)
	}
}

// Unix has no job object: when the host is killed, its dev-server tree
// survives, and the pid files are what lets the next run find and stop it
// (DV-03). The Windows counterpart is TestDevServerTreeDiesWhenHostIsKilled.
func TestDevServerTreeOutlivesKilledHostAndIsFoundAsOrphan(t *testing.T) {
	dataDir := filepath.Join(t.TempDir(), "data dir")
	out := filepath.Join(t.TempDir(), "host")
	exe, err := os.Executable()
	if err != nil {
		t.Fatal(err)
	}
	host := exec.Command(exe, "ygg-dev", out, dataDir)
	host.Env = helperEnv()
	if err := host.Start(); err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = host.Process.Kill(); _ = host.Wait() })
	server, err := strconv.Atoi(waitFile(t, out))
	if err != nil {
		t.Fatal(err)
	}
	gc, err := strconv.Atoi(waitFile(t, out+".gc"))
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() {
		// Whatever happens, do not leave the tree behind.
		_ = StopOrphan(dataDir, "dev1")
	})

	if err := host.Process.Kill(); err != nil {
		t.Fatal(err)
	}
	_ = host.Wait()
	if !alive(server) || !alive(gc) {
		t.Fatalf("expected the tree to outlive the host on this OS (server alive: %v, grandchild alive: %v)", alive(server), alive(gc))
	}

	// "Next run": find it and stop it.
	orphans, err := ListOrphans(dataDir)
	if err != nil {
		t.Fatal(err)
	}
	if len(orphans) != 1 || orphans[0].ID != "dev1" || orphans[0].Pid != server {
		t.Fatalf("ListOrphans = %v, want dev1 pid %d", orphans, server)
	}
	if err := StopOrphan(dataDir, "dev1"); err != nil {
		t.Fatal(err)
	}
	if !waitDead(server) || !waitDead(gc) {
		t.Fatal("orphaned dev-server tree survived StopOrphan")
	}
}
