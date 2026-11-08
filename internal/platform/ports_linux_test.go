//go:build linux

package platform

import (
	"context"
	"net"
	"os"
	"path/filepath"
	"slices"
	"strconv"
	"strings"
	"testing"
)

const procNetTCPSample = `  sl  local_address rem_address   st tx_queue rx_queue tr tm->when retrnsmt   uid  timeout inode
   0: 0100007F:1F90 00000000:0000 0A 00000000:00000000 00:00000000 00000000  1000        0 11111 1 0000000000000000 100 0 0 10 0
   1: 0100007F:0BB8 0100007F:D2A0 01 00000000:00000000 00:00000000 00000000  1000        0 22222 1 0000000000000000 100 0 0 10 0
   2: 00000000:0050 00000000:0000 0A 00000000:00000000 00:00000000 00000000     0        0 33333 1 0000000000000000 100 0 0 10 0
   3: garbage
   4: 0100007F:ZZZZ 00000000:0000 0A 00000000:00000000 00:00000000 00000000  1000        0 44444 1 0000000000000000 100 0 0 10 0
`

func TestListeningSocketsParsesTable(t *testing.T) {
	got := listeningSockets(strings.NewReader(procNetTCPSample))
	want := map[uint64]int{11111: 8080, 33333: 80}
	if len(got) != len(want) {
		t.Fatalf("got %v, want %v", got, want)
	}
	for ino, port := range want {
		if got[ino] != port {
			t.Errorf("inode %d: port %d, want %d", ino, got[ino], port)
		}
	}
}

func TestParseProcStatWithAwkwardName(t *testing.T) {
	// The command name holds spaces and parentheses; fields count from the last ')'.
	line := "1234 (a) b (c) S 77 1234 1234 0 -1 4194560 100 0 0 0 1 1 0 0 20 0 1 0 987654 1000 100 18446744073709551615\n"
	st, err := parseProcStat(line)
	if err != nil {
		t.Fatal(err)
	}
	if st.state != 'S' || st.ppid != 77 || st.start != "987654" {
		t.Errorf("got %+v", st)
	}
	for _, bad := range []string{"", "1 (x)", "1 (x) S notanumber"} {
		if _, err := parseProcStat(bad); err == nil {
			t.Errorf("parseProcStat(%q) accepted", bad)
		}
	}
}

func listen(t *testing.T, network, addr string) int {
	t.Helper()
	ln, err := net.Listen(network, addr)
	if err != nil {
		return 0
	}
	t.Cleanup(func() { _ = ln.Close() })
	return ln.Addr().(*net.TCPAddr).Port
}

// Several real listeners, IPv4 and IPv6, in this process: all are reported,
// sorted, without duplicates.
func TestListeningPortsOwnListeners(t *testing.T) {
	var want []int
	for _, addr := range []string{"127.0.0.1:0", "127.0.0.1:0", "[::1]:0", "[::]:0"} {
		if p := listen(t, "tcp", addr); p != 0 {
			want = append(want, p)
		}
	}
	if len(want) < 2 {
		t.Skip("no loopback listeners possible")
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
	if !slices.IsSorted(got) || len(slices.Compact(slices.Clone(got))) != len(got) {
		t.Errorf("ports not sorted and unique: %v", got)
	}
}

// A connected (not listening) socket is not a port.
func TestListeningPortsIgnoresConnections(t *testing.T) {
	ln, err := net.Listen("tcp", "127.0.0.1:0")
	if err != nil {
		t.Skip("no loopback listener possible")
	}
	t.Cleanup(func() { _ = ln.Close() })
	c, err := net.Dial("tcp", ln.Addr().String())
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = c.Close() })
	clientPort := c.LocalAddr().(*net.TCPAddr).Port
	got, err := ListeningPorts(os.Getpid())
	if err != nil {
		t.Fatal(err)
	}
	if slices.Contains(got, clientPort) {
		t.Errorf("client port %d reported as listening: %v", clientPort, got)
	}
}

// The listeners belong to a grandchild: the tree walk must find them, and the
// ancestor (this process) sees them too.
func TestListeningPortsGrandchild(t *testing.T) {
	file := filepath.Join(t.TempDir(), "ports")
	p, err := Start(context.Background(), helperSpec(t, "spawn-listen", file))
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = p.Kill() })
	var want []int
	for s := range strings.SplitSeq(waitFile(t, file), ",") {
		n, err := strconv.Atoi(s)
		if err != nil {
			t.Fatalf("bad port %q in %q", s, file)
		}
		want = append(want, n)
	}
	for _, pid := range []int{p.Pid(), os.Getpid()} {
		got, err := ListeningPorts(pid)
		if err != nil {
			t.Fatal(err)
		}
		for _, port := range want {
			if !slices.Contains(got, port) {
				t.Errorf("ListeningPorts(%d) = %v, missing %d", pid, got, port)
			}
		}
		if !slices.IsSorted(got) {
			t.Errorf("not sorted: %v", got)
		}
	}
	if err := p.Kill(); err != nil {
		t.Fatal(err)
	}
	_, _ = p.Wait()
	if _, err := ListeningPorts(p.Pid()); err == nil {
		t.Error("ListeningPorts still finds the killed process")
	}
}

func TestListeningPortsNotRunning(t *testing.T) {
	if _, err := ListeningPorts(2147483646); err == nil {
		t.Fatal("expected an error for a pid that is not running")
	}
}
