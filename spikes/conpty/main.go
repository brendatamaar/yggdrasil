package main

import (
	"context"
	"errors"
	"flag"
	"log/slog"
	"net"
	"net/http"
	"os"
	"os/signal"
	"path/filepath"
	"strconv"
	"time"
)

func main() {
	port := flag.Int("port", 4848, "port to listen on (always 127.0.0.1)")
	xtermDir := flag.String("xterm", "", "path to web/node_modules/@xterm (default: found by walking up from the working directory)")
	flag.Parse()

	log := slog.New(slog.NewTextHandler(os.Stderr, nil))
	dir := *xtermDir
	if dir == "" {
		dir = findXterm()
	}
	if dir == "" {
		log.Warn("xterm.js not found; run: pnpm -C web install --frozen-lockfile")
	} else {
		log.Info("serving xterm.js", "dir", dir)
	}

	addr := net.JoinHostPort("127.0.0.1", strconv.Itoa(*port))
	srv := &http.Server{
		Addr:              addr,
		Handler:           newServer(startGoPTY, dir, log),
		ReadHeaderTimeout: 10 * time.Second,
	}

	ctx, stop := signal.NotifyContext(context.Background(), os.Interrupt)
	defer stop()
	go func() {
		<-ctx.Done()
		shutdownCtx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
		defer cancel()
		_ = srv.Shutdown(shutdownCtx)
	}()

	log.Info("open in a browser", "url", "http://"+addr+"/")
	if err := srv.ListenAndServe(); err != nil && !errors.Is(err, http.ErrServerClosed) {
		log.Error("listen", "err", err)
		os.Exit(1)
	}
}

// findXterm looks for web/node_modules/@xterm in the working directory and
// its parents, so the spike works from the repo root or from spikes/conpty.
func findXterm() string {
	dir, err := os.Getwd()
	if err != nil {
		return ""
	}
	for {
		candidate := filepath.Join(dir, "web", "node_modules", "@xterm")
		if _, err := os.Stat(filepath.Join(candidate, "xterm", "package.json")); err == nil {
			return candidate
		}
		parent := filepath.Dir(dir)
		if parent == dir {
			return ""
		}
		dir = parent
	}
}
