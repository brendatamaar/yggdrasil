package agentstream

import (
	"bufio"
	"encoding/json"
	"fmt"
	"os"
	"os/exec"
	"strings"
	"testing"
	"time"
)

// The test binary doubles as a fake agent CLI. Runner tests start it through
// Start with AGENTSTREAM_FAKE=<mode> in the extras; it never runs a real CLI.
const (
	fakeEnv        = "AGENTSTREAM_FAKE"
	fakeFixtureEnv = "AGENTSTREAM_FAKE_FIXTURE"
	fakePIDsEnv    = "AGENTSTREAM_FAKE_PIDS"
	fakeDelayEnv   = "AGENTSTREAM_FAKE_DELAY"
	fakeHoldEnv    = "AGENTSTREAM_FAKE_HOLD"
	// fakeLifetime is a backstop: a fake that leaks anyway exits on its own.
	fakeLifetime = 2 * time.Minute
)

func TestMain(m *testing.M) {
	if mode := os.Getenv(fakeEnv); mode != "" {
		if err := runFake(mode); err != nil {
			fmt.Fprintln(os.Stderr, "fake:", err)
			os.Exit(3)
		}
		os.Exit(0)
	}
	os.Exit(m.Run())
}

// runFake modes:
//
//	sleep   sleep fakeLifetime (the fake's child)
//	env     print one {"type":"fake_env","env":{...}} line and exit
//	stream  start a sleeping child that shares stdout, write both pids to
//	        $AGENTSTREAM_FAKE_PIDS, print $AGENTSTREAM_FAKE_FIXTURE line by
//	        line every $AGENTSTREAM_FAKE_DELAY, then sleep $AGENTSTREAM_FAKE_HOLD
func runFake(mode string) error {
	switch mode {
	case "sleep":
		time.Sleep(fakeLifetime)
		return nil
	case "env":
		env := map[string]string{}
		for _, kv := range os.Environ() {
			if k, v, ok := strings.Cut(kv, "="); ok && k != "" {
				env[k] = v
			}
		}
		line, err := json.Marshal(map[string]any{"type": "fake_env", "env": env})
		if err != nil {
			return err
		}
		_, err = fmt.Printf("%s\n", line)
		return err
	case "stream":
		return fakeStream()
	}
	return fmt.Errorf("unknown mode %q", mode)
}

func fakeStream() error {
	delay, err := time.ParseDuration(os.Getenv(fakeDelayEnv))
	if err != nil {
		return fmt.Errorf("delay: %w", err)
	}
	hold, err := time.ParseDuration(os.Getenv(fakeHoldEnv))
	if err != nil {
		return fmt.Errorf("hold: %w", err)
	}
	exe, err := os.Executable()
	if err != nil {
		return err
	}
	child := exec.Command(exe)
	for _, kv := range os.Environ() {
		if !strings.HasPrefix(kv, fakeEnv+"=") {
			child.Env = append(child.Env, kv)
		}
	}
	child.Env = append(child.Env, fakeEnv+"=sleep")
	// The child holds our stdout too, like a background tool process would.
	child.Stdout = os.Stdout
	if err := child.Start(); err != nil {
		return fmt.Errorf("start child: %w", err)
	}
	pids := fmt.Sprintf("fake=%d\nchild=%d\n", os.Getpid(), child.Process.Pid)
	path := os.Getenv(fakePIDsEnv)
	if err := os.WriteFile(path+".tmp", []byte(pids), 0o600); err != nil {
		return err
	}
	if err := os.Rename(path+".tmp", path); err != nil {
		return err
	}
	f, err := os.Open(os.Getenv(fakeFixtureEnv))
	if err != nil {
		return err
	}
	defer f.Close()
	sc := bufio.NewScanner(f)
	for sc.Scan() {
		if _, err := fmt.Printf("%s\n", sc.Bytes()); err != nil {
			return err
		}
		time.Sleep(delay)
	}
	if err := sc.Err(); err != nil {
		return err
	}
	time.Sleep(hold)
	return nil
}
