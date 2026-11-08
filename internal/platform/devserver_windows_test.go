//go:build windows

package platform

import (
	"context"
	"net"
	"os/exec"
	"path/filepath"
	"strconv"
	"testing"
	"time"

	"golang.org/x/sys/windows"
)

// runYggDev acts as Yggdrasil running a dev server (DV-03): it starts a
// server whose listener lives in a grandchild (npm.cmd -> node style), writes
// the pids and the port to out, and sleeps until the test kills it.
func runYggDev(exe, out string) error {
	portFile := out + ".port"
	p, err := Start(context.Background(), Spec{
		Argv: []string{exe, "spawn-listen", portFile},
		Env:  helperEnv(), TaskID: "dev1",
	})
	if err != nil {
		return err
	}
	ports, err := waitPIDs(portFile)
	if err != nil {
		return err
	}
	if err := writePIDs(out, map[string]int{"server": p.Pid(), "port": ports["port"]}); err != nil {
		return err
	}
	time.Sleep(helperLife)
	return nil
}

// A started dev-server tree dies when the host process is killed (as from Task
// Manager): the server process is gone and its listener's port is closed, so
// no dev server outlives Yggdrasil (DV-03, PL-02). Windows-only: the job
// object does this.
func TestDevServerTreeDiesWhenHostIsKilled(t *testing.T) {
	out := filepath.Join(t.TempDir(), "devhost")
	cmd := exec.Command(selfExe(t), "ygg-dev", out)
	cmd.Env = helperEnv()
	if err := cmd.Start(); err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() {
		_ = cmd.Process.Kill()
		_ = cmd.Wait()
	})
	pids, err := waitPIDs(out)
	if err != nil {
		t.Fatal(err)
	}
	server := openProc(t, "server", pids["server"])
	addr := net.JoinHostPort("127.0.0.1", strconv.Itoa(pids["port"]))
	conn, err := net.DialTimeout("tcp", addr, time.Second)
	if err != nil {
		t.Fatalf("dev server port %s is not open before the kill: %v", addr, err)
	}
	_ = conn.Close()

	host := openProc(t, "host", cmd.Process.Pid)
	if err := windows.TerminateProcess(host.h, 1); err != nil {
		t.Fatalf("kill host: %v", err)
	}
	if !server.exited(waitTimeout) {
		t.Fatalf("dev server (pid %d) still alive %v after the host was killed", server.pid, waitTimeout)
	}
	deadline := time.Now().Add(waitTimeout)
	for {
		c, err := net.DialTimeout("tcp", addr, 500*time.Millisecond)
		if err != nil {
			return // listener gone
		}
		_ = c.Close()
		if time.Now().After(deadline) {
			t.Fatalf("port %s still listens after the host was killed", addr)
		}
		time.Sleep(100 * time.Millisecond)
	}
}
