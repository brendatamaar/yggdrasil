package terminaltest_test

import (
	"testing"

	"github.com/brendatamaar/yggdrasil/internal/terminal"
	tt "github.com/brendatamaar/yggdrasil/internal/terminal/terminaltest"
)

func TestFakePTYContract(t *testing.T) {
	s := &tt.Starter{}
	tt.PTYContract(t, s.Start, tt.PTYProgram{Argv: []string{"fake"}, Dir: t.TempDir(), Env: []string{"A=b"}, ExitInput: "exit 3\r", ExitCode: 3})
}

func TestCounterTokensContract(t *testing.T) {
	tt.TokenSourceContract(t, func() terminal.TokenSource { return &tt.CounterTokens{} }, 10000)
}

func TestMemSocketContract(t *testing.T) {
	tt.SocketContract(t, func() tt.SocketPair {
		m := tt.NewMemSocket()
		return tt.SocketPair{Server: m, Send: m.Send, Disconnect: m.Disconnect, Output: m.Output, Closed: m.Closed}
	})
}

func TestFakeFoldersContract(t *testing.T) {
	tt.FoldersContract(t, func() tt.FoldersFixture {
		f := tt.NewFakeFolders()
		f.AddRepo("repo_a", "/work/a")
		f.AddWorktree("wt_a", "repo_a", "/work/a-wt")
		f.AddRepo("repo_b", "/work/b")
		f.AddWorktree("wt_b", "repo_b", "/work/b-wt")
		return tt.FoldersFixture{Folders: f, RemoveRepoA: func() { f.RemoveRepo("repo_a") }}
	})
}

func TestMemAuditContract(t *testing.T) {
	tt.AuditContract(t, func() (terminal.AuditSink, func() []terminal.AuditEntry) {
		m := &tt.MemAudit{}
		return m, m.Entries
	})
}
