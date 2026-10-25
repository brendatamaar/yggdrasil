package search

import (
	"fmt"

	"github.com/brendatamaar/yggdrasil/internal/core"
)

// Limits of a search (design: Caps).
const (
	MaxPerRepo      = 200
	MaxTotal        = 1000
	MaxTextRunes    = 300
	MaxPatternBytes = 500
	MaxPaths        = 20
	MaxPathBytes    = 200
	MaxRepoFilter   = 500
)

// Mode says how the pattern is read.
type Mode string

const (
	// ModeLiteral searches for the text as typed (git grep -F). The default.
	ModeLiteral Mode = "literal"
	// ModeRegex reads the pattern as an extended regex (git grep -E).
	ModeRegex Mode = "regex"
)

// Validate reports whether the mode is known.
func (m Mode) Validate() error {
	switch m {
	case ModeLiteral, ModeRegex:
		return nil
	}
	return fmt.Errorf("search mode %q: must be %q or %q", string(m), ModeLiteral, ModeRegex)
}

// ProblemKind says why a repo could not be searched.
type ProblemKind string

const (
	ProblemTimedOut  ProblemKind = "timed_out"
	ProblemGitFailed ProblemKind = "git_failed"
	ProblemMissing   ProblemKind = "missing"
)

// Validate reports whether the kind is known.
func (k ProblemKind) Validate() error {
	switch k {
	case ProblemTimedOut, ProblemGitFailed, ProblemMissing:
		return nil
	}
	return fmt.Errorf("search problem %q is unknown", string(k))
}

// Query is a parsed search. Build it with ParseQuery. The pattern is data: the
// shell passes it after -e and never as an option.
type Query struct {
	Pattern string
	Mode    Mode
	Case    bool          // case-sensitive
	Repos   []core.RepoID // empty = every active repo in scope
	Paths   []string      // optional pathspec globs, relative to each repo
}

// Match is one matching line.
type Match struct {
	Repo core.RepoID
	Path string
	Line int
	Col  int
	Text string // at most MaxTextRunes runes
}

// RepoRef names a repo to search.
type RepoRef struct {
	ID   core.RepoID
	Name string
	Path string
}

// Search is the state of one request. The zero value is "not begun". It has
// two phases: searching (Pending is not empty) and done (Pending is empty
// after Begin). Treat it as immutable: Step returns a new value.
type Search struct {
	Begun     bool
	Query     Query
	Repos     []RepoRef // every repo of Begin, deduplicated, in request order
	Pending   map[core.RepoID]bool
	Results   []Match // ordered by (repo name, repo id, path, line, col)
	PerRepo   map[core.RepoID]int
	Problems  map[core.RepoID]ProblemKind
	Truncated bool // a cap cut a match, or a worker said it truncated
}

// Searching reports whether some repo has not answered yet.
func (s Search) Searching() bool { return s.Begun && len(s.Pending) > 0 }

// Done reports whether every repo has answered or the deadline passed.
func (s Search) Done() bool { return s.Begun && len(s.Pending) == 0 }

// Input is a closed set: Begin, RepoResult, RepoFailed, Deadline.
type Input interface{ isInput() }

// Begin starts a search over Repos (already filtered to active tracked repos).
type Begin struct {
	Query Query
	Repos []RepoRef
}

// RepoResult is a worker's answer. Truncated is set when the worker stopped
// reading at its own cap.
type RepoResult struct {
	Repo      core.RepoID
	Matches   []Match
	Truncated bool
}

// RepoFailed is a worker's failure.
type RepoFailed struct {
	Repo core.RepoID
	Kind ProblemKind
}

// Deadline is fed by the shell timer when the overall time is up.
type Deadline struct{}

func (Begin) isInput()      {}
func (RepoResult) isInput() {}
func (RepoFailed) isInput() {}
func (Deadline) isInput()   {}

// Command is a closed set: Grep, CancelAll. The core never performs them.
type Command interface{ isCommand() }

// Grep asks the shell to search one repo (read-only, idempotent).
type Grep struct {
	Repo  RepoRef
	Query Query
}

// CancelAll asks the shell to cancel every running worker.
type CancelAll struct{}

func (Grep) isCommand()      {}
func (CancelAll) isCommand() {}
