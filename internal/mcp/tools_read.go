package mcp

import (
	"context"
	"fmt"
	"strings"

	cmcp "github.com/brendatamaar/yggdrasil/internal/core/mcp"
)

// runRead answers the read tools (MCP-01). Metadata tools may name any visible
// repo; content tools only repos in the task's scope (SF-02).
func (rc *reqCtx) runRead(ctx context.Context, c cmcp.Call) (result, error) {
	rd := rc.s.cfg.Reader
	switch v := c.(type) {
	case cmcp.ListRepos:
		var b strings.Builder
		for _, r := range rc.snap.Repos {
			fmt.Fprintf(&b, "%s (id %s) default branch %s", r.Name, r.ID, orDash(r.DefaultBranch))
			if r.InScope {
				b.WriteString("; you work in this repo")
			} else {
				b.WriteString("; read-only metadata")
			}
			if r.Worktree != "" {
				fmt.Fprintf(&b, "; task branch %s", orDash(r.WorktreeBranch))
			}
			b.WriteByte('\n')
		}
		if b.Len() == 0 {
			return result{text: "This project has no tracked repos."}, nil
		}
		return result{text: b.String()}, nil

	case cmcp.RepoStatus:
		r, err := rc.repo(cmcp.ToolRepoStatus, v.Repo)
		if err != nil {
			return result{}, err
		}
		at := Loc{Repo: r.ID}
		if r.InScope {
			at = r.loc()
		}
		st, err := rd.Status(ctx, at)
		if err != nil {
			return result{}, fmt.Errorf("repo status %s: %w", r.ID, err)
		}
		return result{text: formatStatus(r.Name, st)}, nil

	case cmcp.RemoteStatus:
		r, err := rc.repo(cmcp.ToolRemoteStatus, v.Repo)
		if err != nil {
			return result{}, err
		}
		ri, err := rc.s.cfg.Hosts.Remote(ctx, r.ID)
		if err != nil {
			return result{}, fmt.Errorf("remote status %s: %w", r.ID, err)
		}
		if len(ri.Lines) == 0 {
			return result{text: r.Name + ": no host data yet (the repo has no supported remote, or it has not been checked)."}, nil
		}
		return result{text: r.Name + "\n" + strings.Join(ri.Lines, "\n")}, nil

	case cmcp.RecentCommits:
		r, err := rc.repo(cmcp.ToolRecentCommits, v.Repo)
		if err != nil {
			return result{}, err
		}
		cs, err := rd.Commits(ctx, r.loc(), v.Limit)
		if err != nil {
			return result{}, fmt.Errorf("recent commits %s: %w", r.ID, err)
		}
		if len(cs) == 0 {
			return result{text: r.Name + " has no commits."}, nil
		}
		var b strings.Builder
		for _, x := range cs {
			fmt.Fprintf(&b, "%s %s %s %s\n", x.ID, x.When, x.Author, x.Subject)
		}
		return result{text: b.String()}, nil

	case cmcp.ChangedFiles:
		r, err := rc.repo(cmcp.ToolChangedFiles, v.Repo)
		if err != nil {
			return result{}, err
		}
		ch, err := rd.Changes(ctx, r.loc())
		if err != nil {
			return result{}, fmt.Errorf("changed files %s: %w", r.ID, err)
		}
		if len(ch) == 0 {
			return result{text: r.Name + " has no changes."}, nil
		}
		var b strings.Builder
		for _, x := range ch {
			fmt.Fprintf(&b, "%s %s\n", x.Status, x.Path)
		}
		return result{text: b.String()}, nil

	case cmcp.GetDiff:
		r, err := rc.repo(cmcp.ToolGetDiff, v.Repo)
		if err != nil {
			return result{}, err
		}
		text, cut, err := rd.Diff(ctx, r.loc(), v.Path, v.MaxBytes)
		if err != nil {
			return result{}, fmt.Errorf("diff %s: %w", r.ID, err)
		}
		if text == "" {
			return result{text: "No changes."}, nil
		}
		return result{text: text, truncated: cut}, nil
	}
	return result{}, fmt.Errorf("runRead: unhandled %T", c)
}

func orDash(s string) string {
	if s == "" {
		return "-"
	}
	return s
}

func formatStatus(name string, st RepoStatus) string {
	var b strings.Builder
	switch {
	case st.Detached:
		fmt.Fprintf(&b, "%s: detached HEAD", name)
	default:
		fmt.Fprintf(&b, "%s: branch %s", name, orDash(st.Branch))
	}
	if st.Upstream != "" {
		fmt.Fprintf(&b, " (upstream %s, ahead %d, behind %d)", st.Upstream, st.Ahead, st.Behind)
	} else {
		b.WriteString(" (no upstream)")
	}
	fmt.Fprintf(&b, "\nstaged %d, unstaged %d, untracked %d, conflicted %d", st.Staged, st.Unstaged, st.Untracked, st.Conflicted)
	if st.Head != "" {
		fmt.Fprintf(&b, "\nhead %s", st.Head)
	}
	return b.String()
}
