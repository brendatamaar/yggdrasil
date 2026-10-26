package safety

import (
	"context"
	"crypto/sha256"
	"encoding/binary"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"io/fs"
	"os"
	"path"
	"path/filepath"
	"sort"
	"strings"
	"unicode/utf8"

	"github.com/brendatamaar/yggdrasil/internal/core/guard"
	"github.com/brendatamaar/yggdrasil/internal/platform"
)

// Limits of the agent-config scan (SF-09).
const (
	// MaxConfigFileSize is the largest agent-config file the scan reads.
	MaxConfigFileSize = 1 << 20
	// maxWalked bounds how many directory entries the walk visits.
	maxWalked = 500000
)

// MaxConfigFiles is the most matching files a scan accepts. It is a variable
// only so the contract suite can lower it; nothing else writes it.
var MaxConfigFiles = 5000

// ErrScan is wrapped by every scan failure. The message names the file
// relative to the repo and never holds an absolute path.
var ErrScan = errors.New("the agent configuration of this repo could not be read completely")

// ConfigScanner reads the agent configuration of a repo for the trust
// decision. paths come from agents.RepoConfigProvider.ConfigPaths (files or
// folders, relative to the repo, forward slashes) and names from
// agents.ConfigNameProvider.ConfigNames (file names matched at any depth).
type ConfigScanner interface {
	Scan(ctx context.Context, dir string, paths, names []string) (guard.Scan, error)
}

// FSScanner is the ConfigScanner that reads the disk.
type FSScanner struct{}

// Scan implements ConfigScanner.
func (FSScanner) Scan(ctx context.Context, dir string, paths, names []string) (guard.Scan, error) {
	return Scan(ctx, dir, paths, names)
}

// ScanInput is one matched file handed to BuildScan.
type ScanInput struct {
	Path    string // relative to the repo, forward slashes
	Content []byte
}

func cleanScanPath(p string) string {
	p = strings.Trim(path.Clean(strings.ReplaceAll(p, `\`, "/")), "/")
	if p == "." {
		return ""
	}
	return p
}

// SelectConfig reports whether the repo-relative, slash-separated path rel is
// agent configuration: it is one of paths or below one of them, or its base
// name is one of names. Comparison folds case, so a scan on a case-insensitive
// disk can never miss a file by case. Anything inside .git is never selected.
func SelectConfig(rel string, paths, names []string) bool {
	segs := strings.Split(rel, "/")
	for _, s := range segs {
		if strings.EqualFold(s, ".git") {
			return false
		}
	}
	base := segs[len(segs)-1]
	for _, n := range names {
		if n != "" && strings.EqualFold(base, n) {
			return true
		}
	}
	for _, p := range paths {
		p = cleanScanPath(p)
		if p == "" {
			continue
		}
		if len(rel) == len(p) && strings.EqualFold(rel, p) {
			return true
		}
		if len(rel) > len(p) && rel[len(p)] == '/' && strings.EqualFold(rel[:len(p)], p) {
			return true
		}
	}
	return false
}

// LinkBlocksConfig reports whether a link, junction or special entry at rel
// would hide or replace agent configuration: rel is a config path, a folder
// above one, something below one, or is named like a config name. The scan
// refuses such an entry because it can't see what the link leads to.
func LinkBlocksConfig(rel string, paths, names []string) bool {
	if SelectConfig(rel, paths, names) {
		return true
	}
	return isAboveConfigPath(rel, paths)
}

// isAboveConfigPath is true when rel is a folder on the way to one of paths.
func isAboveConfigPath(rel string, paths []string) bool {
	for _, p := range paths {
		p = cleanScanPath(p)
		if p != "" && len(p) > len(rel) && p[len(rel)] == '/' && strings.EqualFold(p[:len(rel)], rel) {
			return true
		}
	}
	return false
}

// skippedFolders are dependency and build folders the walk does not enter at
// the repo root level (nested ones are scanned, S7)
// unless a config path runs through them, so a big repo does not exceed the
// walk cap for ever. Config files that are only matched by name inside them
// are not seen: they are third-party code, not the repo's own agent setup.
var skippedFolders = []string{"node_modules", "target", "build", "dist", ".venv", "vendor"}

func skippable(rel, name string, paths []string) bool {
	if strings.Contains(rel, "/") {
		return false // only the repo root level is skipped (S7)
	}
	for _, s := range skippedFolders {
		if strings.EqualFold(name, s) {
			return !isAboveConfigPath(rel, paths) && !SelectConfig(rel, paths, nil)
		}
	}
	return false
}

// isLinkEntry is true for a symlink, a junction (a reparse point Go may report
// as a plain directory) or any other irregular entry.
func isLinkEntry(p string, d fs.DirEntry) bool {
	if d.Type()&(fs.ModeSymlink|fs.ModeIrregular) != 0 {
		return true
	}
	if d.IsDir() {
		r, err := platform.IsReparsePoint(p)
		return err != nil || r
	}
	return false
}

// Scan walks dir (skipping .git), reads every selected file and returns the
// hash and the "would run" summary. A file over 1 MiB, a selected link and any
// read error fail the whole scan, naming the file relative to dir.
func Scan(ctx context.Context, dir string, paths, names []string) (guard.Scan, error) {
	var in []ScanInput
	visited := 0
	err := filepath.WalkDir(dir, func(p string, d fs.DirEntry, werr error) error {
		if err := ctx.Err(); err != nil {
			return err
		}
		relOS, rerr := filepath.Rel(dir, p)
		if rerr != nil {
			return ErrScan
		}
		rel := filepath.ToSlash(relOS)
		if rel == "." {
			if werr != nil {
				return fmt.Errorf("%w: the repo folder can't be opened", ErrScan)
			}
			return nil
		}
		if werr != nil {
			if LinkBlocksConfig(rel, paths, names) {
				return fmt.Errorf("%w: %s can't be read", ErrScan, rel)
			}
			return nil
		}
		if visited++; visited > maxWalked {
			return fmt.Errorf("%w: the repo has too many files", ErrScan)
		}
		if LinkBlocksConfig(rel, paths, names) && isLinkEntry(p, d) {
			return fmt.Errorf("%w: %s is a link", ErrScan, rel)
		}
		if d.IsDir() {
			if strings.EqualFold(d.Name(), ".git") || skippable(rel, d.Name(), paths) {
				return filepath.SkipDir
			}
			return nil
		}
		if !SelectConfig(rel, paths, names) {
			return nil
		}
		if !d.Type().IsRegular() {
			return fmt.Errorf("%w: %s is a link or special file", ErrScan, rel)
		}
		if len(in) >= MaxConfigFiles {
			return fmt.Errorf("%w: more than %d configuration files", ErrScan, MaxConfigFiles)
		}
		data, rerr := readLimited(p)
		if rerr != nil {
			if errors.Is(rerr, errTooBig) {
				return fmt.Errorf("%w: %s is larger than 1 MiB", ErrScan, rel)
			}
			return fmt.Errorf("%w: %s can't be read", ErrScan, rel)
		}
		in = append(in, ScanInput{Path: rel, Content: data})
		return nil
	})
	if err != nil {
		if errors.Is(err, ErrScan) || ctx.Err() != nil {
			return guard.Scan{}, err
		}
		return guard.Scan{}, ErrScan
	}
	// Belt and braces: every segment of every config path must be a plain
	// entry, whatever the walk saw (a link may have appeared since).
	for _, p := range paths {
		p = cleanScanPath(p)
		if p == "" {
			continue
		}
		segs := strings.Split(p, "/")
		for i := range segs {
			sub := strings.Join(segs[:i+1], "/")
			full := filepath.Join(dir, filepath.FromSlash(sub))
			if _, lerr := os.Lstat(full); lerr != nil {
				if errors.Is(lerr, fs.ErrNotExist) {
					break
				}
				return guard.Scan{}, fmt.Errorf("%w: %s can't be checked", ErrScan, sub)
			}
			if r, rerr := platform.IsReparsePoint(full); rerr != nil || r {
				return guard.Scan{}, fmt.Errorf("%w: %s is a link", ErrScan, sub)
			}
		}
	}
	return BuildScan(in), nil
}

var errTooBig = errors.New("file too big")

func readLimited(p string) ([]byte, error) {
	f, err := os.Open(p) //nolint:gosec // p comes from a walk of the repo
	if err != nil {
		return nil, err
	}
	defer f.Close()
	data, err := io.ReadAll(io.LimitReader(f, MaxConfigFileSize+1))
	if err != nil {
		return nil, err
	}
	if len(data) > MaxConfigFileSize {
		return nil, errTooBig
	}
	return data, nil
}

// BuildScan is the pure part of a scan: the hash over the sorted (path,
// content) pairs and the per-file summary. The input order does not matter.
// The fake scanner uses it too, so the two cannot drift apart.
func BuildScan(in []ScanInput) guard.Scan {
	files := append([]ScanInput(nil), in...)
	sort.Slice(files, func(i, j int) bool { return files[i].Path < files[j].Path })
	h := sha256.New()
	var out guard.Scan
	var n [8]byte
	writeLen := func(s int) {
		binary.BigEndian.PutUint64(n[:], uint64(s)) //nolint:gosec // lengths are never negative
		h.Write(n[:])
	}
	for _, f := range files {
		writeLen(len(f.Path))
		h.Write([]byte(f.Path))
		writeLen(len(f.Content))
		h.Write(f.Content)
		sf := guard.ScanFile{Path: f.Path}
		if !utf8.Valid(f.Content) {
			sf.Opaque = true
		} else {
			sf.WouldRun, sf.Opaque = wouldRun(f.Path, f.Content)
		}
		out.Files = append(out.Files, sf)
	}
	if len(files) > 0 {
		out.Hash = hex.EncodeToString(h.Sum(nil))
	}
	return out
}

// wouldRun extracts, best effort and without running anything, the commands a
// settings file would start: hooks and MCP servers. opaque is true when a file
// that should be JSON is not.
func wouldRun(rel string, content []byte) (lines []string, opaque bool) {
	if !strings.HasSuffix(strings.ToLower(path.Base(rel)), ".json") {
		return nil, false
	}
	var v any
	if err := json.Unmarshal(content, &v); err != nil {
		return nil, true
	}
	var out []string
	collect(v, &out)
	sort.Strings(out)
	return out, false
}

func collect(v any, out *[]string) {
	switch t := v.(type) {
	case map[string]any:
		for k, child := range t {
			switch strings.ToLower(k) {
			case "hooks":
				collectHooks(child, out)
			case "mcpservers", "mcp", "servers":
				collectServers(child, out)
			}
			collect(child, out)
		}
	case []any:
		for _, c := range t {
			collect(c, out)
		}
	}
}

func collectHooks(v any, out *[]string) {
	switch t := v.(type) {
	case map[string]any:
		if c, ok := t["command"].(string); ok {
			*out = append(*out, "hook: "+clean(c))
		}
		for _, child := range t {
			collectHooks(child, out)
		}
	case []any:
		for _, c := range t {
			collectHooks(c, out)
		}
	}
}

func collectServers(v any, out *[]string) {
	m, ok := v.(map[string]any)
	if !ok {
		return
	}
	for name, s := range m {
		sm, ok := s.(map[string]any)
		if !ok {
			continue
		}
		var parts []string
		switch c := sm["command"].(type) {
		case string:
			parts = append(parts, c)
		case []any:
			for _, x := range c {
				if s, ok := x.(string); ok {
					parts = append(parts, s)
				}
			}
		}
		if len(parts) == 0 {
			if u, ok := sm["url"].(string); ok {
				*out = append(*out, "MCP server "+clean(name)+": connects to "+clean(u))
			}
			continue
		}
		if args, ok := sm["args"].([]any); ok {
			for _, x := range args {
				if s, ok := x.(string); ok {
					parts = append(parts, s)
				}
			}
		}
		*out = append(*out, "MCP server "+clean(name)+": "+clean(strings.Join(parts, " ")))
	}
}

var scanRedactor = NewRedactor()

// clean keeps a summary line to one printable line.
func clean(s string) string {
	s = strings.Map(func(r rune) rune {
		if r < 0x20 || r == 0x7f {
			return ' '
		}
		return r
	}, s)
	s = scanRedactor.Redact(s) // a hook or server URL may carry a token (invariant 4)
	if len(s) > 200 {
		s = s[:200]
		for !utf8.ValidString(s) {
			s = s[:len(s)-1]
		}
		s += "..."
	}
	return s
}
