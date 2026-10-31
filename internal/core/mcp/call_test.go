package mcp

import (
	"reflect"
	"strings"
	"testing"
)

// samples has one valid call per tool.
func samples() []Call {
	return []Call{
		ListRepos{},
		RepoStatus{Repo: "shop-api"},
		RemoteStatus{Repo: "shop-api"},
		RecentCommits{Repo: "shop-api", Limit: 5},
		ChangedFiles{Repo: "shop-api"},
		GetDiff{Repo: "shop-api", Path: "src/main.go", MaxBytes: 1000},
		GetDiff{Repo: "shop-api", MaxBytes: MaxDiffBytes},
		BuildInfo{Repo: "shop-web"},
		BuildOrder{},
		ListBuildPresets{},
		ListBuilds{Limit: 10},
		BuildLog{Build: "b1", Step: 2, MaxBytes: 500},
		StartBuild{Repos: []RepoRef{"shop-api", "shop-web"}, Steps: []string{"verify", "build"}},
		StartBuild{Repos: []RepoRef{"shop-api"}},
		RunBuildPreset{Preset: "all"},
		ListDevServers{},
		DevServerLog{Name: "web"},
		Fetch{Repo: "shop-api"},
		Pull{Repo: "shop-api"},
		ProposeCommit{Repo: "shop-api", Paths: []string{"a.go", "dir/b.go"}, Message: "fix it", Summary: "s"},
		ProposePush{Repo: "shop-api", Branch: "feat/x", Remote: "origin"},
		ProposeMergeRequest{Repo: "shop-api", Branch: "feat/x", Remote: "origin", Target: "main", Title: "T", Description: "d"},
		ProposeInstall{Repo: "shop-web"},
		ProposeAgentFile{Repo: "shop-api", Path: "CLAUDE.md", Content: "# hi\n"},
		ProposeDeleteFiles{Repo: "shop-api", Paths: []string{"old.txt"}},
	}
}

func TestSamplesCoverEveryTool(t *testing.T) {
	seen := map[Tool]bool{}
	for _, c := range samples() {
		seen[c.Tool()] = true
	}
	for _, tl := range AllTools() {
		if !seen[tl] {
			t.Errorf("no sample for %s", tl)
		}
	}
	if len(AllTools()) != 23 {
		t.Fatalf("tool set changed: %d", len(AllTools()))
	}
}

func TestCallRoundTrip(t *testing.T) {
	for _, c := range samples() {
		name, args := FormatCall(c)
		got, err := ParseCall(name, args)
		if err != nil {
			t.Fatalf("%s: %v (args %s)", name, err, args)
		}
		if !reflect.DeepEqual(got, c) {
			t.Errorf("%s: round trip gave %#v, want %#v", name, got, c)
		}
	}
}

func TestParsedCallsAreValid(t *testing.T) {
	for _, c := range samples() {
		name, args := FormatCall(c)
		got, err := ParseCall(name, args)
		if err != nil {
			t.Fatal(err)
		}
		if err := CheckCall(got); err != nil {
			t.Errorf("%s accepted but invalid: %v", name, err)
		}
		if got.Tool() != Tool(name) {
			t.Errorf("tool mismatch %s", name)
		}
	}
}

func TestParseDefaults(t *testing.T) {
	c, err := ParseCall("recent_commits", []byte(`{"repo":"r"}`))
	if err != nil || c.(RecentCommits).Limit != DefaultListLimit {
		t.Fatalf("limit default: %v %#v", err, c)
	}
	c, err = ParseCall("get_diff", []byte(`{"repo":"r"}`))
	if err != nil || c.(GetDiff).MaxBytes != MaxDiffBytes {
		t.Fatalf("diff default: %v %#v", err, c)
	}
	c, err = ParseCall("build_log", []byte(`{"build":"b"}`))
	if err != nil || c.(BuildLog).MaxBytes != MaxLogBytes {
		t.Fatalf("log default: %v %#v", err, c)
	}
	for _, in := range []string{"", "null", "{}", " {} "} {
		if _, err := ParseCall("list_repos", []byte(in)); err != nil {
			t.Errorf("list_repos with %q: %v", in, err)
		}
	}
}

func many(n int, f func(int) string) []string {
	out := make([]string, n)
	for i := range out {
		out[i] = f(i)
	}
	return out
}

func TestParseRejects(t *testing.T) {
	longName := strings.Repeat("a", maxNameLen+1)
	tooManyPaths := `["` + strings.Join(many(MaxPaths+1, func(i int) string { return "f" + string(rune('a'+i%26)) + strings.Repeat("x", i/26+1) }), `","`) + `"]`
	tooManySteps := `["` + strings.Join(many(MaxBuildSteps+1, func(int) string { return "verify" }), `","`) + `"]`
	cases := []struct{ name, tool, args, want string }{
		{"unknown tool", "delete_everything", `{}`, "not a tool"},
		{"confirm tool", "confirm_proposal", `{}`, "not a tool"},
		{"unknown field", "repo_status", `{"repo":"r","x":1}`, "not what this tool accepts"},
		{"unknown field no-arg", "list_repos", `{"x":1}`, "not what this tool accepts"},
		{"wrong type", "repo_status", `{"repo":5}`, "not what this tool accepts"},
		{"not json", "repo_status", `nope`, "not what this tool accepts"},
		{"trailing data", "repo_status", `{"repo":"r"} {"repo":"s"}`, "extra data"},
		{"trailing garbage", "repo_status", `{"repo":"r"}x`, "extra data"},
		{"missing repo", "repo_status", `{}`, "repo is required"},
		{"repo path", "repo_status", `{"repo":"../x"}`, "name, not a path"},
		{"repo slash", "repo_status", `{"repo":"a/b"}`, "name, not a path"},
		{"repo drive", "repo_status", `{"repo":"C:"}`, "name, not a path"},
		{"repo dotdot", "repo_status", `{"repo":".."}`, "name, not a path"},
		{"repo control", "repo_status", `{"repo":"a\nb"}`, "not allowed"},
		{"repo long", "repo_status", `{"repo":"` + longName + `"}`, "over"},
		{"limit zero", "recent_commits", `{"repo":"r","limit":0}`, "limit must be"},
		{"limit negative", "list_builds", `{"limit":-1}`, "limit must be"},
		{"limit over", "list_builds", `{"limit":201}`, "limit must be"},
		{"max_bytes over cap", "get_diff", `{"repo":"r","max_bytes":65537}`, "max_bytes must be"},
		{"max_bytes zero", "get_diff", `{"repo":"r","max_bytes":0}`, "max_bytes must be"},
		{"log max_bytes over cap", "build_log", `{"build":"b","max_bytes":65537}`, "max_bytes must be"},
		{"path absolute", "get_diff", `{"repo":"r","path":"/etc/passwd"}`, "relative"},
		{"path dotdot", "get_diff", `{"repo":"r","path":"a/../../b"}`, "inside the repo"},
		{"path drive", "get_diff", `{"repo":"r","path":"C:/x"}`, "relative"},
		{"path unc", "get_diff", `{"repo":"r","path":"\\\\host\\share\\x"}`, "backslash"},
		{"path backslash", "get_diff", `{"repo":"r","path":"a\\b"}`, "backslash"},
		{"path stream", "get_diff", `{"repo":"r","path":"a.txt:s"}`, "relative"},
		{"path git", "get_diff", `{"repo":"r","path":".GIT/config"}`, ".git"},
		{"path device", "get_diff", `{"repo":"r","path":"nul.txt"}`, "device"},
		{"path empty part", "get_diff", `{"repo":"r","path":"a//b"}`, "empty part"},
		{"path trailing dot", "get_diff", `{"repo":"r","path":"a./b"}`, "dot or space"},
		{"path short name", "get_diff", `{"repo":"r","path":"PROGRA~1/x"}`, "short name"},
		{"build id bad", "build_log", `{"build":"a b"}`, "build id"},
		{"build step negative", "build_log", `{"build":"b","step":-1}`, "step must be"},
		{"build step big", "build_log", `{"build":"b","step":50}`, "step must be"},
		{"no repos", "start_build", `{"repos":[]}`, "between 1 and"},
		{"too many steps", "start_build", `{"repos":["r"],"steps":` + tooManySteps + `}`, "at most"},
		{"bad step name", "start_build", `{"repos":["r"],"steps":["a b"]}`, "build step"},
		{"bad repo in list", "start_build", `{"repos":["r","../x"]}`, "name, not a path"},
		{"preset path", "run_build_preset", `{"preset":"a/b"}`, "not a path"},
		{"dev server missing", "dev_server_log", `{}`, "required"},
		{"commit no paths", "propose_commit", `{"repo":"r","message":"m"}`, "no paths"},
		{"commit too many paths", "propose_commit", `{"repo":"r","message":"m","paths":` + tooManyPaths + `}`, "at most"},
		{"commit dup path", "propose_commit", `{"repo":"r","message":"m","paths":["A","a"]}`, "twice"},
		{"commit abs path", "propose_commit", `{"repo":"r","message":"m","paths":["/x"]}`, "relative"},
		{"commit empty message", "propose_commit", `{"repo":"r","message":"  ","paths":["a"]}`, "empty"},
		{"commit long message", "propose_commit", `{"repo":"r","message":"` + strings.Repeat("m", MaxMessageBytes+1) + `","paths":["a"]}`, "over"},
		{"commit has worktree field", "propose_commit", `{"repo":"r","message":"m","paths":["a"],"worktree":"w"}`, "not what this tool accepts"},
		{"summary multi-line", "propose_install", `{"repo":"r","summary":"a\nb"}`, "one line"},
		{"summary long", "propose_install", `{"repo":"r","summary":"` + strings.Repeat("s", MaxSummaryLen+1) + `"}`, "over"},
		{"push bad branch", "propose_push", `{"repo":"r","branch":"-x","remote":"origin"}`, "branch"},
		{"push dotdot branch", "propose_push", `{"repo":"r","branch":"a..b","remote":"origin"}`, "branch"},
		{"push bad remote", "propose_push", `{"repo":"r","branch":"x","remote":"o rigin"}`, "remote"},
		{"push dash remote", "propose_push", `{"repo":"r","branch":"x","remote":"-o"}`, "remote"},
		{"mr empty title", "propose_merge_request", `{"repo":"r","branch":"x","remote":"o","target":"main","title":""}`, "title"},
		{"mr long title", "propose_merge_request", `{"repo":"r","branch":"x","remote":"o","target":"main","title":"` + strings.Repeat("t", MaxTitleLen+1) + `"}`, "title"},
		{"mr description newline", "propose_merge_request", `{"repo":"r","branch":"x","remote":"o","target":"main","title":"t","description":"a\nb"}`, "one line"},
		{"mr bad target", "propose_merge_request", `{"repo":"r","branch":"x","remote":"o","target":"a b","title":"t"}`, "branch"},
		{"agent file abs", "propose_agent_file", `{"repo":"r","path":"/CLAUDE.md","content":"x"}`, "relative"},
		{"agent file dotdot", "propose_agent_file", `{"repo":"r","path":"../CLAUDE.md","content":"x"}`, "inside the repo"},
		{"agent file big", "propose_agent_file", `{"repo":"r","path":"CLAUDE.md","content":"` + strings.Repeat("c", MaxContentBytes+1) + `"}`, "over"},
		{"delete no paths", "propose_delete_files", `{"repo":"r","paths":[]}`, "no paths"},
		{"delete dotdot", "propose_delete_files", `{"repo":"r","paths":["../x"]}`, "inside the repo"},
		{"delete drive", "propose_delete_files", `{"repo":"r","paths":["D:\\x"]}`, "backslash"},
		{"over request cap", "repo_status", `{"repo":"r","pad":"` + strings.Repeat("p", MaxRequestBytes) + `"}`, "over"},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			c, err := ParseCall(tc.tool, []byte(tc.args))
			if err == nil {
				t.Fatalf("accepted %#v", c)
			}
			if c != nil {
				t.Errorf("returned a call with an error: %#v", c)
			}
			if !strings.Contains(err.Error(), tc.want) {
				t.Errorf("error %q does not mention %q", err, tc.want)
			}
		})
	}
}

func TestParseAcceptsBoundaryValues(t *testing.T) {
	steps := `["` + strings.Join(many(MaxBuildSteps, func(int) string { return "verify" }), `","`) + `"]`
	ok := []struct{ tool, args string }{
		{"start_build", `{"repos":["r"],"steps":` + steps + `}`},
		{"get_diff", `{"repo":"r","max_bytes":65536}`},
		{"list_builds", `{"limit":200}`},
		{"get_diff", `{"repo":"r","path":"a/b c/d.txt"}`},
		{"propose_commit", `{"repo":"r","message":"` + strings.Repeat("m", MaxMessageBytes) + `","paths":["a"]}`},
	}
	for _, tc := range ok {
		if _, err := ParseCall(tc.tool, []byte(tc.args)); err != nil {
			t.Errorf("%s %.40s: %v", tc.tool, tc.args, err)
		}
	}
}

func TestFormatNilCall(t *testing.T) {
	if n, a := FormatCall(nil); n != "" || a != nil {
		t.Fatalf("got %q %q", n, a)
	}
}
