package proposals

import (
	"reflect"
	"strings"
	"testing"
)

func FuzzParseAction(f *testing.F) {
	for kind, args := range validArgs {
		f.Add(kind, []byte(args))
	}
	f.Add("commit", []byte(`{"worktree":"w","paths":["C:/x","..\\y","CON"],"message":"m","expect_diff_hash":"abcdef0"}`))
	f.Add("delete_files", []byte(`{"repo":"r","paths":["a/../../b"]}`))
	f.Fuzz(func(t *testing.T, kind string, data []byte) {
		a, err := ParseAction(kind, data)
		if err != nil {
			if a != nil {
				t.Fatal("error with an action")
			}
			return
		}
		checkValid(t, a)
		for _, p := range pathsOf(a) {
			s := string(p)
			if strings.HasPrefix(s, "/") || strings.ContainsAny(s, `\:`) || strings.Contains(s, "..") && hasDotDotSegment(s) {
				t.Fatalf("path %q is not relative and inside", s)
			}
		}
		// accepted input survives the stored form
		k, payload := RowOf(a)
		back, err := ActionFromRow(k, payload)
		if err != nil || !reflect.DeepEqual(back, a) {
			t.Fatalf("round trip failed: %#v -> %#v, %v", a, back, err)
		}
	})
}

func hasDotDotSegment(s string) bool {
	for _, seg := range strings.Split(s, "/") {
		if seg == ".." {
			return true
		}
	}
	return false
}

func pathsOf(a Action) []RelPath {
	switch v := a.(type) {
	case Commit:
		return v.Paths
	case DeleteFiles:
		return v.Paths
	case AgentFile:
		return []RelPath{v.Path}
	}
	return nil
}

func FuzzParsePushOutput(f *testing.F) {
	f.Add("remote: View merge request for x:\nremote:   https://gl.example/m/1\n")
	f.Add("remote: View merge request for x: https://u:p@gl.example/m/1")
	f.Add("View merge request http://[::1]:80/a")
	f.Fuzz(func(t *testing.T, out string) {
		r, err := ParsePushOutput(out)
		if err != nil {
			return
		}
		u := r.MRURL
		if !SafeWebURL(u) {
			t.Fatalf("accepted %q fails SafeWebURL", u)
		}
		// independent restatement of the rules
		lu := strings.ToLower(u)
		if !strings.HasPrefix(lu, "http://") && !strings.HasPrefix(lu, "https://") {
			t.Fatalf("scheme of %q", u)
		}
		rest := u[strings.Index(u, "://")+3:]
		auth := rest
		if i := strings.IndexAny(rest, "/?#"); i >= 0 {
			auth = rest[:i]
		}
		if auth == "" || strings.Contains(auth, "@") || strings.ContainsAny(u, " \t\r\n\\") {
			t.Fatalf("accepted %q has userinfo, no host or spaces", u)
		}
	})
}

func FuzzCheckRemote(f *testing.F) {
	for _, s := range []string{"https://h/x.git", "git@h:x.git", "ssh://git@h/x", "https://u:p@h/x"} {
		f.Add(s)
	}
	f.Fuzz(func(t *testing.T, in string) {
		r, err := CheckRemote(in)
		if err != nil {
			return
		}
		if r.Host == "" || strings.Contains(r.Host, "@") {
			t.Fatalf("accepted %q with host %q", in, r.Host)
		}
		if r.Scheme == "https" || r.Scheme == "http" {
			if strings.Contains(strings.SplitN(in[strings.Index(in, "://")+3:], "/", 2)[0], "@") {
				t.Fatalf("accepted userinfo in %q", in)
			}
		}
	})
}

func FuzzBranchAndPath(f *testing.F) {
	for _, s := range []string{"main", "a/../b", "-x", "C:\\x", "con"} {
		f.Add(s)
	}
	f.Fuzz(func(t *testing.T, s string) {
		if CheckBranch(s) == nil && (strings.HasPrefix(s, "-") || strings.HasPrefix(s, "+") || strings.ContainsAny(s, ": \\")) {
			t.Fatalf("branch %q accepted", s)
		}
		if CheckRelPath(s) == nil && (strings.HasPrefix(s, "/") || strings.ContainsAny(s, `:\`) || hasDotDotSegment(s)) {
			t.Fatalf("path %q accepted", s)
		}
	})
}
