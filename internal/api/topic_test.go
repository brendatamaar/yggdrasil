package api

import (
	"math/rand"
	"strings"
	"testing"

	"github.com/brendatamaar/yggdrasil/internal/core"
)

// randomValidTaskID builds IDs from the characters TaskID.Validate accepts.
func randomValidTaskID(r *rand.Rand) core.TaskID {
	const chars = "abcdefghijklmnopqrstuvwxyzABCDEFGHIJKLMNOPQRSTUVWXYZ0123456789-_"
	n := 1 + r.Intn(40)
	var b strings.Builder
	for range n {
		b.WriteByte(chars[r.Intn(len(chars))])
	}
	return core.TaskID(b.String())
}

func TestTopicWireStrings(t *testing.T) {
	if got := (tasksTopic{}).String(); got != TopicTasks {
		t.Fatalf("tasks topic = %q, want %q", got, TopicTasks)
	}
	if got := (taskTopic{ID: "abc"}).String(); got != TopicTaskPrefix+"abc" {
		t.Fatalf("task topic = %q", got)
	}
}

func TestParseTopicRoundTrip(t *testing.T) {
	r := rand.New(rand.NewSource(1))
	cases := []wsTopic{tasksTopic{}}
	for range 500 {
		cases = append(cases, taskTopic{ID: randomValidTaskID(r)})
	}
	for _, want := range cases {
		got, err := parseTopic(want.String())
		if err != nil {
			t.Fatalf("parseTopic(%q): %v", want.String(), err)
		}
		if got != want {
			t.Fatalf("round trip: got %#v, want %#v", got, want)
		}
	}
}

func TestParseTopicAcceptedIsValid(t *testing.T) {
	inputs := []string{"tasks", "task:abc", "task:a-b_c", "task:", "", "tasks:x", "task:../x", "task: x", "Tasks", "task:a b"}
	for _, in := range inputs {
		got, err := parseTopic(in)
		if err != nil {
			continue
		}
		if got.String() != in {
			t.Errorf("accepted %q but String() = %q", in, got.String())
		}
		if tt, ok := got.(taskTopic); ok {
			if verr := tt.ID.Validate(); verr != nil {
				t.Errorf("accepted %q with invalid id: %v", in, verr)
			}
		}
	}
}

func TestParseTopicRejects(t *testing.T) {
	tests := []struct{ name, in string }{
		{"empty", ""},
		{"unknown name", "builds"},
		{"unknown prefix", "repo:abc"},
		{"case differs", "Tasks"},
		{"tasks with suffix", "tasks:abc"},
		{"prefix without id", "task:"},
		{"id with path separator", "task:a/b"},
		{"id with dot dots", "task:../x"},
		{"id with space", "task:a b"},
		{"id with newline", "task:a\n"},
	}
	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			if got, err := parseTopic(tc.in); err == nil {
				t.Fatalf("parseTopic(%q) = %#v, want error", tc.in, got)
			}
		})
	}
}

func FuzzParseTopic(f *testing.F) {
	for _, s := range []string{"tasks", "task:abc", "task:", "", "task:a/b", "x"} {
		f.Add(s)
	}
	f.Fuzz(func(t *testing.T, in string) {
		got, err := parseTopic(in)
		if err != nil {
			return
		}
		if got.String() != in {
			t.Fatalf("accepted %q but String() = %q", in, got.String())
		}
		if tt, ok := got.(taskTopic); ok {
			if verr := tt.ID.Validate(); verr != nil {
				t.Fatalf("accepted %q with invalid id: %v", in, verr)
			}
		}
	})
}
