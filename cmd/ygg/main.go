// Command ygg runs Yggdrasil: a multi-repo workspace for code agents.
package main

import (
	"context"
	"errors"
	"fmt"
	"io"
	"io/fs"
	"log/slog"
	"net"
	"net/http"
	"os"
	"os/signal"
	"time"

	"github.com/brendatamaar/yggdrasil/web"
)

// version is set at build time with -ldflags "-X main.version=<tag>".
var version = "dev"

const defaultAddr = "127.0.0.1:4747"

func main() {
	if err := run(os.Args[1:]); err != nil {
		fmt.Fprintln(os.Stderr, "ygg:", err)
		os.Exit(1)
	}
}

func run(args []string) error {
	cmd := "serve"
	if len(args) > 0 {
		cmd = args[0]
	}
	switch cmd {
	case "serve":
		return serve()
	case "version", "--version":
		fmt.Println("ygg", version)
		return nil
	case "help", "-h", "--help":
		usage(os.Stdout)
		return nil
	default:
		usage(os.Stderr)
		return fmt.Errorf("unknown command %q", cmd)
	}
}

func usage(w io.Writer) {
	fmt.Fprint(w, `Usage: ygg [command]

Commands:
  serve     start Yggdrasil (default)
  version   print the version
  help      show this help

Settings:
  YGG_ADDR  listen address (default `+defaultAddr+`, loopback only)
`)
}

func serve() error {
	addr, err := listenAddr(os.Getenv("YGG_ADDR"))
	if err != nil {
		return err
	}

	dist, err := fs.Sub(web.Dist, "dist")
	if err != nil {
		return fmt.Errorf("open embedded frontend: %w", err)
	}

	ln, err := net.Listen("tcp", addr)
	if err != nil {
		return fmt.Errorf("can't listen on %s (is another ygg running? set YGG_ADDR to use another port): %w", addr, err)
	}

	srv := &http.Server{
		Handler:           uiHandler(dist),
		ReadHeaderTimeout: 10 * time.Second,
	}

	ctx, stop := signal.NotifyContext(context.Background(), os.Interrupt)
	defer stop()

	errc := make(chan error, 1)
	go func() { errc <- srv.Serve(ln) }()
	slog.Info("Yggdrasil is running", "url", "http://"+ln.Addr().String(), "version", version)

	select {
	case err := <-errc:
		return fmt.Errorf("serve http: %w", err)
	case <-ctx.Done():
	}

	shutdownCtx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
	defer cancel()
	if err := srv.Shutdown(shutdownCtx); err != nil {
		return fmt.Errorf("shut down http server: %w", err)
	}
	if err := <-errc; err != nil && !errors.Is(err, http.ErrServerClosed) {
		return fmt.Errorf("serve http: %w", err)
	}
	return nil
}

// listenAddr returns the address to listen on. Only loopback addresses are
// accepted until remote access mode (RA-*) exists.
func listenAddr(env string) (string, error) {
	if env == "" {
		return defaultAddr, nil
	}
	host, _, err := net.SplitHostPort(env)
	if err != nil {
		return "", fmt.Errorf("YGG_ADDR %q is not host:port, e.g. %s: %w", env, defaultAddr, err)
	}
	if ip := net.ParseIP(host); ip == nil || !ip.IsLoopback() {
		return "", fmt.Errorf("YGG_ADDR %q must use a loopback IP such as 127.0.0.1; remote access isn't available yet", env)
	}
	return env, nil
}
