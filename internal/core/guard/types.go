package guard

import (
	"fmt"
	"strings"

	"github.com/brendatamaar/yggdrasil/internal/core"
)

// Enforcement is how strongly Yggdrasil's limits hold for an adapter (SF-06).
// It is a core-side copy of agents.Enforcement with the same wire values.
type Enforcement string

const (
	// EnforcementHook: a pre-tool hook blocks calls and fails closed.
	EnforcementHook Enforcement = "hook"
	// EnforcementLimited: limits are only requested; worktree-only Code mode.
	EnforcementLimited Enforcement = "limited"
)

// Validate reports whether e is known.
func (e Enforcement) Validate() error {
	switch e {
	case EnforcementHook, EnforcementLimited:
		return nil
	}
	return fmt.Errorf("unknown enforcement %q", string(e))
}

// Tool is the closed set of tool kinds a call can have. An adapter maps its
// own tool names onto it; anything it cannot map is ToolOther and is denied.
type Tool string

const (
	ToolRead     Tool = "read"
	ToolSearch   Tool = "search"
	ToolWrite    Tool = "write"
	ToolExec     Tool = "exec"
	ToolSubagent Tool = "subagent"
	ToolOther    Tool = "other"
)

// Validate reports whether t is in the closed set.
func (t Tool) Validate() error {
	switch t {
	case ToolRead, ToolSearch, ToolWrite, ToolExec, ToolSubagent, ToolOther:
		return nil
	}
	return fmt.Errorf("unknown tool %q", string(t))
}

// PathRef is one path a call names, resolved by the shell.
type PathRef struct {
	// Canon is the long form, symlinks and junctions resolved, absolute.
	Canon string
	// OK is false when the shell could not resolve the path: treat as outside.
	OK bool
	// Raw is the path as the agent wrote it, for the audit entry only.
	Raw string
}

// Call is one tool call, parsed and resolved by the shell.
type Call struct {
	Tool Tool
	// ToolName is the CLI's own tool name, for the audit entry of a denied
	// ToolOther call. It never carries a path.
	ToolName string
	Paths    []PathRef
	// Globs are glob patterns (Search, Glob).
	Globs []string
	// Argv is the parsed command line (Exec).
	Argv []string
	// Program is the executable the shell resolved argv[0] to, with platform
	// resolution and the working directory never searched (Exec). D11 needs it.
	Program *PathRef
	// ParseErr: the command line had shell syntax we refuse (Exec).
	ParseErr bool
	// Isolation is "" or the agent-native isolation a sub-agent asked for.
	Isolation string
}

// TailRule says whether a CommandRule accepts arguments after its fixed ones.
type TailRule string

const (
	// TailNone: no extra arguments.
	TailNone TailRule = "none"
	// TailPaths: extra arguments must be plain words without a leading "-" or
	// paths inside the read roots.
	TailPaths TailRule = "paths"
)

// Validate reports whether t is known.
func (t TailRule) Validate() error {
	switch t {
	case TailNone, TailPaths:
		return nil
	}
	return fmt.Errorf("unknown tail rule %q", string(t))
}

// CommandRule is one entry of the command allowlist.
type CommandRule struct {
	// Program is the base name without extension: "go", "npm", "mvnw".
	Program string
	// Args are the fixed leading arguments: ["run", "test"].
	Args []string
	Tail TailRule
}

// Validate reports whether the rule is well formed.
func (r CommandRule) Validate() error {
	if r.Program == "" || strings.ContainsAny(r.Program, `/\`) || strings.ContainsRune(r.Program, 0) {
		return fmt.Errorf("allowlist rule %q needs a program name without a path", r.Program)
	}
	return r.Tail.Validate()
}

// Policy is what the tasks service computes for a run and writes to the
// hook's config file.
type Policy struct {
	Mode       core.TaskMode
	ReadRoots  []string // canonical, absolute
	WriteRoots []string // canonical, absolute; empty in Ask mode
	Allow      []CommandRule
	// FoldCase compares paths case-insensitively (Windows).
	FoldCase    bool
	Enforcement Enforcement
}

// Validate checks the rules ParsePolicy rejects: known mode, at least one
// absolute read root, absolute roots without "..", no write root in Ask mode,
// valid rules.
func (p Policy) Validate() error {
	if err := p.Mode.Validate(); err != nil {
		return err
	}
	if err := p.Enforcement.Validate(); err != nil {
		return err
	}
	if len(p.ReadRoots) == 0 {
		return fmt.Errorf("the policy has no readable folder")
	}
	if p.Mode == core.ModeAsk && len(p.WriteRoots) > 0 {
		return fmt.Errorf("an Ask policy cannot have a writable folder")
	}
	for _, r := range p.ReadRoots {
		if err := checkRoot(r); err != nil {
			return fmt.Errorf("read root: %w", err)
		}
	}
	for _, r := range p.WriteRoots {
		if err := checkRoot(r); err != nil {
			return fmt.Errorf("write root: %w", err)
		}
	}
	for i, r := range p.Allow {
		if err := r.Validate(); err != nil {
			return fmt.Errorf("allowlist rule %d: %w", i+1, err)
		}
	}
	return nil
}

func checkRoot(r string) error {
	if r == "" || strings.ContainsRune(r, 0) {
		return fmt.Errorf("a folder is empty or holds a NUL character")
	}
	if !looksAbsolute(r) {
		return fmt.Errorf("folder %q is not absolute", r)
	}
	var segs []string
	for _, seg := range strings.FieldsFunc(r, func(c rune) bool { return c == '/' || c == '\\' }) {
		if seg == ".." {
			return fmt.Errorf("folder %q has a parent-folder part", r)
		}
		if seg != "." {
			segs = append(segs, seg)
		}
	}
	// A whole drive, the filesystem root or a bare network host or share is
	// not a repo: it would let the policy reach far more than one project.
	need := 1 // "/x": one part; "C:\x": the drive and one part below it
	switch {
	case strings.HasPrefix(r, `\\`) || strings.HasPrefix(r, "//"):
		need = 3 // host, share and one more part (or `?`, drive and one more)
	case len(segs) > 0 && isDriveSeg(segs[0]):
		need = 2
	}
	if len(segs) < need {
		return fmt.Errorf("folder %q is a drive, a share or the filesystem root, not a project folder", r)
	}
	return nil
}

// looksAbsolute is a shape check (this package touches no filesystem): "/x",
// `\\host\share`, `\\?\C:\x` or `C:\x` / `C:/x`.
func looksAbsolute(p string) bool {
	if strings.HasPrefix(p, "/") || strings.HasPrefix(p, `\\`) {
		return true
	}
	return len(p) >= 3 && p[1] == ':' && (p[2] == '\\' || p[2] == '/') &&
		(p[0] >= 'A' && p[0] <= 'Z' || p[0] >= 'a' && p[0] <= 'z')
}

// TurnID identifies one turn of a task. internal/core/tasks has its own TurnID
// with the same wire value; the shell converts, because domain packages under
// internal/core do not import each other (archtest).
type TurnID string

// Validate reports whether the ID is well formed.
func (id TurnID) Validate() error {
	if id == "" || strings.ContainsAny(string(id), " \t\r\n/\\") {
		return fmt.Errorf("turn id %q: %w", string(id), core.ErrInvalidID)
	}
	return nil
}

// Snapshot is a read-only fingerprint of a working tree (SF-07).
type Snapshot struct {
	Head     string // the commit
	Status   string // hash of `git status --porcelain=v2 -z`
	DiffHash string // hash of the working diff
	// Paths are the changed paths, attached to the After snapshot so the user
	// can see what changed. They do not take part in Equal.
	Paths []string
}

// Equal reports whether two snapshots describe the same tree.
func (s Snapshot) Equal(o Snapshot) bool {
	return s.Head == o.Head && s.Status == o.Status && s.DiffHash == o.DiffHash
}

// Change is one path that differs between two snapshots.
type Change struct{ Path string }

// ScanFile is one agent-config file found in a repo.
type ScanFile struct {
	// Path is relative to the repo root, with forward slashes.
	Path string
	// WouldRun says what the file would start, in plain words (hooks, MCP
	// servers). Empty with Opaque set when the file could not be read as text.
	WouldRun []string
	Opaque   bool
}

// Scan is what the shell found when it read a repo's agent config.
type Scan struct {
	// Hash is sha256 over the sorted paths and contents, 64 hex digits.
	Hash  string
	Files []ScanFile
}

// Empty reports whether the repo ships no agent config.
func (s Scan) Empty() bool { return len(s.Files) == 0 }

// TrustView is the trust state as Admit sees it.
type TrustView string

const (
	// TrustNone: the repo ships no agent config; nothing to trust.
	TrustNone TrustView = "none"
	// TrustTrusted: the user trusted the exact hash the repo has now.
	TrustTrusted TrustView = "trusted"
	// TrustUntrusted: the repo has agent config the user has not trusted.
	TrustUntrusted TrustView = "untrusted"
)

// Validate reports whether v is known.
func (v TrustView) Validate() error {
	switch v {
	case TrustNone, TrustTrusted, TrustUntrusted:
		return nil
	}
	return fmt.Errorf("unknown trust view %q", string(v))
}

// Request asks Admit whether a turn may run in a mode.
type Request struct {
	Mode core.TaskMode
	// Previous is the task's earlier write mode when this is a mode switch,
	// nil for a first turn.
	Previous *core.TaskMode
	OptIn    bool
	// Trust has one view per repo the mode touches (the task's repo for
	// Code, the main checkout for CurrentBranch, every repo for Project). A
	// non-Ask mode is admitted only if all are Trusted or None; an empty
	// list is refused.
	Trust       []TrustView
	Enforcement Enforcement
}

// Warning is shown with an admitted turn.
type Warning string

const (
	WarningNone              Warning = ""
	WarningEditsMainCheckout Warning = "edits_main_checkout"
	WarningEditsAllRepos     Warning = "edits_all_repos"
)

// Admission is the answer of Admit: admitted (maybe with a warning) or
// refused with a reason.
type Admission struct {
	Admitted bool
	Warning  Warning
	// Refusal is set when Admitted is false.
	Refusal Reason
}
