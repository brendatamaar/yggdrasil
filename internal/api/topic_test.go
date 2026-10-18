package api

import (
	"math/rand"
	"strings"
	"testing"

	"github.com/brendatamaar/yggdrasil/internal/core"
)

// randomValidID builds IDs from the characters the ID types' Validate accepts.
func randomValidID(r *rand.Rand) string {
	const chars = "abcdefghijklmnopqrstuvwxyzABCDEFGHIJKLMNOPQRSTUVWXYZ0123456789-_"
	n := 1 + r.Intn(40)
	var b strings.Builder
	for range n {
		b.WriteByte(chars[r.Intn(len(chars))])
	}
	return b.String()
}

// validID checks the ID inside a parsed topic with the ID type's own rule.
func validID(t wsTopic) error {
	switch tt := t.(type) {
	case taskTopic:
		return tt.ID.Validate()
	case repoTopic:
		return tt.ID.Validate()
	case buildTopic:
		return tt.ID.Validate()
	}
	return nil
}

func TestTopicWireStrings(t *testing.T) {
	cases := []struct {
		topic wsTopic
		want  string
	}{
		{tasksTopic{}, "tasks"},
		{taskTopic{ID: "abc"}, "task:abc"},
		{reposTopic{}, "repos"},
		{repoTopic{ID: "abc"}, "repo:abc"},
		{buildsTopic{}, "builds"},
		{buildTopic{ID: "abc"}, "build:abc"},
		{hostsTopic{}, "hosts"},
	}
	for _, c := range cases {
		if got := c.topic.String(); got != c.want {
			t.Errorf("%T = %q, want %q", c.topic, got, c.want)
		}
	}
	if TopicTasks != "tasks" || TopicRepos != "repos" || TopicBuilds != "builds" || TopicHosts != "hosts" ||
		TopicTaskPrefix != "task:" || TopicRepoPrefix != "repo:" || TopicBuildPrefix != "build:" {
		t.Fatal("a wire constant changed: the frontend subscribes with these strings")
	}
}

func TestParseTopicRoundTrip(t *testing.T) {
	r := rand.New(rand.NewSource(1))
	cases := []wsTopic{tasksTopic{}, reposTopic{}, buildsTopic{}, hostsTopic{}}
	for range 300 {
		cases = append(cases,
			taskTopic{ID: core.TaskID(randomValidID(r))},
			repoTopic{ID: core.RepoID(randomValidID(r))},
			buildTopic{ID: core.BuildID(randomValidID(r))},
		)
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
	inputs := []string{
		"tasks", "task:abc", "task:a-b_c", "task:", "", "tasks:x", "task:../x", "task: x", "Tasks", "task:a b",
		"repos", "repo:r1", "repo:", "repos:x", "builds", "build:b1", "build:", "builds:x", "hosts", "hosts:x", "host:x",
	}
	for _, in := range inputs {
		got, err := parseTopic(in)
		if err != nil {
			continue
		}
		if got.String() != in {
			t.Errorf("accepted %q but String() = %q", in, got.String())
		}
		if verr := validID(got); verr != nil {
			t.Errorf("accepted %q with invalid id: %v", in, verr)
		}
	}
}

func TestParseTopicRejects(t *testing.T) {
	tests := []struct{ name, in string }{
		{"empty", ""},
		{"unknown name", "build"},
		{"unknown prefix", "foo:abc"},
		{"case differs", "Tasks"},
		{"case differs repos", "Repos"},
		{"tasks with suffix", "tasks:abc"},
		{"repos with suffix", "repos:abc"},
		{"builds with suffix", "builds:abc"},
		{"hosts with suffix", "hosts:abc"},
		{"hosts with id", "hosts:github.com"},
		{"singular host", "host"},
		{"task prefix without id", "task:"},
		{"repo prefix without id", "repo:"},
		{"build prefix without id", "build:"},
		{"task id with path separator", "task:a/b"},
		{"repo id with path separator", "repo:a/b"},
		{"build id with path separator", "build:a\\b"},
		{"id with dot dots", "task:../x"},
		{"repo id with space", "repo:a b"},
		{"build id with space", "build:a b"},
		{"id with newline", "task:a\n"},
		{"repo id with newline", "repo:a\n"},
		{"build id with tab", "build:a\t"},
		{"plural prefix", "tasks:abc"},
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
	for _, s := range []string{
		"tasks", "task:abc", "task:", "", "task:a/b", "x",
		"repos", "repo:r1", "repo:", "repo:a b", "builds", "build:b1", "build:", "build:a/b", "hosts", "hosts:x",
	} {
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
		if verr := validID(got); verr != nil {
			t.Fatalf("accepted %q with invalid id: %v", in, verr)
		}
	})
}
