package mcp_test

import (
	"go/ast"
	"go/parser"
	"go/token"
	"regexp"
	"strconv"
	"strings"
	"testing"

	"github.com/brendatamaar/yggdrasil/internal/core"
	coreactions "github.com/brendatamaar/yggdrasil/internal/core/actions"
	cmcp "github.com/brendatamaar/yggdrasil/internal/core/mcp"
)

// instructionTexts reads the fixed task text of every action kind from
// internal/actions/prompt.go (the `instruction` function), so this test sees
// what the agent is really told.
func instructionTexts(t *testing.T) map[coreactions.Kind]string {
	t.Helper()
	fset := token.NewFileSet()
	f, err := parser.ParseFile(fset, "../actions/prompt.go", nil, 0)
	if err != nil {
		t.Fatal(err)
	}
	out := map[coreactions.Kind]string{}
	for _, d := range f.Decls {
		fn, ok := d.(*ast.FuncDecl)
		if !ok || fn.Name.Name != "instruction" {
			continue
		}
		ast.Inspect(fn.Body, func(n ast.Node) bool {
			cc, ok := n.(*ast.CaseClause)
			if !ok || len(cc.Body) != 1 {
				return true
			}
			ret, ok := cc.Body[0].(*ast.ReturnStmt)
			if !ok || len(ret.Results) != 1 {
				return true
			}
			lit, ok := ret.Results[0].(*ast.BasicLit)
			if !ok || lit.Kind != token.STRING {
				return true
			}
			text, _ := strconv.Unquote(lit.Value)
			for _, e := range cc.List {
				sel, ok := e.(*ast.SelectorExpr)
				if !ok {
					continue
				}
				for _, k := range coreactions.Kinds() {
					if strings.EqualFold(strings.ReplaceAll(string(k), "_", ""), sel.Sel.Name) {
						out[k] = text
					}
				}
			}
			return true
		})
	}
	if len(out) == 0 {
		t.Fatal("found no instruction text in ../actions/prompt.go")
	}
	return out
}

// The tools each instruction asks the agent to use, by the words it uses.
// Add a row when an instruction starts to ask for another proposal: the test
// below fails for an instruction that talks about proposing but has no row, so
// a promise the agent cannot keep (AC-03) cannot be added silently.
var instructionPromises = []struct {
	phrase string
	tool   cmcp.Tool
}{
	{"propose one commit", cmcp.ToolProposeCommit},
	{"as a proposal for the user to confirm", cmcp.ToolProposeReviewComment},
	{"Propose the exact lines", cmcp.ToolProposeIgnore},
}

var toolNameRe = regexp.MustCompile(`propose_[a-z_]+`)

// Every tool an action's instruction names is authorized in the mode and the
// scope the action runs in: an Ask task on one repo, with no worktree.
func TestActionInstructionsOnlyAskForAuthorizedTools(t *testing.T) {
	const repo core.RepoID = "r1"
	known := map[string]bool{}
	for _, tool := range cmcp.AllTools() {
		known[string(tool)] = true
	}
	for kind, text := range instructionTexts(t) {
		mode := kind.Mode()
		if mode == "" {
			continue
		}
		var tools []cmcp.Tool
		for _, p := range instructionPromises {
			if strings.Contains(text, p.phrase) {
				tools = append(tools, p.tool)
			}
		}
		for _, name := range toolNameRe.FindAllString(text, -1) {
			if !known[name] {
				t.Errorf("%s: the instruction names %q, which is not an MCP tool", kind, name)
				continue
			}
			tools = append(tools, cmcp.Tool(name))
		}
		if len(tools) == 0 && (strings.Contains(strings.ToLower(text), "propos")) {
			t.Errorf("%s: the instruction asks for proposals but names no tool in instructionPromises: %q", kind, text)
		}
		ctx := cmcp.Context{Task: "t", Live: true, Mode: mode, Scope: []core.RepoID{repo}, Project: []core.RepoID{repo},
			HasWorktree: map[core.RepoID]bool{}}
		if mode == core.ModeCode {
			ctx.HasWorktree[repo] = true // a Code task works in its own worktree
		}
		for _, tool := range tools {
			if d := cmcp.Authorize(ctx, tool, repo); !d.Allowed {
				t.Errorf("%s runs in %s mode but its instruction asks for %s, which is denied: %+v", kind, mode, tool, d)
			}
			if d := cmcp.Authorize(ctx, tool); !d.Allowed {
				t.Errorf("%s: %s is not offered to the task: %+v", kind, tool, d)
			}
		}
	}
}
