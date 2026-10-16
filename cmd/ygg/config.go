package main

import (
	"fmt"
	"log/slog"
	"net"
	"path/filepath"
	"strconv"
	"strings"
	"time"
)

const defaultAddr = "127.0.0.1:4747"

// config holds the settings read from the environment (.env.example).
type config struct {
	// addr is the listen address: always a loopback IP until remote access
	// mode (RA-*) exists.
	addr string
	// dataDir holds the database, run folders and logs.
	dataDir string
	level   slog.Level
	// mockDelay paces the demo agent so a transcript visibly streams.
	mockDelay time.Duration
}

// loadConfig reads settings through getenv, so tests don't touch the real
// environment. userConfigDir gives the default parent of the data folder.
func loadConfig(getenv func(string) string, userConfigDir func() (string, error)) (config, error) {
	addr, err := listenAddr(getenv("YGG_ADDR"))
	if err != nil {
		return config{}, err
	}
	dataDir := getenv("YGG_DATA_DIR")
	if dataDir == "" {
		base, err := userConfigDir()
		if err != nil {
			return config{}, fmt.Errorf("can't find a place for Yggdrasil's data (%w): set YGG_DATA_DIR to a folder", err)
		}
		dataDir = filepath.Join(base, "ygg")
	}
	if !filepath.IsAbs(dataDir) {
		return config{}, fmt.Errorf("YGG_DATA_DIR %q must be an absolute path, such as C:\\Users\\you\\ygg-data", dataDir)
	}
	level := slog.LevelInfo
	switch strings.ToLower(getenv("YGG_LOG")) {
	case "", "info":
	case "debug":
		level = slog.LevelDebug
	case "warn":
		level = slog.LevelWarn
	case "error":
		level = slog.LevelError
	default:
		return config{}, fmt.Errorf("YGG_LOG %q must be debug, info, warn or error", getenv("YGG_LOG"))
	}
	delay := 250 * time.Millisecond
	if v := getenv("YGG_MOCK_DELAY_MS"); v != "" {
		ms, err := strconv.Atoi(v)
		if err != nil || ms < 0 || ms > 60_000 {
			return config{}, fmt.Errorf("YGG_MOCK_DELAY_MS %q must be a number of milliseconds from 0 to 60000", v)
		}
		delay = time.Duration(ms) * time.Millisecond
	}
	return config{addr: addr, dataDir: dataDir, level: level, mockDelay: delay}, nil
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
