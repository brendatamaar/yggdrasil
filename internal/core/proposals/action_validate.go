package proposals

// The protected agent-config names. internal/core/guard keeps the same lists
// (the two packages may not import each other); protected_test.go fails if
// they differ.
var protectedDirNames = []string{".claude", ".opencode", ".codex", ".gemini", ".cursor", ".husky", ".githooks"}
var protectedFileNames = []string{".mcp.json", "CLAUDE.md", "CLAUDE.local.md", "AGENTS.md", "opencode.json", "GEMINI.md", ".gitmodules"}

func validateTarget(t Target) error {
	switch v := t.(type) {
	case InWorktree:
		if v.ID.Validate() != nil {
			return invalid("the worktree id is not valid")
		}
	case MainCheckout:
		if v.Repo.Validate() != nil {
			return invalid("the repo id is not valid")
		}
	default:
		return invalid("say where to act: a worktree or a repo")
	}
	return nil
}

func validatePaths(ps []RelPath) error {
	strs := make([]string, len(ps))
	for i, p := range ps {
		strs[i] = string(p)
	}
	_, err := checkPaths(strs)
	return err
}

func validateBranchRemote(branch, remote string) error {
	if err := CheckBranch(branch); err != nil {
		return err
	}
	return CheckRemoteName(remote)
}

// Validate implementations re-run the rules ParseAction applies, so an action
// built by hand (the structs are exported) cannot skip them.

func (a Commit) Validate() error {
	if err := validateTarget(a.Where); err != nil {
		return err
	}
	if err := validatePaths(a.Paths); err != nil {
		return err
	}
	if err := checkMessage(a.Message); err != nil {
		return err
	}
	if err := checkHexField("expect_head", a.ExpectHead, true); err != nil {
		return err
	}
	return checkHexField("expect_diff_hash", a.ExpectDiffHash, false)
}

func (a Push) Validate() error {
	if err := validateTarget(a.Where); err != nil {
		return err
	}
	if err := validateBranchRemote(a.Branch, a.Remote); err != nil {
		return err
	}
	return checkHexField("expect_head", a.ExpectHead, false)
}

func (a MergeRequest) Validate() error {
	if err := validateTarget(a.Where); err != nil {
		return err
	}
	if err := validateBranchRemote(a.Branch, a.Remote); err != nil {
		return err
	}
	if err := CheckBranch(a.TargetBranch); err != nil {
		return err
	}
	if err := checkTitle(a.Title); err != nil {
		return err
	}
	if err := checkDescription(a.Description); err != nil {
		return err
	}
	return checkHexField("expect_head", a.ExpectHead, false)
}

func (a AgentFile) Validate() error {
	if err := validateTarget(a.Where); err != nil {
		return err
	}
	if err := CheckRelPath(string(a.Path)); err != nil {
		return err
	}
	if !IsProtectedPath(a.Path) || lastSegIsProtectedDir(string(a.Path)) {
		return invalid("path %q is not an agent configuration file", string(a.Path))
	}
	if len(a.Content) > MaxContentSize {
		return invalid("the file content is over %d KiB", MaxContentSize>>10)
	}
	return checkHexField("expect_hash", a.ExpectHash, true)
}

func (a DeleteFiles) Validate() error {
	if err := validateTarget(a.Where); err != nil {
		return err
	}
	return validatePaths(a.Paths)
}

func (a Install) Validate() error {
	if a.Repo.Validate() != nil {
		return invalid("the repo id is not valid")
	}
	return checkStep(a.Step)
}
