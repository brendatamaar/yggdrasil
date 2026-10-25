package guard

import (
	"encoding/json"
	"fmt"
	"strings"

	"github.com/brendatamaar/yggdrasil/internal/core"
)

// policyWire is the policy file's shape. The core types carry no JSON tags,
// so the file format lives here, next to its parser.
type policyWire struct {
	Mode        string     `json:"mode"`
	ReadRoots   []string   `json:"readRoots"`
	WriteRoots  []string   `json:"writeRoots"`
	Allow       []ruleWire `json:"allow"`
	FoldCase    bool       `json:"foldCase"`
	Enforcement string     `json:"enforcement"`
}

type ruleWire struct {
	Program string   `json:"program"`
	Args    []string `json:"args"`
	Tail    string   `json:"tail"`
}

// ParsePolicy turns the policy file written by the tasks service into a
// Policy and rejects what Policy.Validate rejects, plus unknown fields and
// trailing data. A hook that cannot parse its policy denies every call.
func ParsePolicy(data []byte) (Policy, error) {
	if !json.Valid(data) {
		return Policy{}, fmt.Errorf("parse policy: not a single valid JSON document")
	}
	dec := json.NewDecoder(strings.NewReader(string(data)))
	dec.DisallowUnknownFields()
	var w policyWire
	if err := dec.Decode(&w); err != nil {
		return Policy{}, fmt.Errorf("parse policy: %w", err)
	}
	p := Policy{
		Mode:        core.TaskMode(w.Mode),
		ReadRoots:   nilIfEmpty(w.ReadRoots),
		WriteRoots:  nilIfEmpty(w.WriteRoots),
		FoldCase:    w.FoldCase,
		Enforcement: Enforcement(w.Enforcement),
	}
	for _, r := range w.Allow {
		p.Allow = append(p.Allow, CommandRule{Program: r.Program, Args: nilIfEmpty(r.Args), Tail: TailRule(r.Tail)})
	}
	if err := p.Validate(); err != nil {
		return Policy{}, fmt.Errorf("parse policy: %w", err)
	}
	return p, nil
}

// FormatPolicy is the inverse of ParsePolicy: ParsePolicy(FormatPolicy(p))
// equals p for every valid p. It returns nil only if p cannot be encoded,
// which a hook then reads as "no policy" and denies.
func FormatPolicy(p Policy) []byte {
	w := policyWire{
		Mode:        string(p.Mode),
		ReadRoots:   p.ReadRoots,
		WriteRoots:  p.WriteRoots,
		FoldCase:    p.FoldCase,
		Enforcement: string(p.Enforcement),
	}
	for _, r := range p.Allow {
		w.Allow = append(w.Allow, ruleWire{Program: r.Program, Args: r.Args, Tail: string(r.Tail)})
	}
	out, err := json.Marshal(w)
	if err != nil {
		return nil
	}
	return out
}

func nilIfEmpty(s []string) []string {
	if len(s) == 0 {
		return nil
	}
	return s
}
