//go:build !windows

package platform

import (
	"context"
	"fmt"
	"net"
	"os"
	"os/exec"
	"strconv"
	"strings"
	"testing"
	"time"
)

// The test binary doubles as a helper process (as on Windows): started with
// YGG_PLATFORM_HELPER=1 and a mode as its first argument it runs that mode
// instead of the tests.
//
//	sleep
//	listen <file>               listen on several ports (IPv4, IPv6), write them, sleep
//	spawn-listen <file>         start a "listen" grandchild, sleep
//	child <file>                start a "sleep" grandchild, write its pid, sleep
//	ygg-dev <out> <datadir>     act as Yggdrasil: Start a "child" tree as dev
//	                            server "dev1", RecordProcess it, write pids, sleep

const (
	helperEnvVar = "YGG_PLATFORM_HELPER"
	helperLife   = 2 * time.Minute
	waitTimeout  = 10 * time.Second
)

func TestMain(m *testing.M) {
	if len(os.Args) > 1 && !strings.HasPrefix(os.Args[1], "-test.") {
		if os.Getenv(helperEnvVar) == "1" {
			if err := runHelper(os.Args[1], os.Args[2:]); err != nil {
				fmt.Fprintln(os.Stderr, err)
				os.Exit(2)
			}
			os.Exit(0)
		}
		fmt.Fprintf(os.Stderr, "platform test binary: unexpected argument %q\n", os.Args[1])
		os.Exit(2)
	}
	os.Exit(m.Run())
}

func helperEnv() []string {
	return []string{helperEnvVar + "=1", "PATH=/usr/bin:/bin"}
}

func runHelper(mode string, args []string) error {
	exe, err := os.Executable()
	if err != nil {
		return err
	}
	switch mode {
	case "sleep":
		time.Sleep(helperLife)
	case "listen":
		var ports []string
		for _, addr := range []string{"127.0.0.1:0", "127.0.0.1:0", "[::1]:0"} {
			ln, err := net.Listen("tcp", addr)
			if err != nil {
				continue // IPv6 may be unavailable
			}
			defer ln.Close()
			ports = append(ports, strconv.Itoa(ln.Addr().(*net.TCPAddr).Port))
		}
		if err := writeFileAtomic(args[0], strings.Join(ports, ",")); err != nil {
			return err
		}
		time.Sleep(helperLife)
	case "spawn-listen":
		gc := exec.Command(exe, "listen", args[0])
		gc.Env = helperEnv()
		if err := gc.Start(); err != nil {
			return err
		}
		time.Sleep(helperLife)
	case "child":
		gc := exec.Command(exe, "sleep")
		gc.Env = helperEnv()
		if err := gc.Start(); err != nil {
			return err
		}
		if err := writeFileAtomic(args[0], strconv.Itoa(gc.Process.Pid)); err != nil {
			return err
		}
		time.Sleep(helperLife)
	case "ygg-dev":
		p, err := Start(context.Background(), Spec{Argv: []string{exe, "child", args[0] + ".gc"}, Env: helperEnv(), TaskID: "t1"})
		if err != nil {
			return err
		}
		if err := RecordProcess(args[1], "dev1", p.Pid()); err != nil {
			return err
		}
		if err := writeFileAtomic(args[0], strconv.Itoa(p.Pid())); err != nil {
			return err
		}
		time.Sleep(helperLife)
	default:
		return fmt.Errorf("unknown helper mode %q", mode)
	}
	return nil
}

func writeFileAtomic(path, content string) error {
	tmp := path + ".tmp"
	if err := os.WriteFile(tmp, []byte(content), 0o600); err != nil {
		return err
	}
	return os.Rename(tmp, path)
}

func waitFile(t *testing.T, path string) string {
	t.Helper()
	deadline := time.Now().Add(waitTimeout)
	for {
		if data, err := os.ReadFile(path); err == nil {
			return string(data)
		}
		if time.Now().After(deadline) {
			t.Fatalf("timed out waiting for %s", path)
		}
		time.Sleep(20 * time.Millisecond)
	}
}

func helperSpec(t *testing.T, argv ...string) Spec {
	t.Helper()
	exe, err := os.Executable()
	if err != nil {
		t.Fatal(err)
	}
	return Spec{Argv: append([]string{exe}, argv...), Env: helperEnv()}
}

// alive reports whether pid is a running (non-zombie) process.
func alive(pid int) bool {
	_, err := processStart(pid)
	return err == nil
}

func waitDead(pid int) bool {
	deadline := time.Now().Add(waitTimeout)
	for alive(pid) {
		if time.Now().After(deadline) {
			return false
		}
		time.Sleep(20 * time.Millisecond)
	}
	return true
}
