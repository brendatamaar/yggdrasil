//go:build windows

package jobobject

import (
	"fmt"
	"os"
	"os/exec"
	"sort"
	"strconv"
	"strings"
	"time"
)

// helperEnv marks a re-executed binary (test binary or demo) as a helper. The
// mode is os.Args[1].
const helperEnv = "JOBSPIKE_HELPER"

// IsHelper reports whether this process was started by HelperCmd.
func IsHelper() bool { return os.Getenv(helperEnv) == "1" && len(os.Args) > 1 }

// HelperArgv returns argv re-executing the current binary in helper mode.
func HelperArgv(mode string, args ...string) []string {
	exe, err := os.Executable()
	if err != nil {
		exe = os.Args[0]
	}
	return append([]string{exe, mode}, args...)
}

// HelperEnv returns our environment plus the helper marker.
func HelperEnv() []string { return append(os.Environ(), helperEnv+"=1") }

// HelperCmd returns an exec.Cmd re-executing the current binary in helper
// mode. Std handles stay nil (NUL), so a surviving helper never holds a
// parent's pipe open.
func HelperCmd(mode string, args ...string) *exec.Cmd {
	argv := HelperArgv(mode, args...)
	cmd := exec.Command(argv[0], argv[1:]...)
	cmd.Env = HelperEnv()
	return cmd
}

// RunHelper runs the generic helper modes:
//
//	sleep <lifetime>             sleep, then exit (backstop against leaks)
//	child <pidfile> <lifetime>   start a "sleep" grandchild, write its pid, sleep
func RunHelper(mode string, args []string) error {
	switch mode {
	case "sleep":
		d, err := lifetimeArg(args, 0)
		if err != nil {
			return err
		}
		time.Sleep(d)
		return nil
	case "child":
		if len(args) < 2 {
			return fmt.Errorf("child: want <pidfile> <lifetime>")
		}
		d, err := lifetimeArg(args, 1)
		if err != nil {
			return err
		}
		gc := HelperCmd("sleep", d.String())
		if err := gc.Start(); err != nil {
			return fmt.Errorf("start grandchild: %w", err)
		}
		if err := WriteKV(args[0], map[string]string{"pid": strconv.Itoa(gc.Process.Pid)}); err != nil {
			return err
		}
		time.Sleep(d)
		return nil
	default:
		return fmt.Errorf("unknown helper mode %q", mode)
	}
}

func lifetimeArg(args []string, i int) (time.Duration, error) {
	if len(args) <= i {
		return 0, fmt.Errorf("missing lifetime argument")
	}
	return time.ParseDuration(args[i])
}

// WriteKV writes key=value lines atomically (temp file + rename), so a reader
// polling for the file never sees it half written.
func WriteKV(path string, kv map[string]string) error {
	keys := make([]string, 0, len(kv))
	for k := range kv {
		keys = append(keys, k)
	}
	sort.Strings(keys)
	var b strings.Builder
	for _, k := range keys {
		fmt.Fprintf(&b, "%s=%s\n", k, kv[k])
	}
	tmp := path + ".tmp"
	if err := os.WriteFile(tmp, []byte(b.String()), 0o600); err != nil {
		return fmt.Errorf("write %s: %w", tmp, err)
	}
	if err := os.Rename(tmp, path); err != nil {
		return fmt.Errorf("rename %s: %w", tmp, err)
	}
	return nil
}

// WaitKV polls for a file written by WriteKV and parses it.
func WaitKV(path string, timeout time.Duration) (map[string]string, error) {
	deadline := time.Now().Add(timeout)
	for {
		data, err := os.ReadFile(path)
		if err == nil {
			kv := map[string]string{}
			for _, line := range strings.Split(strings.TrimSpace(string(data)), "\n") {
				if k, v, ok := strings.Cut(line, "="); ok {
					kv[k] = v
				}
			}
			return kv, nil
		}
		if time.Now().After(deadline) {
			return nil, fmt.Errorf("wait for %s: %w", path, err)
		}
		time.Sleep(50 * time.Millisecond)
	}
}
