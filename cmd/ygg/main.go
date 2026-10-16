// Command ygg runs Yggdrasil: a multi-repo workspace for code agents.
package main

import (
	"context"
	"errors"
	"fmt"
	"io"
	"io/fs"
	"net"
	"os"
	"os/signal"

	"github.com/brendatamaar/yggdrasil/internal/agents/claudecode"
	"github.com/brendatamaar/yggdrasil/internal/platform"
	"github.com/brendatamaar/yggdrasil/web"
)

// version is set at build time with -ldflags "-X main.version=<tag>".
var version = "dev"

// streams is what a command talks to, so tests can replace it.
type streams struct {
	in            io.Reader
	out, err      io.Writer
	getenv        func(string) string
	userConfigDir func() (string, error)
}

// exitError asks main to exit with a code without printing anything more.
type exitError struct{ code int }

func (e exitError) Error() string { return fmt.Sprintf("exit status %d", e.code) }

func main() {
	ctx, stop := signal.NotifyContext(context.Background(), os.Interrupt)
	err := run(ctx, os.Args[1:], streams{
		in: os.Stdin, out: os.Stdout, err: os.Stderr, getenv: os.Getenv, userConfigDir: os.UserConfigDir,
	})
	stop()
	// Closes the job every child process belongs to: nothing outlives ygg.
	if shutdownErr := platform.Shutdown(); shutdownErr != nil {
		fmt.Fprintln(os.Stderr, "ygg: stopping child processes:", shutdownErr)
	}
	var ee exitError
	if errors.As(err, &ee) {
		os.Exit(ee.code)
	}
	if err != nil {
		fmt.Fprintln(os.Stderr, "ygg:", err)
		os.Exit(1)
	}
}

func run(ctx context.Context, args []string, s streams) error {
	cmd := "serve"
	if len(args) > 0 {
		cmd = args[0]
	}
	switch cmd {
	case "serve":
		return serve(ctx, s)
	case "doctor":
		cfg, err := loadConfig(s.getenv, s.userConfigDir)
		if err != nil {
			return err
		}
		if code := doctor(ctx, s.out, cfg); code != 0 {
			return exitError{code}
		}
		return nil
	case "seed":
		if len(args) < 2 || len(args) > 3 {
			return errors.New("usage: ygg seed <repo folder> [project name]")
		}
		cfg, err := loadConfig(s.getenv, s.userConfigDir)
		if err != nil {
			return err
		}
		name := ""
		if len(args) == 3 {
			name = args[2]
		}
		return seed(ctx, s.out, cfg, args[1], name)
	case "hook":
		// Not listed in the help: Claude Code runs this on every tool call to
		// enforce the task's rules (see claudecode.RunHook). It needs no
		// configuration of its own and opens no database.
		if code := claudecode.RunHook(args[1:], s.in, s.out, s.err, s.getenv); code != 0 {
			return exitError{code}
		}
		return nil
	case "version", "--version":
		fmt.Fprintln(s.out, "ygg", version)
		return nil
	case "help", "-h", "--help":
		usage(s.out)
		return nil
	default:
		usage(s.err)
		return fmt.Errorf("unknown command %q", cmd)
	}
}

func usage(w io.Writer) {
	fmt.Fprint(w, `Usage: ygg [command]

Commands:
  serve            start Yggdrasil (default)
  doctor           check what Yggdrasil needs and explain what is missing
  seed <folder>    track a git repo so you can start a task on it
                   (a stand-in until the project pages arrive)
  version          print the version
  help             show this help

Settings (all optional, see .env.example):
  YGG_ADDR            listen address (default `+defaultAddr+`, loopback only)
  YGG_DATA_DIR        where the database and logs live
  YGG_LOG             debug, info, warn or error
  YGG_MOCK_DELAY_MS   pause between events of the demo agent
`)
}

// serve starts Yggdrasil on the configured address until ctx is cancelled.
func serve(ctx context.Context, s streams) error {
	cfg, err := loadConfig(s.getenv, s.userConfigDir)
	if err != nil {
		return err
	}
	dist, err := fs.Sub(web.Dist, "dist")
	if err != nil {
		return fmt.Errorf("open embedded frontend: %w", err)
	}
	// Listen first, so a busy port is reported before anything else starts.
	ln, err := net.Listen("tcp", cfg.addr)
	if err != nil {
		return fmt.Errorf("can't listen on %s (is another ygg running? set YGG_ADDR to use another port): %w", cfg.addr, err)
	}
	a, err := newApp(ctx, cfg, dist, s.err)
	if err != nil {
		_ = ln.Close()
		return err
	}
	return a.run(ctx, ln)
}
