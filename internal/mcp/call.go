package mcp

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"regexp"
	"strings"
	"unicode/utf8"

	"github.com/modelcontextprotocol/go-sdk/jsonrpc"
	sdk "github.com/modelcontextprotocol/go-sdk/mcp"

	"github.com/brendatamaar/yggdrasil/internal/apperr"
	"github.com/brendatamaar/yggdrasil/internal/core"
	cmcp "github.com/brendatamaar/yggdrasil/internal/core/mcp"
)

// userErr is a refusal the agent should read: plain language, no internals.
type userErr string

func (e userErr) Error() string { return string(e) }

const (
	msgNoRepo    = "No such repo. Use list_repos to see the repos you can use."
	msgNoSession = "This session has ended."
	msgSlowDown  = "Too many calls; wait a moment."
)

// result is what a tool produced.
type result struct {
	text      string
	truncated bool
}

// reqCtx is everything one tool call knows.
type reqCtx struct {
	s    *Server
	act  Active
	snap Snapshot
	cctx cmcp.Context
	id   string
}

// call runs one tools/call: limiter, parse, authorize, run, sanitize.
func (s *Server) call(ctx context.Context, act Active, name string, args json.RawMessage) (res *sdk.CallToolResult, err error) {
	id := newCallID()
	defer func() {
		if v := recover(); v != nil {
			s.cfg.Logger.Error("mcp tool panic", "call", id, "tool", name, "panic", s.sanitize(fmt.Sprint(v)))
			res, err = toolError(genericFailure(id)), nil
		}
	}()
	g := s.gateFor(act.Task)
	if g == nil {
		return toolError(msgNoSession), nil
	}
	if a := g.allow(s.cfg.Clock()); !a.Allowed {
		return toolError(msgSlowDown), nil
	}
	select {
	case g.sem <- struct{}{}:
		defer func() { <-g.sem }()
	case <-ctx.Done():
		return nil, ctx.Err()
	}

	c, perr := cmcp.ParseCall(name, args)
	if perr != nil {
		return nil, &jsonrpc.Error{Code: jsonrpc.CodeInvalidParams, Message: s.cfg.Redactor.Redact(perr.Error())}
	}
	snap, serr := s.cfg.Workspace.Snapshot(ctx, act.Task)
	if serr != nil {
		if errors.Is(serr, ErrNoTask) {
			return toolError(msgNoSession), nil
		}
		s.cfg.Logger.Error("mcp snapshot failed", "call", id, "err", s.sanitize(serr.Error()))
		return toolError(genericFailure(id)), nil
	}
	rc := &reqCtx{s: s, act: act, snap: snap, cctx: s.coreContext(act, snap), id: id}
	if d := cmcp.Authorize(rc.cctx, c.Tool()); !d.Allowed {
		return toolError(denyMessage(d.Reason)), nil
	}
	out, rerr := rc.run(ctx, c)
	if !s.sessions.Live(act.Task) {
		// The run ended while the call was in flight: drop the result.
		return toolError(msgNoSession), nil
	}
	if rerr != nil {
		var ue userErr
		if errors.As(rerr, &ue) {
			return toolError(rc.sanitize(string(ue))), nil
		}
		if m, ok := userMessage(rerr); ok {
			return toolError(rc.sanitize(m)), nil
		}
		s.cfg.Logger.Error("mcp tool failed", "call", id, "tool", name, "task", act.Task, "err", rc.sanitize(rerr.Error()))
		return toolError(genericFailure(id)), nil
	}
	text, cut := rc.finish(out.text)
	cr := &sdk.CallToolResult{Content: []sdk.Content{&sdk.TextContent{Text: text}}}
	if cut || out.truncated {
		cr.Meta = sdk.Meta{"truncated": true}
	}
	return cr, nil
}

func toolError(msg string) *sdk.CallToolResult {
	return &sdk.CallToolResult{IsError: true, Content: []sdk.Content{&sdk.TextContent{Text: msg}}}
}

func denyMessage(r cmcp.DenyReason) string {
	switch r {
	case cmcp.DenyNoSession:
		return msgNoSession
	case cmcp.DenyNoWorktree:
		return "This task has no worktree for that repo, so there is nothing to commit or push from. Ask the user to start a task that can edit it."
	case cmcp.DenyNoSuchRepo:
		return msgNoRepo
	case cmcp.DenyAskMode:
		return "That tool changes files, so it is not available in Ask mode."
	}
	return "That tool is not available."
}

func userMessage(err error) (string, bool) {
	if m, ok := apperr.MessageOf(err); ok {
		return m, true
	}
	var um userMessager
	if errors.As(err, &um) {
		return um.UserMessage(), true
	}
	return "", false
}

// Absolute paths of the machine never leave the process (M7): Windows drive
// and UNC paths, and rooted Unix paths under the usual system folders.
var (
	// A drive or UNC path. After a drive letter the path may hold spaces
	// (C:\Program Files\x): it runs to the end of the line, or to a colon,
	// quote or shell character.
	reWinPath = regexp.MustCompile(`(?i)(?:\\\\\?\\)?(?:[a-z]:\\|\b[a-z]:/)[^\r\n"'<>|*?:]*|\\\\[A-Za-z0-9_.$-]+\\[^\s"'<>|*?]*`)
	// Any rooted /seg/seg token, whatever comes before it ([, comma, file://,
	// backtick, ...). Only a slash that follows a word character, dot or dash is
	// left alone: a relative path or a git diff fragment (a/foo/bar).
	reUnixPath = regexp.MustCompile(`(^|[^\w.\-])/[^\s/"')]+/[^\s"')]*`)
)

// sanitize redacts secrets and replaces absolute paths. extra are folders to
// scrub whatever they look like (the visible repos' paths); the data folder is
// always scrubbed. Matching ignores case and treats / and \ alike.
func (s *Server) sanitize(text string, extra ...string) string {
	text = s.cfg.Redactor.Redact(text)
	for _, p := range append([]string{s.cfg.DataDir}, extra...) {
		if re := folderRegexp(p); re != nil {
			text = re.ReplaceAllString(text, "<path>")
		}
	}
	text = reWinPath.ReplaceAllString(text, "<path>")
	return reUnixPath.ReplaceAllString(text, "${1}<path>")
}

// folderRegexp matches a folder path and whatever follows it in the same token.
func folderRegexp(p string) *regexp.Regexp {
	p = strings.TrimRight(p, `\/`)
	parts := strings.FieldsFunc(p, func(r rune) bool { return r == '/' || r == '\\' })
	if len(p) < 2 || len(parts) == 0 {
		return nil
	}
	for i, x := range parts {
		parts[i] = regexp.QuoteMeta(x)
	}
	lead := ""
	if strings.HasPrefix(p, "/") || strings.HasPrefix(p, `\`) {
		lead = `[\\/]+`
	}
	re, err := regexp.Compile(`(?i)` + lead + strings.Join(parts, `[\\/]+`) + `[^\s"'<>|*?]*`)
	if err != nil {
		return nil
	}
	return re
}

// sanitize scrubs text with the paths of this call's visible repos too.
func (rc *reqCtx) sanitize(text string) string {
	var paths []string
	for _, r := range rc.snap.Repos {
		paths = append(paths, r.Paths...)
	}
	return rc.s.sanitize(text, paths...)
}

// finish sanitizes and caps a result at MaxResultBytes on a rune boundary.
func (rc *reqCtx) finish(text string) (string, bool) {
	text = rc.sanitize(text)
	if len(text) <= cmcp.MaxResultBytes {
		return text, false
	}
	cut := cmcp.MaxResultBytes
	for cut > 0 && !utf8.RuneStart(text[cut]) {
		cut--
	}
	return text[:cut] + "\n[truncated]", true
}

// msgAmbiguous: two visible repos answer to the same reference.
const msgAmbiguous = "More than one repo has that name. Use the repo's id from list_repos."

// resolve finds a repo among the visible ones by ID, then by exact name, then
// by name ignoring case. Anything else, whether untracked, unknown or out of
// scope, is not found. When more than one repo matches at the first level that
// matches, ambiguous is true and no repo is returned: never pick one silently.
func (rc *reqCtx) resolve(ref cmcp.RepoRef) (r RepoView, found, ambiguous bool) {
	levels := []func(RepoView) bool{
		func(v RepoView) bool { return string(v.ID) == string(ref) },
		func(v RepoView) bool { return v.Name == string(ref) },
		func(v RepoView) bool { return strings.EqualFold(v.Name, string(ref)) },
	}
	for _, match := range levels {
		var hits []RepoView
		for _, v := range rc.snap.Repos {
			if match(v) {
				hits = append(hits, v)
			}
		}
		switch len(hits) {
		case 0:
			continue
		case 1:
			return hits[0], true, false
		}
		return RepoView{}, false, true
	}
	return RepoView{}, false, false
}

// repo resolves ref and authorizes tool for it. The refusal is the same for an
// untracked, an unknown and an out-of-scope repo (M3).
func (rc *reqCtx) repo(tool cmcp.Tool, ref cmcp.RepoRef) (RepoView, error) {
	r, _, ambiguous := rc.resolve(ref)
	if ambiguous {
		return RepoView{}, userErr(msgAmbiguous)
	}
	d := cmcp.Authorize(rc.cctx, tool, r.ID)
	if !d.Allowed {
		return RepoView{}, userErr(denyMessage(d.Reason))
	}
	return r, nil
}

func (r RepoView) loc() Loc { return Loc{Repo: r.ID, Worktree: r.Worktree} }

// visible says whether id is a visible repo, and its name.
func (rc *reqCtx) name(id core.RepoID) (string, bool) {
	for _, r := range rc.snap.Repos {
		if r.ID == id {
			return r.Name, true
		}
	}
	return "", false
}

// audit records a state-changing call before it runs; if the entry cannot be
// written the call does not run (invariant 8).
//
// Every audit-and-effect step goes through here, so it is also where the
// session is checked once more: a run that was revoked since Authorize gets
// neither an audit entry nor an effect.
func (rc *reqCtx) audit(ctx context.Context, tool cmcp.Tool, subject string, detail map[string]any) error {
	if !rc.s.sessions.Live(rc.act.Task) {
		return userErr(msgNoSession)
	}
	if err := rc.s.cfg.Audit.Record(ctx, AuditRecord{Task: rc.act.Task, Tool: tool, Subject: subject, Detail: detail}); err != nil {
		return fmt.Errorf("audit %s: %w", tool, err)
	}
	return nil
}

func (rc *reqCtx) run(ctx context.Context, c cmcp.Call) (result, error) {
	switch v := c.(type) {
	case cmcp.ListRepos, cmcp.RepoStatus, cmcp.RemoteStatus, cmcp.RecentCommits, cmcp.ChangedFiles, cmcp.GetDiff:
		return rc.runRead(ctx, v)
	case cmcp.BuildInfo, cmcp.BuildOrder, cmcp.ListBuildPresets, cmcp.ListBuilds, cmcp.BuildLog,
		cmcp.StartBuild, cmcp.RunBuildPreset, cmcp.ListDevServers, cmcp.DevServerLog:
		return rc.runBuild(ctx, v)
	case cmcp.Fetch, cmcp.Pull:
		return rc.runGit(ctx, v)
	}
	return rc.runPropose(ctx, c)
}
