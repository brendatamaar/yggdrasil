// Package composer is the backend of the message composer (AG-13, AG-20):
// saved prompts, @ mentions, model lists and one-shot drafts. The pure parts
// (prompt parsing, fuzzy ranking, draft text rules) are plain functions; the
// service wires them to the store, git and the task service.
package composer

import (
	"encoding/json"
	"fmt"
	"strings"
	"unicode"
	"unicode/utf8"
)

// Limits of a saved prompt.
const (
	MaxPromptName    = 60
	MaxPromptBody    = 20000
	MaxPrompts       = 200
	PromptSettingKey = "composer.prompts"
)

// Prompt is a validated saved prompt. Build it with ParsePrompt.
type Prompt struct {
	Name string
	Text string
}

// StoredPrompt is a prompt with its ID, as kept in the settings row.
type StoredPrompt struct {
	ID   string `json:"id"`
	Name string `json:"name"`
	Text string `json:"text"`
}

// RuleError is a rejected input; Message is plain language.
type RuleError struct{ Message string }

func (e *RuleError) Error() string { return e.Message }

// UserMessage is the text for people.
func (e *RuleError) UserMessage() string { return e.Message }

func reject(format string, a ...any) error { return &RuleError{Message: fmt.Sprintf(format, a...)} }

// ParsePrompt checks a name and body from outside. The name is trimmed.
func ParsePrompt(name, text string) (Prompt, error) {
	name = strings.TrimSpace(name)
	switch {
	case name == "":
		return Prompt{}, reject("Give the prompt a name.")
	case !utf8.ValidString(name) || utf8.RuneCountInString(name) > MaxPromptName:
		return Prompt{}, reject("The prompt name can have at most %d characters.", MaxPromptName)
	case strings.ContainsFunc(name, unicode.IsControl):
		return Prompt{}, reject("The prompt name can't contain control characters.")
	case strings.TrimSpace(text) == "":
		return Prompt{}, reject("The prompt text is empty.")
	case len(text) > MaxPromptBody:
		return Prompt{}, reject("The prompt text can have at most %d bytes.", MaxPromptBody)
	case strings.ContainsRune(text, 0):
		return Prompt{}, reject("The prompt text can't contain a NUL character.")
	case !utf8.ValidString(text):
		return Prompt{}, reject("The prompt text isn't valid text.")
	}
	return Prompt{Name: name, Text: text}, nil
}

// Validate reports whether p could have come from ParsePrompt.
func (p Prompt) Validate() error {
	q, err := ParsePrompt(p.Name, p.Text)
	if err != nil {
		return err
	}
	if q != p {
		return reject("The prompt name has spaces around it.")
	}
	return nil
}

// FormatPrompts is the settings value of a list.
func FormatPrompts(list []StoredPrompt) (string, error) {
	if list == nil {
		list = []StoredPrompt{}
	}
	b, err := json.Marshal(list)
	if err != nil {
		return "", fmt.Errorf("encode prompts: %w", err)
	}
	return string(b), nil
}

// ParsePrompts reads the settings value back, checking every entry.
func ParsePrompts(raw string) ([]StoredPrompt, error) {
	var list []StoredPrompt
	if err := json.Unmarshal([]byte(raw), &list); err != nil {
		return nil, fmt.Errorf("saved prompts are damaged: %w", err)
	}
	if len(list) > MaxPrompts {
		return nil, reject("There can be at most %d saved prompts.", MaxPrompts)
	}
	seen := map[string]bool{}
	for _, p := range list {
		if p.ID == "" || seen[p.ID] {
			return nil, fmt.Errorf("saved prompts are damaged: bad or repeated id %q", p.ID)
		}
		seen[p.ID] = true
		if err := (Prompt{Name: p.Name, Text: p.Text}).Validate(); err != nil {
			return nil, fmt.Errorf("saved prompt %s: %w", p.ID, err)
		}
	}
	return list, nil
}
