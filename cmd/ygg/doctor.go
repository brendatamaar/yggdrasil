package main

import (
	"bytes"
	"context"
	"errors"
	"fmt"
	"io"
	"os"
	"strings"
	"time"

	"github.com/brendatamaar/yggdrasil/internal/agents/claudecode"
	"github.com/brendatamaar/yggdrasil/internal/platform"
	"github.com/brendatamaar/yggdrasil/internal/safety"
)

// advice is a plain-language message that says what to do next. It is an
// error so a probe can return it, but it is written for people, not logs.
type advice string

func (a advice) Error() string { return string(a) }

// probe is one check of `ygg doctor` (IN-04).
type probe struct {
	name string
	// required probes decide the exit code; the others only inform.
	required bool
	// check returns a short detail when the check passes. A failure is an
	// error whose text is plain language and says what to do next.
	check func(ctx context.Context) (string, error)
}

// runDoctor runs the probes, prints one line each and a summary, and returns
// the exit code: 1 when a required probe failed.
func runDoctor(ctx context.Context, w io.Writer, probes []probe) int {
	fmt.Fprintln(w, "Yggdrasil doctor")
	fmt.Fprintln(w)
	var failed []string
	missing := 0
	for _, p := range probes {
		detail, err := p.check(ctx)
		switch {
		case err == nil:
			fmt.Fprintf(w, "  ok    %-14s %s\n", p.name, detail)
		case p.required:
			failed = append(failed, p.name)
			fmt.Fprintf(w, "  FAIL  %-14s %s\n", p.name, err)
		default:
			missing++
			fmt.Fprintf(w, "  --    %-14s %s\n", p.name, err)
		}
	}
	fmt.Fprintln(w)
	switch {
	case len(failed) > 0:
		fmt.Fprintf(w, "%d required check(s) failed: %s. Fix them, then run `ygg doctor` again.\n", len(failed), strings.Join(failed, ", "))
		return 1
	case missing > 0:
		fmt.Fprintf(w, "Everything required is in place. %d optional tool(s) were not found; install one only if you need it.\n", missing)
	default:
		fmt.Fprintln(w, "Everything is in place.")
	}
	return 0
}

// tool describes a program the doctor looks for.
type tool struct {
	name     string
	bin      string
	args     []string
	required bool
	// why says what the tool is for and where to get it, for the "not found" line.
	why string
}

var tools = []tool{
	{name: "git", bin: "git", args: []string{"--version"}, required: true,
		why: "Yggdrasil needs Git. Install Git for Windows from https://git-scm.com, then open a new terminal."},
	{name: "Node.js", bin: "node", args: []string{"--version"},
		why: "Only needed to build the frontend or to work on Node repos. Get it from https://nodejs.org."},
	{name: "pnpm", bin: "pnpm", args: []string{"--version"},
		why: "Only needed to build the frontend. Run `corepack enable`, or see https://pnpm.io."},
	{name: "Go", bin: "go", args: []string{"version"},
		why: "Only needed to build Yggdrasil or to work on Go repos. Get it from https://go.dev/dl."},
	{name: "Java", bin: "java", args: []string{"--version"},
		why: "Only needed for Maven and Gradle repos. Install a JDK, such as Temurin from https://adoptium.net."},
	{name: "Maven", bin: "mvn", args: []string{"--version"},
		why: "Only needed for Maven repos that have no mvnw wrapper. See https://maven.apache.org."},
	{name: ".NET", bin: "dotnet", args: []string{"--version"},
		why: "Only needed for .NET repos. Get the SDK from https://dotnet.microsoft.com."},
	{name: "Python", bin: "python", args: []string{"--version"},
		why: "Only needed for Python repos. Get it from https://www.python.org."},
	{name: "uv", bin: "uv", args: []string{"--version"},
		why: "Only needed for Python repos that use uv. See https://docs.astral.sh/uv."},
}

// versionOf runs `bin args` with the clean environment and returns the first
// line it prints. Every process starts through platform.Start (invariant 3).
func versionOf(ctx context.Context, env []string, bin string, args []string) (string, error) {
	path, err := platform.Resolve(bin, "", env)
	if err != nil {
		return "", err
	}
	ctx, cancel := context.WithTimeout(ctx, 15*time.Second)
	defer cancel()
	var out, errOut bytes.Buffer
	p, err := platform.Start(ctx, platform.Spec{Argv: append([]string{path}, args...), Env: env, Stdout: &out, Stderr: &errOut})
	if err != nil {
		return "", err
	}
	code, err := p.Wait()
	if err != nil {
		return "", err
	}
	if code != 0 {
		return "", fmt.Errorf("exited with code %d", code)
	}
	text := strings.TrimSpace(out.String())
	if text == "" {
		text = strings.TrimSpace(errOut.String()) // some tools print their version on stderr
	}
	line, _, _ := strings.Cut(text, "\n")
	return strings.TrimSpace(line), nil
}

func toolProbe(t tool, env []string) probe {
	return probe{name: t.name, required: t.required, check: func(ctx context.Context) (string, error) {
		line, err := versionOf(ctx, env, t.bin, t.args)
		switch {
		case err == nil:
			return line, nil
		case errors.Is(err, platform.ErrNotFound):
			return "", advice("not found. " + t.why)
		default:
			return "", advice(fmt.Sprintf("found, but it didn't run (%v). %s", err, t.why))
		}
	}}
}

// dataDirProbe checks that the data folder can be created and written to.
func dataDirProbe(dir string) probe {
	return probe{name: "data folder", required: true, check: func(context.Context) (string, error) {
		if err := os.MkdirAll(dir, 0o700); err != nil {
			return "", advice(fmt.Sprintf("%s can't be created (%v). Set YGG_DATA_DIR to a folder you can write to.", dir, err))
		}
		f, err := os.CreateTemp(dir, ".doctor-*")
		if err != nil {
			return "", advice(fmt.Sprintf("%s can't be written to (%v). Set YGG_DATA_DIR to a folder you can write to.", dir, err))
		}
		name := f.Name()
		_ = f.Close()
		_ = os.Remove(name) // the probe's own empty file
		return dir, nil
	}}
}

// agentProbe reports whether an agent CLI is usable. Agents are optional: the
// workspace works without one.
func agentProbe(name string, check func(context.Context) error) probe {
	return probe{name: name, check: func(ctx context.Context) (string, error) {
		if err := check(ctx); err != nil {
			return "", advice(userText(err))
		}
		return "installed and starts", nil
	}}
}

// userText is the part of an error that is meant for people.
func userText(err error) string {
	var um interface{ UserMessage() string }
	if errors.As(err, &um) {
		return um.UserMessage()
	}
	return "it can't be used right now. Run it in a terminal to see why."
}

// defaultProbes are the checks `ygg doctor` runs.
func defaultProbes(cfg config, env []string) []probe {
	probes := make([]probe, 0, len(tools)+3)
	for _, t := range tools[:1] { // git first: it decides the exit code
		probes = append(probes, toolProbe(t, env))
	}
	probes = append(probes, dataDirProbe(cfg.dataDir))
	probes = append(probes, agentProbe("Claude Code", claudecode.New(claudecode.Options{}).Check))
	for _, t := range tools[1:] {
		probes = append(probes, toolProbe(t, env))
	}
	return probes
}

// doctor is the `ygg doctor` command.
func doctor(ctx context.Context, w io.Writer, cfg config) int {
	env, err := safety.NewEnvBuilder().Build(safety.EnvOptions{})
	if err != nil {
		fmt.Fprintln(w, "Yggdrasil doctor can't prepare its environment:", err)
		return 1
	}
	return runDoctor(ctx, w, defaultProbes(cfg, env))
}
