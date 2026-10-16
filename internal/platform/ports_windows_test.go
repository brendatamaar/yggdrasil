//go:build windows

package platform

import (
	"context"
	"net"
	"os"
	"path/filepath"
	"slices"
	"testing"
	"time"
)

func TestListeningPortsOwnListeners(t *testing.T) {
	var want []int
	for _, addr := range []string{"127.0.0.1:0", "[::1]:0"} {
		ln, err := net.Listen("tcp", addr)
		if err != nil {
			t.Logf("cannot listen on %s: %v", addr, err)
			continue
		}
		t.Cleanup(func() { _ = ln.Close() })
		want = append(want, ln.Addr().(*net.TCPAddr).Port)
	}
	if len(want) == 0 {
		t.Skip("no loopback listener possible")
	}
	got, err := ListeningPorts(os.Getpid())
	if err != nil {
		t.Fatal(err)
	}
	for _, p := range want {
		if !slices.Contains(got, p) {
			t.Errorf("port %d missing from %v", p, got)
		}
	}
	if !slices.IsSorted(got) {
		t.Errorf("ports not sorted: %v", got)
	}
}

// The listener belongs to a grandchild, so the tree walk must find it.
func TestListeningPortsDescendants(t *testing.T) {
	portFile := filepath.Join(t.TempDir(), "port")
	p, err := Start(context.Background(), testEnvSpec(t, "spawn-listen", portFile))
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = p.Kill() })
	pids, err := waitPIDs(portFile)
	if err != nil {
		t.Fatal(err)
	}
	got, err := ListeningPorts(p.Pid())
	if err != nil {
		t.Fatal(err)
	}
	if !slices.Contains(got, pids["port"]) {
		t.Errorf("ListeningPorts(%d) = %v, want it to contain %d", p.Pid(), got, pids["port"])
	}
	// The test process is an ancestor, so it sees the grandchild's port too.
	if own, err := ListeningPorts(os.Getpid()); err != nil || !slices.Contains(own, pids["port"]) {
		t.Errorf("ancestor does not report the grandchild's port: %v, %v", own, err)
	}

	if err := p.Kill(); err != nil {
		t.Fatal(err)
	}
	_, _ = p.Wait()
	deadline := time.Now().Add(waitTimeout)
	for {
		_, err := ListeningPorts(p.Pid())
		if err != nil {
			break // gone
		}
		if time.Now().After(deadline) {
			t.Fatal("ListeningPorts still finds the killed process")
		}
		time.Sleep(50 * time.Millisecond)
	}
}

func TestTablePortsParsesRows(t *testing.T) {
	// Two IPv4 rows: pid 7 listens on 8080 (0x1F90, big-endian 1F 90), pid 9 on 3000.
	table := []byte{
		2, 0, 0, 0,
		2, 0, 0, 0, 0, 0, 0, 0, 0x1F, 0x90, 0, 0, 0, 0, 0, 0, 0, 0, 0, 0, 7, 0, 0, 0,
		2, 0, 0, 0, 0, 0, 0, 0, 0x0B, 0xB8, 0, 0, 0, 0, 0, 0, 0, 0, 0, 0, 9, 0, 0, 0,
	}
	got := tablePorts(table, row4Size, row4Port, row4PID, map[uint32]bool{7: true})
	if !slices.Equal(got, []int{8080}) {
		t.Errorf("got %v, want [8080]", got)
	}
	// A truncated table is cut, not a panic.
	if got := tablePorts(table[:30], row4Size, row4Port, row4PID, map[uint32]bool{7: true, 9: true}); len(got) != 1 {
		t.Errorf("truncated table gave %v", got)
	}
}
