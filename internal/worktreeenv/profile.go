package worktreeenv

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"

	"github.com/brendatamaar/yggdrasil/internal/core"
	env "github.com/brendatamaar/yggdrasil/internal/core/worktreeenv"
)

// CopyProfile copies the repo's bootstrap profile files into the worktree
// (WE-01). Existing files are never overwritten and nothing is deleted. The
// trigger says who started the worktree: for an agent task the copy happens
// only when the repo's "also copy for agent tasks" toggle is on.
//
// The core decides which files are copied; this function observes the files,
// runs the copies and writes the audit entry.
func (s *Service) CopyProfile(ctx context.Context, w core.Worktree, trigger env.Trigger) error {
	settings, err := s.c.Settings.RepoEnv(ctx, w.RepoID)
	if err != nil {
		return fmt.Errorf("load profile settings of repo %s: %w", w.RepoID, err)
	}
	if len(settings.Profile) == 0 {
		return nil
	}
	var repoDir string
	if !(trigger == env.TriggerAgentTask && !settings.AgentCopy) {
		repoDir, err = s.c.RepoDir(ctx, w.RepoID)
		if err != nil {
			return fmt.Errorf("find repo %s: %w", w.RepoID, err)
		}
	}
	job, cmds := env.Profile(env.JobIdle{}, env.CopyRequested{
		Worktree: w.ID, Trigger: trigger, Entries: settings.Profile, AgentCopy: settings.AgentCopy, Now: s.c.Now(),
	})
	for len(cmds) > 0 {
		if err := ctx.Err(); err != nil {
			return fmt.Errorf("copy profile files: %w", err)
		}
		c := cmds[0]
		cmds = cmds[1:]
		var in env.ProfileInput
		switch v := c.(type) {
		case env.Observe:
			src, dest, err := s.c.FS.Observe(ctx, repoDir, w.Path, v.Entry)
			if err != nil {
				src, dest = env.SourceUnreadable{}, env.DestExists
			}
			in = env.Observed{Entry: v.Entry, Source: src, Dest: dest}
		case env.CopyFile:
			if err := s.c.FS.CopyExclusive(ctx, repoDir, w.Path, v.Entry, env.MaxFileBytes); err != nil {
				in = env.FileCopyFailed{Entry: v.Entry, Reason: copyFailure(err)}
			} else {
				in = env.FileCopied{Entry: v.Entry}
			}
		case env.ProfileAudit:
			if err := s.auditProfile(ctx, v); err != nil {
				return err
			}
		case env.ProfilePublish:
			if s.c.OnProfileDone != nil {
				s.c.OnProfileDone(v.Worktree)
			}
		}
		if in != nil {
			var next []env.ProfileCommand
			job, next = env.Profile(job, in)
			cmds = append(cmds, next...)
		}
	}
	if _, ok := job.(env.JobDone); !ok {
		return errors.New("worktreeenv: internal error: profile copy did not finish")
	}
	return nil
}

// copyFailure is the plain-language reason; it holds no paths.
func copyFailure(err error) string {
	switch {
	case errors.Is(err, ErrDestExists):
		return "the file already exists in the worktree"
	case errors.Is(err, ErrTooLarge):
		return "the file is too large"
	case errors.Is(err, ErrNotRegular):
		return "the file is not a regular file"
	}
	return "the file could not be copied"
}

func (s *Service) auditProfile(ctx context.Context, a env.ProfileAudit) error {
	names := make([]string, len(a.Names))
	for i, n := range a.Names {
		names[i] = string(n)
	}
	detail, err := json.Marshal(map[string]any{
		"trigger": string(a.Trigger), "copied": a.Copied, "skipped": a.Skipped, "failed": a.Failed, "names": names,
	})
	if err != nil {
		return fmt.Errorf("encode audit detail: %w", err)
	}
	actor := "system"
	if a.Trigger != env.TriggerAgentTask {
		actor = "user"
	}
	e := auditEntry(s.c.NewID("aud"), actor, string(a.Action), wtSubject(a.Worktree), s.c.Now().UTC(), detail)
	if err := s.c.Audit.Append(ctx, e); err != nil {
		return fmt.Errorf("write audit entry: %w", err)
	}
	return nil
}

// SetProfile validates and stores a repo's profile paths and the agent-copy
// toggle, with the audit entry in the same commit. Paths are parsed at this
// boundary (ParseProfile); the error says which rule a path broke.
func (s *Service) SetProfile(ctx context.Context, repo core.RepoID, paths []string, agentCopy bool) error {
	parsed, err := env.ParseProfile(paths)
	if err != nil {
		return &Error{Msg: "Profile files must be paths inside the repo, one file each: " + err.Error() + ".", Err: err}
	}
	detail, err := json.Marshal(map[string]any{"files": len(parsed), "agentCopy": agentCopy})
	if err != nil {
		return fmt.Errorf("encode audit detail: %w", err)
	}
	now := s.c.Now().UTC()
	e := auditEntry(s.c.NewID("aud"), "user", "worktreeenv.profile_settings", "repo:"+string(repo), now, detail)
	if err := s.c.Settings.PutRepoEnv(ctx, repo, RepoEnv{Profile: parsed, AgentCopy: agentCopy}, now, e); err != nil {
		return fmt.Errorf("save profile settings of repo %s: %w", repo, err)
	}
	return nil
}

// SetPortRange validates and stores the port range ("lo-hi"). Blocks that
// are already handed out stay where they are.
func (s *Service) SetPortRange(ctx context.Context, raw string) error {
	r, err := env.ParseRange(raw)
	if err != nil {
		return &Error{Msg: "Use a port range like 20000-29999 (from 1024 up, at least 20 ports): " + err.Error() + ".", Err: err}
	}
	detail, err := json.Marshal(map[string]int{"lo": r.Lo, "hi": r.Hi})
	if err != nil {
		return fmt.Errorf("encode audit detail: %w", err)
	}
	e := auditEntry(s.c.NewID("aud"), "user", string(env.ActionRange), "settings:"+"worktreeenv.port_range", s.c.Now().UTC(), detail)
	if err := s.c.Settings.SetPortRange(ctx, r, e); err != nil {
		return fmt.Errorf("save port range: %w", err)
	}
	return nil
}
