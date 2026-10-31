package mcp

import (
	"path"
	"reflect"
	"strings"
	"testing"
)

func FuzzParseCall(f *testing.F) {
	for _, c := range samples() {
		n, a := FormatCall(c)
		f.Add(n, a)
	}
	f.Add("get_diff", []byte(`{"repo":"r","path":"../x"}`))
	f.Add("repo_status", []byte(`{"repo":"..\\.."}`))
	f.Add("propose_delete_files", []byte(`{"repo":"r","paths":["C:/x","//h/s"]}`))
	f.Fuzz(func(t *testing.T, name string, args []byte) {
		c, err := ParseCall(name, args)
		if err != nil {
			if c != nil {
				t.Fatal("call returned with an error")
			}
			return
		}
		if err := CheckCall(c); err != nil {
			t.Fatalf("accepted but invalid: %v", err)
		}
		for _, r := range repoNames(c) {
			if r == "" || r == "." || r == ".." || strings.ContainsAny(r, `/\:`) {
				t.Fatalf("repo escapes: %q", r)
			}
		}
		for _, p := range pathsOf(c) {
			if p == "" || strings.HasPrefix(p, "/") || strings.ContainsAny(p, `\:`) || path.Clean(p) != p ||
				p == ".." || strings.HasPrefix(p, "../") || strings.Contains(p, "/../") {
				t.Fatalf("path escapes: %q", p)
			}
		}
		// Accepted calls survive a format and parse.
		n, a := FormatCall(c)
		c2, err := ParseCall(n, a)
		if err != nil || !reflect.DeepEqual(c, c2) {
			t.Fatalf("round trip: %v %#v %#v", err, c, c2)
		}
	})
}

func repoNames(c Call) []string {
	switch v := c.(type) {
	case RepoStatus:
		return []string{string(v.Repo)}
	case RemoteStatus:
		return []string{string(v.Repo)}
	case RecentCommits:
		return []string{string(v.Repo)}
	case ChangedFiles:
		return []string{string(v.Repo)}
	case GetDiff:
		return []string{string(v.Repo)}
	case BuildInfo:
		return []string{string(v.Repo)}
	case Fetch:
		return []string{string(v.Repo)}
	case Pull:
		return []string{string(v.Repo)}
	case StartBuild:
		return strs(v.Repos)
	case ProposeCommit:
		return []string{string(v.Repo)}
	case ProposePush:
		return []string{string(v.Repo)}
	case ProposeMergeRequest:
		return []string{string(v.Repo)}
	case ProposeInstall:
		return []string{string(v.Repo)}
	case ProposeAgentFile:
		return []string{string(v.Repo)}
	case ProposeDeleteFiles:
		return []string{string(v.Repo)}
	}
	return nil
}

func pathsOf(c Call) []string {
	switch v := c.(type) {
	case GetDiff:
		if v.Path != "" {
			return []string{v.Path}
		}
	case ProposeCommit:
		return v.Paths
	case ProposeAgentFile:
		return []string{v.Path}
	case ProposeDeleteFiles:
		return v.Paths
	}
	return nil
}
