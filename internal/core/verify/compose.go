package verify

import (
	"errors"
	"fmt"
	"strconv"
	"strings"
	"unicode"
	"unicode/utf8"

	"github.com/brendatamaar/yggdrasil/internal/core"
)

// MaxMessageBytes bounds the message ComposeFailure returns. It is the task
// prompt limit (core/tasks.MaxPromptBytes, 100 KiB; domain packages do not
// import each other) minus a 4 KiB margin for the shell's own framing.
const MaxMessageBytes = 100*1024 - 4*1024

const (
	pathPlaceholder = "<path>"
	omittedNote     = "(earlier output omitted)\n"
	maxStepRunes    = MaxNameRunes
)

// FailureReport is what ComposeFailure turns into a message for the agent
// (VF-03). LogTail is already redacted by the shell (TailLog); the composer
// still makes it safe to embed.
type FailureReport struct {
	Repo    core.RepoID
	Stage   Stage
	Step    string
	Reason  FailReason
	LogTail string
}

// ComposeFailure builds the "Send failure to agent" message, with at most
// MaxFailureLogBytes of log. A failed bootstrap is worded differently from a
// failed verify step. The log is fenced with a backtick run longer than any
// run inside it, so it cannot close its own wrapper; invalid UTF-8 and control
// characters are replaced or dropped, and absolute paths are replaced by
// "<path>" so nothing outside the repo leaks. Pure; not part of Step.
func ComposeFailure(r FailureReport) (string, error) {
	if err := r.Repo.Validate(); err != nil {
		return "", fmt.Errorf("compose failure: %w", err)
	}
	if err := r.Stage.Validate(); err != nil {
		return "", fmt.Errorf("compose failure: %w", err)
	}
	if r.Reason == nil {
		return "", errors.New("compose failure: the failure has no reason")
	}
	if err := r.Reason.Validate(); err != nil {
		return "", fmt.Errorf("compose failure: %w", err)
	}

	step := oneLine(r.Step, maxStepRunes)
	if step == "" {
		step = "(unnamed step)"
	}
	repo := oneLine(string(r.Repo), maxStepRunes)

	var b strings.Builder
	if r.Stage == StageBootstrap {
		b.WriteString("Setting up the dependencies in repo " + repo + " failed, so the checks could not run.\n")
	} else {
		b.WriteString("The checks for repo " + repo + " failed after your last turn.\n")
	}
	b.WriteString("\nStep: " + step + "\n")
	switch reason := r.Reason.(type) {
	case ExitCode:
		b.WriteString("Result: the step exited with code " + strconv.Itoa(reason.Code) + ".\n")
	case StartFailed:
		b.WriteString("Result: the step could not start: " + oneLine(string(reason.Failure), MaxStartFailureRunes) + "\n")
	}

	log := tailLines(cleanLog(r.LogTail), MaxFailureLogBytes)
	if log.text != "" {
		fence := fenceFor(log.text)
		b.WriteString("\nLast output:\n" + fence + "\n")
		if log.cut {
			b.WriteString(omittedNote)
		}
		b.WriteString(log.text)
		if !strings.HasSuffix(log.text, "\n") {
			b.WriteString("\n")
		}
		b.WriteString(fence + "\n")
	}

	if r.Stage == StageBootstrap {
		b.WriteString("\nPlease fix the dependency setup (for example the lockfile or manifest) so it installs cleanly.\n")
	} else {
		b.WriteString("\nPlease fix the cause and make the checks pass.\n")
	}
	out := b.String()
	if len(out) > MaxMessageBytes { // unreachable with the caps above; keeps the bound a guarantee
		return "", errors.New("compose failure: the message is too long")
	}
	return out, nil
}

// oneLine makes s safe to put on one line of prose: control and format
// characters become spaces, backticks become quotes, and it is cut to max
// runes.
func oneLine(s string, max int) string {
	s = strings.ToValidUTF8(s, "�")
	s = strings.Map(func(r rune) rune {
		switch {
		case r == '`':
			return '\''
		case unicode.IsControl(r) || unicode.Is(unicode.Cf, r):
			return ' '
		}
		return r
	}, s)
	s = strings.TrimSpace(s)
	if utf8.RuneCountInString(s) > max {
		s = string([]rune(s)[:max])
	}
	return s
}

// cleanLog makes log text safe to embed: valid UTF-8, LF line ends, no
// control or format characters except newline and tab, and no absolute paths.
func cleanLog(s string) string {
	s = strings.ToValidUTF8(s, "�")
	s = strings.ReplaceAll(s, "\r\n", "\n")
	s = strings.Map(func(r rune) rune {
		switch {
		case r == '\n' || r == '\t':
			return r
		case unicode.IsControl(r) || unicode.Is(unicode.Cf, r):
			return -1
		}
		return r
	}, s)
	lines := strings.Split(s, "\n")
	for i, l := range lines {
		lines[i] = redactPaths(l)
	}
	return strings.Join(lines, "\n")
}

// redactPaths replaces every absolute path in a line (drive letter, UNC or a
// rooted multi-segment path) by "<path>". Paths relative to the repo stay.
func redactPaths(line string) string {
	var out strings.Builder
	i := 0
	for i < len(line) {
		if isPathStart(line, i) {
			out.WriteString(pathPlaceholder)
			for i < len(line) && !isPathEnd(line[i]) {
				i++
			}
			continue
		}
		out.WriteByte(line[i])
		i++
	}
	return out.String()
}

func isPathEnd(c byte) bool {
	switch c {
	case ' ', '\t', '"', '\'', '`', '<', '>', '|', ',', ';', ')', ']', '}':
		return true
	}
	return false
}

// isPathStart reports whether an absolute path begins at line[i]: it must sit
// at a token start (line start, after space or an opening/assignment mark).
func isPathStart(line string, i int) bool {
	if i > 0 {
		switch line[i-1] {
		case ' ', '\t', '"', '\'', '`', '(', '[', '{', '=', '<', ':':
		default:
			return false
		}
	}
	rest := line[i:]
	switch {
	case len(rest) >= 3 && isLetter(rest[0]) && rest[1] == ':' && (rest[2] == '\\' || rest[2] == '/'):
		return true
	case strings.HasPrefix(rest, `\\`):
		return true
	case len(rest) >= 3 && rest[0] == '/' && rest[1] != '/' && rest[1] != ' ':
		return strings.ContainsRune(rest[2:], '/')
	}
	return false
}

func isLetter(c byte) bool { return c >= 'a' && c <= 'z' || c >= 'A' && c <= 'Z' }

type tail struct {
	text string
	cut  bool
}

// tailLines keeps the last max bytes of s, starting at a line boundary (or,
// for one huge line, at a rune boundary).
func tailLines(s string, max int) tail {
	if len(s) <= max {
		return tail{text: s}
	}
	start := len(s) - max
	if s[start-1] != '\n' {
		if nl := strings.IndexByte(s[start:], '\n'); nl >= 0 {
			start += nl + 1
		} else {
			for start < len(s) && !utf8.RuneStart(s[start]) {
				start++
			}
		}
	}
	return tail{text: s[start:], cut: true}
}

// fenceFor returns a backtick run longer than any run in s (at least three),
// so no line of s can close the fence.
func fenceFor(s string) string {
	longest, run := 0, 0
	for i := 0; i < len(s); i++ {
		if s[i] == '`' {
			run++
			longest = max(longest, run)
		} else {
			run = 0
		}
	}
	return strings.Repeat("`", max(3, longest+1))
}
