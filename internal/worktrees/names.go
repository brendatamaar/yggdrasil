package worktrees

import (
	"errors"
	"fmt"
	"strconv"
	"strings"
)

// MaxNameLen keeps worktree folders short (WE-04).
const MaxNameLen = 16

// Name is a worktree name: "word-word" in lower case with an optional last
// digit, at most MaxNameLen characters. It is also the branch suffix
// (ygg/<name>) and the base of the folder name.
type Name string

// Rand is the source of randomness. The shell injects it; tests script it.
type Rand interface {
	// IntN returns a value in [0, n). n is always > 0.
	IntN(n int) int
}

var adjectives = []string{
	"amber", "brisk", "calm", "dusty", "eager", "fuzzy", "gentle", "hazy",
	"icy", "jolly", "keen", "lucid", "mellow", "nimble", "odd", "plain",
	"quiet", "rapid", "sunny", "tidy", "vivid", "warm", "young", "zesty",
}

var nouns = []string{
	"acorn", "birch", "cedar", "delta", "ember", "fjord", "grove", "heron",
	"iris", "juniper", "kelp", "lark", "maple", "otter", "pine", "quartz",
	"raven", "spruce", "thistle", "willow", "yarrow", "moss", "reef", "dune",
}

// Generate makes a name from r. From the second attempt on (a collision) it
// ends in a digit, so a small word list still gives enough different names.
func Generate(r Rand, attempt int) Name {
	n := adjectives[r.IntN(len(adjectives))] + "-" + nouns[r.IntN(len(nouns))]
	if attempt > 0 {
		n += strconv.Itoa(r.IntN(10))
	}
	return Name(n)
}

// ErrBadName is wrapped by every ParseName failure.
var ErrBadName = errors.New("invalid worktree name")

// ParseName accepts "word-word" with an optional last digit, lower-case
// letters only, at most MaxNameLen characters. It is the boundary parser for
// names that come from the database or the API.
func ParseName(s string) (Name, error) {
	if len(s) > MaxNameLen {
		return "", fmt.Errorf("%w: %d characters is longer than %d", ErrBadName, len(s), MaxNameLen)
	}
	body := s
	if n := len(s); n > 0 && s[n-1] >= '0' && s[n-1] <= '9' {
		body = s[:n-1]
	}
	a, b, ok := strings.Cut(body, "-")
	if !ok {
		return "", fmt.Errorf("%w: expected two words joined by a dash", ErrBadName)
	}
	for _, w := range []string{a, b} {
		if w == "" {
			return "", fmt.Errorf("%w: a word is empty", ErrBadName)
		}
		for i := 0; i < len(w); i++ {
			if w[i] < 'a' || w[i] > 'z' {
				return "", fmt.Errorf("%w: only lower-case letters are allowed in a word", ErrBadName)
			}
		}
	}
	return Name(s), nil
}

// Branch is the branch the name gets.
func (n Name) Branch() string { return "ygg/" + string(n) }
