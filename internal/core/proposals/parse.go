package proposals

import (
	"encoding/json"
	"errors"
	"fmt"
	"strings"

	"github.com/brendatamaar/yggdrasil/internal/core"
)

// supported reports whether a kind can be created in Phase 3. Publish
// (Phase 6) and subtasks (Phase 7) are refused so that no proposal that cannot
// run is ever created.
func supported(k core.ProposalKind) error {
	if k.Validate() != nil {
		return fmt.Errorf("%w: %q", ErrUnsupportedKind, string(k))
	}
	switch k {
	case core.ProposalCommit, core.ProposalPush, core.ProposalMergeRequest,
		core.ProposalAgentFile, core.ProposalDeleteFiles, core.ProposalInstall:
		return nil
	}
	return fmt.Errorf("%w: %q", ErrUnsupportedKind, string(k))
}

// The wire shapes of the arguments and of the stored payload. They are the
// same: ParseAction and ActionFromRow share every rule. Exactly one of
// Worktree and Repo names the target (Repo alone means the main checkout);
// install takes only Repo.
type (
	commitArgs struct {
		Worktree       string   `json:"worktree,omitempty"`
		Repo           string   `json:"repo,omitempty"`
		Paths          []string `json:"paths"`
		Message        string   `json:"message"`
		ExpectHead     string   `json:"expect_head,omitempty"`
		ExpectDiffHash string   `json:"expect_diff_hash"`
	}
	pushArgs struct {
		Worktree   string `json:"worktree,omitempty"`
		Repo       string `json:"repo,omitempty"`
		Branch     string `json:"branch"`
		Remote     string `json:"remote"`
		ExpectHead string `json:"expect_head"`
	}
	mrArgs struct {
		Worktree     string `json:"worktree,omitempty"`
		Repo         string `json:"repo,omitempty"`
		Branch       string `json:"branch"`
		Remote       string `json:"remote"`
		TargetBranch string `json:"target"`
		Title        string `json:"title"`
		Description  string `json:"description,omitempty"`
		ExpectHead   string `json:"expect_head"`
	}
	agentFileArgs struct {
		Worktree   string `json:"worktree,omitempty"`
		Repo       string `json:"repo,omitempty"`
		Path       string `json:"path"`
		Content    string `json:"content"`
		ExpectHash string `json:"expect_hash,omitempty"`
	}
	deleteArgs struct {
		Worktree string   `json:"worktree,omitempty"`
		Repo     string   `json:"repo,omitempty"`
		Paths    []string `json:"paths"`
	}
	installArgs struct {
		Repo string `json:"repo"`
		Step string `json:"step"`
	}
)

func decodeStrict(data []byte, v any) error {
	// Valid is true only for exactly one JSON value, so trailing data is refused.
	if !json.Valid(data) {
		return invalid("the arguments are not a single JSON object")
	}
	dec := json.NewDecoder(strings.NewReader(string(data)))
	dec.DisallowUnknownFields()
	if err := dec.Decode(v); err != nil {
		return invalid("the arguments are not what this proposal accepts (%v)", err)
	}
	return nil
}

func parseTarget(worktree, repo string) (Target, error) {
	switch {
	case worktree != "" && repo != "":
		return nil, invalid("give either a worktree or a repo, not both")
	case worktree != "":
		id := core.WorktreeID(worktree)
		if err := id.Validate(); err != nil {
			return nil, invalid("the worktree id is not valid")
		}
		return InWorktree{ID: id}, nil
	case repo != "":
		id := core.RepoID(repo)
		if err := id.Validate(); err != nil {
			return nil, invalid("the repo id is not valid")
		}
		return MainCheckout{Repo: id}, nil
	}
	return nil, invalid("say where to act: a worktree or a repo")
}

func wrapKind(kind string, err error) error {
	if err == nil || errors.Is(err, ErrUnsupportedKind) {
		return err
	}
	return fmt.Errorf("%s proposal: %w", kind, err)
}

// ParseAction turns an MCP tool's arguments into an Action. It rejects an
// unknown kind or field, publish and subtasks, an absolute path, "..", a
// drive, a stream, a device name, an empty path, more than 200 paths, a
// message that is empty or over 4 KiB, a title over 200, a description with a
// newline, content over 256 KiB, an agent-file path that is not protected, a
// branch failing the git ref rules and a remote not matching
// [A-Za-z0-9._-]+.
func ParseAction(kind string, data []byte) (Action, error) {
	k := core.ProposalKind(kind)
	if err := supported(k); err != nil {
		return nil, err
	}
	a, err := parseKind(k, data)
	return a, wrapKind(kind, err)
}

func parseKind(k core.ProposalKind, data []byte) (Action, error) {
	switch k {
	case core.ProposalCommit:
		var v commitArgs
		if err := decodeStrict(data, &v); err != nil {
			return nil, err
		}
		return buildCommit(v)
	case core.ProposalPush:
		var v pushArgs
		if err := decodeStrict(data, &v); err != nil {
			return nil, err
		}
		return buildPush(v)
	case core.ProposalMergeRequest:
		var v mrArgs
		if err := decodeStrict(data, &v); err != nil {
			return nil, err
		}
		return buildMR(v)
	case core.ProposalAgentFile:
		var v agentFileArgs
		if err := decodeStrict(data, &v); err != nil {
			return nil, err
		}
		return buildAgentFile(v)
	case core.ProposalDeleteFiles:
		var v deleteArgs
		if err := decodeStrict(data, &v); err != nil {
			return nil, err
		}
		return buildDelete(v)
	case core.ProposalInstall:
		var v installArgs
		if err := decodeStrict(data, &v); err != nil {
			return nil, err
		}
		return buildInstall(v)
	}
	return nil, fmt.Errorf("%w: %q", ErrUnsupportedKind, string(k))
}

func buildCommit(v commitArgs) (Action, error) {
	t, err := parseTarget(v.Worktree, v.Repo)
	if err != nil {
		return nil, err
	}
	ps, err := checkPaths(v.Paths)
	if err != nil {
		return nil, err
	}
	if err := checkMessage(v.Message); err != nil {
		return nil, err
	}
	if err := checkHexField("expect_head", v.ExpectHead, true); err != nil {
		return nil, err
	}
	if err := checkHexField("expect_diff_hash", v.ExpectDiffHash, false); err != nil {
		return nil, err
	}
	return Commit{Where: t, Paths: ps, Message: v.Message, ExpectHead: v.ExpectHead, ExpectDiffHash: v.ExpectDiffHash}, nil
}

func buildPush(v pushArgs) (Action, error) {
	t, err := parseTarget(v.Worktree, v.Repo)
	if err != nil {
		return nil, err
	}
	if err := checkBranchRemote(v.Branch, v.Remote); err != nil {
		return nil, err
	}
	if err := checkHexField("expect_head", v.ExpectHead, false); err != nil {
		return nil, err
	}
	return Push{Where: t, Branch: v.Branch, Remote: v.Remote, ExpectHead: v.ExpectHead}, nil
}

func checkBranchRemote(branch, remote string) error {
	if err := CheckBranch(branch); err != nil {
		return err
	}
	return CheckRemoteName(remote)
}

func buildMR(v mrArgs) (Action, error) {
	t, err := parseTarget(v.Worktree, v.Repo)
	if err != nil {
		return nil, err
	}
	if err := checkBranchRemote(v.Branch, v.Remote); err != nil {
		return nil, err
	}
	if err := CheckBranch(v.TargetBranch); err != nil {
		return nil, err
	}
	if err := checkTitle(v.Title); err != nil {
		return nil, err
	}
	if err := checkDescription(v.Description); err != nil {
		return nil, err
	}
	if err := checkHexField("expect_head", v.ExpectHead, false); err != nil {
		return nil, err
	}
	return MergeRequest{
		Where: t, Branch: v.Branch, Remote: v.Remote, TargetBranch: v.TargetBranch,
		Title: v.Title, Description: v.Description, ExpectHead: v.ExpectHead,
	}, nil
}

func buildAgentFile(v agentFileArgs) (Action, error) {
	t, err := parseTarget(v.Worktree, v.Repo)
	if err != nil {
		return nil, err
	}
	if err := CheckRelPath(v.Path); err != nil {
		return nil, err
	}
	if !IsProtectedPath(RelPath(v.Path)) {
		return nil, invalid("path %q is not an agent configuration file (.claude/**, .mcp.json, CLAUDE.md, AGENTS.md, opencode.json, .opencode/**, GEMINI.md)", v.Path)
	}
	if len(v.Content) > MaxContentSize {
		return nil, invalid("the file content is over %d KiB", MaxContentSize>>10)
	}
	if err := checkHexField("expect_hash", v.ExpectHash, true); err != nil {
		return nil, err
	}
	return AgentFile{Where: t, Path: RelPath(v.Path), Content: v.Content, ExpectHash: v.ExpectHash}, nil
}

func buildDelete(v deleteArgs) (Action, error) {
	t, err := parseTarget(v.Worktree, v.Repo)
	if err != nil {
		return nil, err
	}
	ps, err := checkPaths(v.Paths)
	if err != nil {
		return nil, err
	}
	return DeleteFiles{Where: t, Paths: ps}, nil
}

func buildInstall(v installArgs) (Action, error) {
	id := core.RepoID(v.Repo)
	if err := id.Validate(); err != nil {
		return nil, invalid("the repo id is not valid")
	}
	if err := checkStep(v.Step); err != nil {
		return nil, err
	}
	return Install{Repo: id, Step: v.Step}, nil
}

// ActionFromRow rebuilds an Action from the stored kind and payload, with the
// same rules as ParseAction. A payload whose shape does not match the kind is
// rejected like any unknown field.
func ActionFromRow(kind core.ProposalKind, payload []byte) (Action, error) {
	if err := supported(kind); err != nil {
		return nil, err
	}
	a, err := parseKind(kind, payload)
	if err != nil {
		return nil, fmt.Errorf("read %s proposal: %w", kind, err)
	}
	return a, nil
}

func targetArgs(t Target) (worktree, repo string) {
	switch v := t.(type) {
	case InWorktree:
		return string(v.ID), ""
	case MainCheckout:
		return "", string(v.Repo)
	}
	return "", ""
}

func pathStrings(ps []RelPath) []string {
	out := make([]string, len(ps))
	for i, p := range ps {
		out[i] = string(p)
	}
	return out
}

// RowOf is the inverse of ActionFromRow: ActionFromRow(RowOf(a)) equals a.
func RowOf(a Action) (core.ProposalKind, []byte) {
	var v any
	switch x := a.(type) {
	case Commit:
		w, r := targetArgs(x.Where)
		v = commitArgs{w, r, pathStrings(x.Paths), x.Message, x.ExpectHead, x.ExpectDiffHash}
	case Push:
		w, r := targetArgs(x.Where)
		v = pushArgs{w, r, x.Branch, x.Remote, x.ExpectHead}
	case MergeRequest:
		w, r := targetArgs(x.Where)
		v = mrArgs{w, r, x.Branch, x.Remote, x.TargetBranch, x.Title, x.Description, x.ExpectHead}
	case AgentFile:
		w, r := targetArgs(x.Where)
		v = agentFileArgs{w, r, string(x.Path), x.Content, x.ExpectHash}
	case DeleteFiles:
		w, r := targetArgs(x.Where)
		v = deleteArgs{w, r, pathStrings(x.Paths)}
	case Install:
		v = installArgs{string(x.Repo), x.Step}
	default:
		return "", nil
	}
	b, err := json.Marshal(v)
	if err != nil { // plain strings and slices cannot fail to marshal
		return a.Kind(), nil
	}
	return a.Kind(), b
}
