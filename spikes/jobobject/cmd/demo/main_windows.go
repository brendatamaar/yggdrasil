//go:build windows

// Command demo is the manual Task Manager check for spike B2 (PL-02, PL-05).
// It creates a kill-on-close job, starts a process tree inside it (Go child
// and grandchild, plus an `npm run dev`-style Node chain when node is on PATH),
// starts an editor outside the job, prints every PID and waits to be killed.
package main

import (
	"context"
	"errors"
	"flag"
	"fmt"
	"os"
	"os/exec"
	"os/signal"
	"path/filepath"
	"sort"
	"strconv"
	"time"
	"unsafe"

	"golang.org/x/sys/windows"

	"github.com/brendatamaar/yggdrasil/spikes/jobobject"
)

// helperLife is how long the Go helpers live if nobody kills them.
const helperLife = "1h"

const devJS = `const { spawn } = require('child_process');
spawn(process.execPath, ['-e', 'setInterval(() => {}, 1000)'], { stdio: 'ignore' });
setInterval(() => {}, 1000);
`

const packageJSON = `{"name":"jobspike-demo","private":true,"scripts":{"dev":"node dev.js"}}
`

func main() {
	if jobobject.IsHelper() {
		if err := jobobject.RunHelper(os.Args[1], os.Args[2:]); err != nil {
			fmt.Fprintln(os.Stderr, err)
			os.Exit(2)
		}
		os.Exit(0)
	}
	approach := flag.String("approach", "self", "how children join the job: self (assign the demo itself) or perchild (suspended start + assign)")
	editor := flag.String("editor", "notepad.exe", "program started outside the job")
	flag.Parse()
	if err := run(*approach, *editor); err != nil {
		fmt.Fprintln(os.Stderr, "demo:", err)
		os.Exit(1)
	}
}

func run(approach, editor string) error {
	inJob, err := jobobject.InJob(os.Getpid())
	if err != nil {
		return err
	}
	fmt.Printf("demo pid %d, already inside some job at startup: %v\n", os.Getpid(), inJob)

	job, err := jobobject.New(jobobject.Options{KillOnClose: true, BreakawayOK: true})
	if err != nil {
		return err
	}
	defer job.Close()
	start := job.Start
	switch approach {
	case "self":
		if err := job.AssignSelf(); err != nil {
			return err
		}
		start = func(c *exec.Cmd) error { return c.Start() }
	case "perchild":
	default:
		return fmt.Errorf("unknown -approach %q", approach)
	}

	dir, err := os.MkdirTemp("", "jobspike-demo-")
	if err != nil {
		return err
	}
	gcFile := filepath.Join(dir, "grandchild.pid")
	child := jobobject.HelperCmd("child", gcFile, helperLife)
	if err := start(child); err != nil {
		return err
	}
	gc, err := jobobject.WaitKV(gcFile, 10*time.Second)
	if err != nil {
		return err
	}
	fmt.Printf("go child       pid %d\n", child.Process.Pid)
	fmt.Printf("go grandchild  pid %s\n", gc["pid"])

	if node := nodeChain(dir); node != nil {
		if err := start(node); err != nil {
			return err
		}
		fmt.Printf("node chain     pid %d (%s), waiting 3s for it to spawn\n", node.Process.Pid, filepath.Base(node.Path))
		time.Sleep(3 * time.Second)
	} else {
		fmt.Println("node chain     skipped: node not on PATH")
	}

	broker, err := jobobject.ShellPID()
	if err != nil {
		fmt.Println("no shell window, broker fallback disabled:", err)
	}
	outside, err := jobobject.StartOutside(func() *exec.Cmd { return exec.Command(editor) }, broker)
	if err != nil {
		return fmt.Errorf("start %s outside the job: %w", editor, err)
	}
	editorPID := outside.Cmd.Process.Pid
	in, _ := job.Contains(editorPID)
	fmt.Printf("%-14s pid %d, started via %s, in our job: %v, in some other job: %v",
		filepath.Base(editor), editorPID, outside.Method, in, outside.StillInJob)
	if outside.BreakawayErr != nil {
		fmt.Printf(" (breakaway not used: %v)", outside.BreakawayErr)
	}
	fmt.Println()
	exited := make(chan struct{})
	go func() {
		_ = outside.Cmd.Wait()
		close(exited)
	}()
	select {
	case <-time.After(2 * time.Second):
	case <-exited:
		fmt.Printf("note: pid %d already exited; %s handed off to another process (e.g. the Windows 11 Notepad app). That window is not our child.\n", editorPID, editor)
	}

	if err := printJob(job); err != nil {
		return err
	}
	fmt.Printf("\nKill the demo (pid %d) in Task Manager: every pid listed in the job must disappear, %s must stay open.\n", os.Getpid(), filepath.Base(editor))
	fmt.Println("Ctrl+C also closes the job, but the console sends it to every child too, so it proves less.")
	ctx, stop := signal.NotifyContext(context.Background(), os.Interrupt)
	defer stop()
	<-ctx.Done()
	return nil
}

// nodeChain returns an `npm run dev` (or plain `node dev.js`) command whose
// script spawns one more node process: npm.cmd -> node npm-cli -> cmd -> node
// -> node.
func nodeChain(dir string) *exec.Cmd {
	node, err := exec.LookPath("node")
	if err != nil {
		return nil
	}
	if err := os.WriteFile(filepath.Join(dir, "dev.js"), []byte(devJS), 0o600); err != nil {
		return nil
	}
	if err := os.WriteFile(filepath.Join(dir, "package.json"), []byte(packageJSON), 0o600); err != nil {
		return nil
	}
	cmd := exec.Command(node, "dev.js")
	if npm, err := exec.LookPath("npm"); err == nil {
		cmd = exec.Command(npm, "run", "dev")
	}
	cmd.Dir = dir
	return cmd
}

func printJob(job *jobobject.Job) error {
	pids, err := job.PIDs()
	if err != nil {
		return err
	}
	names, err := processNames()
	if err != nil {
		return err
	}
	sort.Ints(pids)
	fmt.Printf("\n%d processes in the job:\n", len(pids))
	for _, pid := range pids {
		note := ""
		if pid == os.Getpid() {
			note = "  (the demo itself)"
		}
		fmt.Printf("  %-8s %s%s\n", strconv.Itoa(pid), names[pid], note)
	}
	return nil
}

func processNames() (map[int]string, error) {
	snap, err := windows.CreateToolhelp32Snapshot(windows.TH32CS_SNAPPROCESS, 0)
	if err != nil {
		return nil, fmt.Errorf("process snapshot: %w", err)
	}
	defer windows.CloseHandle(snap)
	names := map[int]string{}
	entry := windows.ProcessEntry32{Size: uint32(unsafe.Sizeof(windows.ProcessEntry32{}))}
	for err = windows.Process32First(snap, &entry); err == nil; err = windows.Process32Next(snap, &entry) {
		names[int(entry.ProcessID)] = windows.UTF16ToString(entry.ExeFile[:])
	}
	if !errors.Is(err, windows.ERROR_NO_MORE_FILES) {
		return nil, fmt.Errorf("walk processes: %w", err)
	}
	return names, nil
}
