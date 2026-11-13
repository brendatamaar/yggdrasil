package repos_test

import (
	"reflect"
	"strings"
	"testing"

	"github.com/brendatamaar/yggdrasil/internal/core/repos"
)

func TestPullCompletion(t *testing.T) {
	for _, tc := range []struct {
		name, before, after string
		read                bool
		want                repos.ActOutcome
	}{
		{"unchanged", "abc", "abc", true, repos.UpToDate{}},
		{"both unborn", "", "", true, repos.UpToDate{}},
		{"advanced", "0123456789012345", "abcdefabcdefabcd", true, repos.Pulled{From: "012345678901", To: "abcdefabcdef"}},
		{"short IDs", "abc", "def", true, repos.Pulled{From: "abc", To: "def"}},
		{"unavailable", "abc", "ignored", false, repos.Pulled{From: "abc"}},
		{"unavailable equal", "abc", "abc", false, repos.Pulled{From: "abc"}},
		{"unavailable unborn", "", "", false, repos.Pulled{}},
	} {
		t.Run(tc.name, func(t *testing.T) {
			if got := repos.PullCompletion(tc.before, tc.after, tc.read); !reflect.DeepEqual(got, tc.want) {
				t.Fatalf("got %#v, want %#v", got, tc.want)
			}
		})
	}
}

// Every successful mutation remains successful even if its follow-up read
// fails. No unobserved destination is reported and displayed IDs stay bounded.
func FuzzPullCompletionEvidence(f *testing.F) {
	f.Add("abc", "def", true)
	f.Add("", "", false)
	f.Add(strings.Repeat("a", 40), strings.Repeat("b", 64), true)
	f.Fuzz(func(t *testing.T, before, after string, read bool) {
		out := repos.PullCompletion(before, after, read)
		switch x := out.(type) {
		case repos.UpToDate:
			if !read || before != after {
				t.Fatal("up-to-date requires observed equal HEADs")
			}
		case repos.Pulled:
			if read && before == after {
				t.Fatal("equal observed HEADs must be up to date")
			}
			if len(x.From) > 12 || len(x.To) > 12 || !strings.HasPrefix(before, x.From) || (read && !strings.HasPrefix(after, x.To)) {
				t.Fatalf("invalid displayed IDs: %#v", x)
			}
			if !read && x.To != "" {
				t.Fatal("unobserved destination reported")
			}
		default:
			t.Fatalf("a successful pull became %#v", out)
		}
		if again := repos.PullCompletion(before, after, read); !reflect.DeepEqual(again, out) {
			t.Fatal("non-deterministic outcome")
		}
	})
}
