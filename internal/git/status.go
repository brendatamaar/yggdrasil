package git

import (
	"context"
	"fmt"
	"strconv"
	"strings"
)

// maxStatusFiles caps Status.Files so a repo with a huge untracked tree stays
// cheap to send to the UI. The counts stay exact.
const maxStatusFiles = 5000

// FileChange is one changed path. Index and Worktree are git's one-letter
// codes (M, A, D, R, C, T, U), "?" for untracked, "" for unchanged.
type FileChange struct {
	Path     string `json:"path"`
	OrigPath string `json:"origPath,omitempty"` // set for renames and copies
	Index    string `json:"index"`
	Worktree string `json:"worktree"`
}

// Status is the state of a repo (WS-02).
type Status struct {
	Branch      string       `json:"branch"` // "" when detached
	Detached    bool         `json:"detached"`
	OID         string       `json:"oid"` // "" before the first commit
	Upstream    string       `json:"upstream"`
	HasUpstream bool         `json:"hasUpstream"`
	Ahead       int          `json:"ahead"`
	Behind      int          `json:"behind"`
	Staged      int          `json:"staged"`
	Unstaged    int          `json:"unstaged"`
	Untracked   int          `json:"untracked"`
	Conflicted  int          `json:"conflicted"`
	Files       []FileChange `json:"files"`
	Truncated   bool         `json:"truncated"` // Files was cut at maxStatusFiles
}

// Dirty reports changes to tracked files, including conflicts. Untracked
// files do not count: they never block a checkout or a fast-forward.
func (s Status) Dirty() bool { return s.Staged+s.Unstaged+s.Conflicted > 0 }

// Status runs git status in porcelain v2 with NUL separators, so file names
// with spaces, newlines, CR or non-ASCII characters arrive unchanged.
func (g *Git) Status(ctx context.Context, dir string) (Status, error) {
	out, err := g.run(ctx, dir, "status", true, "status", "--porcelain=v2", "-z", "--branch", "--untracked-files=all")
	if err != nil {
		return Status{}, err
	}
	st, err := parseStatus(out)
	if err != nil {
		return Status{}, &Error{Op: "status", Err: err, UserMsg: "Yggdrasil could not read the output of git status. Update git and try again."}
	}
	return st, nil
}

func parseStatus(out string) (Status, error) {
	var st Status
	toks := strings.Split(out, "\x00")
	for i := 0; i < len(toks); i++ {
		t := toks[i]
		if t == "" {
			continue
		}
		var fc FileChange
		switch {
		case strings.HasPrefix(t, "# "):
			parseHeader(&st, t[2:])
			continue
		case strings.HasPrefix(t, "1 "):
			f := strings.SplitN(t, " ", 9)
			if len(f) != 9 {
				return st, fmt.Errorf("bad status entry %q", t)
			}
			fc = FileChange{Path: f[8], Index: code(f[1][0]), Worktree: code(f[1][1])}
		case strings.HasPrefix(t, "2 "):
			f := strings.SplitN(t, " ", 10)
			if len(f) != 10 || i+1 >= len(toks) {
				return st, fmt.Errorf("bad status entry %q", t)
			}
			i++ // the original path is the next NUL-separated token
			fc = FileChange{Path: f[9], OrigPath: toks[i], Index: code(f[1][0]), Worktree: code(f[1][1])}
		case strings.HasPrefix(t, "u "):
			f := strings.SplitN(t, " ", 11)
			if len(f) != 11 {
				return st, fmt.Errorf("bad status entry %q", t)
			}
			st.Conflicted++
			st.addFile(FileChange{Path: f[10], Index: "U", Worktree: "U"})
			continue
		case strings.HasPrefix(t, "? "):
			st.Untracked++
			st.addFile(FileChange{Path: t[2:], Worktree: "?"})
			continue
		case strings.HasPrefix(t, "! "):
			continue
		default:
			return st, fmt.Errorf("unknown status entry %q", t)
		}
		if fc.Index != "" {
			st.Staged++
		}
		if fc.Worktree != "" {
			st.Unstaged++
		}
		st.addFile(fc)
	}
	return st, nil
}

func (s *Status) addFile(fc FileChange) {
	if len(s.Files) >= maxStatusFiles {
		s.Truncated = true
		return
	}
	s.Files = append(s.Files, fc)
}

// code maps git's "." (unchanged) to "".
func code(b byte) string {
	if b == '.' {
		return ""
	}
	return string(b)
}

func parseHeader(st *Status, h string) {
	key, val, _ := strings.Cut(h, " ")
	switch key {
	case "branch.oid":
		if val != "(initial)" {
			st.OID = val
		}
	case "branch.head":
		if val == "(detached)" {
			st.Detached = true
		} else {
			st.Branch = val
		}
	case "branch.upstream":
		st.Upstream, st.HasUpstream = val, true
	case "branch.ab":
		a, b, _ := strings.Cut(val, " ")
		st.Ahead, _ = strconv.Atoi(strings.TrimPrefix(a, "+"))
		st.Behind, _ = strconv.Atoi(strings.TrimPrefix(b, "-"))
	}
}
