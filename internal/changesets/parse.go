package changesets

import (
	"bytes"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"slices"
	"strings"
	"time"

	"github.com/brendatamaar/yggdrasil/internal/core"
	cs "github.com/brendatamaar/yggdrasil/internal/core/changesets"
	"github.com/brendatamaar/yggdrasil/internal/core/review"
	"github.com/brendatamaar/yggdrasil/internal/db"
)

// maxBody caps a request body the parsers accept.
const maxBody = 256 << 10

// strictJSON decodes exactly one JSON value: unknown fields, trailing data and
// oversize bodies are refused.
func strictJSON(body []byte, into any) error {
	if len(body) == 0 || len(body) > maxBody {
		return errors.New("the request is empty or too large")
	}
	dec := json.NewDecoder(bytes.NewReader(body))
	dec.DisallowUnknownFields()
	if err := dec.Decode(into); err != nil {
		return fmt.Errorf("the request isn't valid: %w", err)
	}
	if _, err := dec.Token(); !errors.Is(err, io.EOF) {
		return errors.New("the request has data after the first value")
	}
	return nil
}

// Run result encoding (change_sets.run_result)

type plannedJSON struct {
	ID   string `json:"id"`
	Hash string `json:"hash"`
}

type runJSON struct {
	Kind    string        `json:"kind"`
	At      string        `json:"at,omitempty"`
	Reason  string        `json:"reason,omitempty"`
	Scope   string        `json:"scope,omitempty"`
	Repo    string        `json:"repo,omitempty"`
	Queue   []plannedJSON `json:"queue,omitempty"`
	Current string        `json:"current,omitempty"`
}

// FormatRun encodes what a state must remember about runs: the result of the
// last run, or a run in progress (scope, queue with the hashes the user saw,
// current). Every state round-trips through ParseRun.
func FormatRun(st cs.State) (json.RawMessage, error) {
	var j runJSON
	switch v := st.(type) {
	case cs.Confirming:
		j = runJSON{Kind: "confirming", Current: string(v.Current)}
		switch sc := v.Scope.(type) {
		case cs.ScopeAll:
			j.Scope = "all"
		case cs.ScopeRepo:
			j.Scope, j.Repo = "repo", string(sc.Repo)
		default:
			return nil, errors.New("a confirming change set needs a scope")
		}
		for _, p := range v.Queue {
			j.Queue = append(j.Queue, plannedJSON{ID: string(p.ID), Hash: p.Hash})
		}
	default:
		switch r := runOf(st).(type) {
		case cs.RunHalted:
			j = runJSON{Kind: "halted", At: string(r.At), Reason: r.Reason}
		case cs.RunCompleted:
			j = runJSON{Kind: "completed"}
		default:
			j = runJSON{Kind: "idle"}
		}
	}
	return json.Marshal(j)
}

func openOf(st cs.State) cs.Open {
	switch v := st.(type) {
	case cs.Open:
		return v
	case cs.Confirming:
		return v.Open
	case cs.Shipped:
		return v.Open
	case cs.Closed:
		return v.Open
	}
	return cs.Open{}
}

func runOf(st cs.State) cs.RunResult { return openOf(st).LastRun }

func isHex64(s string) bool { return len(s) == 64 && strings.Trim(s, "0123456789abcdef") == "" }

// ParseRun decodes a stored run result. It returns the result of the last run
// and, for a run in progress, the scope, queue and current proposal.
func ParseRun(raw json.RawMessage) (last cs.RunResult, run *cs.Confirming, err error) {
	var j runJSON
	if err := strictJSON(raw, &j); err != nil {
		return nil, nil, fmt.Errorf("run result: %w", err)
	}
	switch j.Kind {
	case "idle":
		return cs.RunIdle{}, nil, nil
	case "completed":
		return cs.RunCompleted{}, nil, nil
	case "halted":
		at := core.ProposalID(j.At)
		if err := at.Validate(); err != nil {
			return nil, nil, fmt.Errorf("run result: %w", err)
		}
		return cs.RunHalted{At: at, Reason: j.Reason}, nil, nil
	case "confirming":
		c := &cs.Confirming{Current: core.ProposalID(j.Current)}
		switch j.Scope {
		case "all":
			c.Scope = cs.ScopeAll{}
		case "repo":
			c.Scope = cs.ScopeRepo{Repo: core.RepoID(j.Repo)}
		default:
			return nil, nil, fmt.Errorf("run result: unknown scope %q", j.Scope)
		}
		if err := errors.Join(c.Scope.Validate(), c.Current.Validate()); err != nil {
			return nil, nil, fmt.Errorf("run result: %w", err)
		}
		for _, p := range j.Queue {
			if err := core.ProposalID(p.ID).Validate(); err != nil || !isHex64(p.Hash) {
				return nil, nil, errors.New("run result: a queued proposal is not valid")
			}
			c.Queue = append(c.Queue, cs.Planned{ID: core.ProposalID(p.ID), Hash: p.Hash})
		}
		if len(c.Queue) > cs.MaxMembers {
			return nil, nil, errors.New("run result: the queue is too long")
		}
		return cs.RunIdle{}, c, nil
	}
	return nil, nil, fmt.Errorf("run result: unknown kind %q", j.Kind)
}

// StateFromRows builds the state of a change set from its rows and rejects
// rows that disagree: a stored order that is not a permutation of the set's
// repos (a missing repo is appended, an unknown or repeated one is refused),
// a member with a bad ID, kind, status or hash, a run in a set that is not
// open. Links hold the MR URL of each executed merge_request member only.
func StateFromRows(row db.ChangeSetRow, taskRepos []core.RepoID, members []db.ChangeSetMemberRow) (cs.State, error) {
	repos := slices.Clone(taskRepos)
	for _, m := range members {
		repos = append(repos, m.Repo)
	}
	slices.Sort(repos)
	repos = slices.Compact(repos)

	order := make([]core.RepoID, 0, len(repos))
	seen := map[core.RepoID]bool{}
	for _, r := range row.MergeOrder {
		if !slices.Contains(repos, r) || seen[r] {
			return nil, fmt.Errorf("change set %s: the stored merge order doesn't match its repos", row.ID)
		}
		seen[r] = true
		order = append(order, r)
	}
	for _, r := range repos {
		if !seen[r] {
			order = append(order, r)
		}
	}

	o := cs.Open{Set: cs.Header{ID: row.ID, Task: row.Task, Repos: repos, Order: order}}
	for _, m := range members {
		o.Members = append(o.Members, cs.Member{ID: m.ID, Repo: m.Repo, Kind: m.Kind, Status: m.Status, Hash: m.Hash})
		if m.Kind == core.ProposalMergeRequest && m.Status == core.ProposalExecuted && m.MRURL != "" {
			if o.Links == nil {
				o.Links = map[core.RepoID]string{}
			}
			o.Links[m.Repo] = m.MRURL
		}
	}
	last, run, err := ParseRun(row.RunResult)
	if err != nil {
		return nil, fmt.Errorf("change set %s: %w", row.ID, err)
	}
	o.LastRun = last
	var st cs.State
	switch row.Status {
	case core.ChangeSetOpen:
		st = o
		if run != nil {
			run.Open = o
			st = *run
		}
	case core.ChangeSetShipped:
		st = cs.Shipped{Open: o}
	case core.ChangeSetClosed:
		st = cs.Closed{Open: o}
	default:
		return nil, fmt.Errorf("change set %s: unknown status %q", row.ID, row.Status)
	}
	if run != nil && row.Status != core.ChangeSetOpen {
		return nil, fmt.Errorf("change set %s: a run is stored in a set that is not open", row.ID)
	}
	if err := st.Validate(); err != nil {
		return nil, fmt.Errorf("change set %s: %w", row.ID, err)
	}
	return st, nil
}

// RowOfState is the row to store for a state: status, order, run result. The
// version and the time stay those of base.
func RowOfState(st cs.State, base db.ChangeSetRow) (db.ChangeSetRow, error) {
	run, err := FormatRun(st)
	if err != nil {
		return db.ChangeSetRow{}, err
	}
	o := openOf(st)
	base.Status, base.MergeOrder, base.RunResult = st.Status(), slices.Clone(o.Set.Order), run
	return base, nil
}

// Order

// DecodeOrder reads the body of PUT order: {"order":["rep_a", ...]}.
func DecodeOrder(body []byte) ([]string, error) {
	var b struct {
		Order []string `json:"order"`
	}
	if err := strictJSON(body, &b); err != nil {
		return nil, err
	}
	return b.Order, nil
}

// ParseOrder accepts a merge order only if it lists each repo of the set
// exactly once.
func ParseOrder(raw []string, repos []core.RepoID) ([]core.RepoID, error) {
	if len(raw) == 0 {
		return nil, errors.New("the merge order is empty")
	}
	if len(raw) > cs.MaxRepos {
		return nil, fmt.Errorf("a merge order lists at most %d repos", cs.MaxRepos)
	}
	out := make([]core.RepoID, len(raw))
	for i, r := range raw {
		out[i] = core.RepoID(r)
		if err := out[i].Validate(); err != nil {
			return nil, err
		}
		if !slices.Contains(repos, out[i]) {
			return nil, fmt.Errorf("repo %s isn't part of this change set", r)
		}
		if slices.Contains(out[:i], out[i]) {
			return nil, fmt.Errorf("repo %s appears twice in the merge order", r)
		}
	}
	if !cs.IsPermutation(out, repos) {
		return nil, errors.New("the merge order must list every repo of the change set")
	}
	return out, nil
}

// FormatOrder is the wire form of an order (the inverse of DecodeOrder after
// marshalling).
func FormatOrder(order []core.RepoID) []string {
	out := make([]string, len(order))
	for i, r := range order {
		out[i] = string(r)
	}
	return out
}

// Confirm run

// RawConfirm is the body of POST confirm.
type RawConfirm struct {
	// Scope is "all" or "repo".
	Scope string `json:"scope"`
	// Repo names the repo when Scope is "repo".
	Repo string `json:"repo,omitempty"`
	// Seen is the action hash the user saw for each proposal of the run.
	Seen map[string]string `json:"seen"`
	// Ack lists the warning codes the user acknowledged.
	Ack []string `json:"ack,omitempty"`
}

// DecodeConfirmRun reads the body of POST confirm.
func DecodeConfirmRun(body []byte) (RawConfirm, error) {
	var r RawConfirm
	if err := strictJSON(body, &r); err != nil {
		return RawConfirm{}, err
	}
	return r, nil
}

// ParseConfirmRun turns a confirm request into core values. The proposals the
// user saw must belong to the set (members).
func ParseConfirmRun(raw RawConfirm, repos []core.RepoID, members []cs.Member) (cs.Scope, map[core.ProposalID]string, []cs.WarningCode, error) {
	if err := scopeErr(raw.Scope, raw.Repo, repos); err != nil {
		return nil, nil, nil, err
	}
	scope := scopeOf(raw.Scope, raw.Repo)
	if len(raw.Seen) == 0 || len(raw.Seen) > cs.MaxMembers {
		return nil, nil, nil, errors.New("list the proposals you reviewed with their hashes")
	}
	seen := make(map[core.ProposalID]string, len(raw.Seen))
	for id, h := range raw.Seen {
		pid := core.ProposalID(id)
		if err := pid.Validate(); err != nil {
			return nil, nil, nil, err
		}
		if !isHex64(h) {
			return nil, nil, nil, fmt.Errorf("the hash for %s isn't a 64-character hex hash", id)
		}
		if !slices.ContainsFunc(members, func(m cs.Member) bool { return m.ID == pid }) {
			return nil, nil, nil, fmt.Errorf("proposal %s isn't part of this change set", id)
		}
		seen[pid] = h
	}
	if len(raw.Ack) > 16 {
		return nil, nil, nil, errors.New("too many acknowledged warnings")
	}
	var ack []cs.WarningCode
	for _, a := range raw.Ack {
		c := cs.WarningCode(a)
		if err := c.Validate(); err != nil {
			return nil, nil, nil, err
		}
		ack = append(ack, c)
	}
	return scope, seen, ack, nil
}

// Notes

// DecodeNote reads the body of POST notes. The ID, task and time are the
// server's, not the client's.
func DecodeNote(body []byte) (review.RawNote, error) {
	var b struct {
		Repo     string `json:"repo"`
		Path     string `json:"path"`
		Line     int    `json:"line"`
		Side     string `json:"side"`
		Text     string `json:"text"`
		DiffHash string `json:"diffHash"`
	}
	if err := strictJSON(body, &b); err != nil {
		return review.RawNote{}, err
	}
	return review.RawNote{Repo: b.Repo, Path: b.Path, Line: b.Line, Side: b.Side, Text: b.Text, DiffHash: b.DiffHash}, nil
}

// DecodeNoteIDs reads the body of POST notes/send: {"ids":["nte_..."]}.
func DecodeNoteIDs(body []byte) ([]string, error) {
	var b struct {
		IDs []string `json:"ids"`
	}
	if err := strictJSON(body, &b); err != nil {
		return nil, err
	}
	return b.IDs, nil
}

// ParseNoteIDs checks a list of note IDs: 1 to MaxNotesPerTask, well formed,
// no repeats.
func ParseNoteIDs(raw []string) ([]review.NoteID, error) {
	if len(raw) == 0 {
		return nil, errors.New("choose at least one note to send")
	}
	if len(raw) > review.MaxNotesPerTask {
		return nil, fmt.Errorf("at most %d notes can be sent at once", review.MaxNotesPerTask)
	}
	out := make([]review.NoteID, 0, len(raw))
	for _, s := range raw {
		id := review.NoteID(s)
		if err := id.Validate(); err != nil {
			return nil, err
		}
		if slices.Contains(out, id) {
			return nil, fmt.Errorf("note %s is listed twice", s)
		}
		out = append(out, id)
	}
	return out, nil
}

// NoteFromRow parses a stored note and rejects an inconsistent row.
func NoteFromRow(r db.NoteRow) (review.Note, error) {
	n := review.Note{
		ID: review.NoteID(r.ID), Task: r.Task, Repo: r.Repo, Text: r.Text, DiffHash: r.DiffHash, Created: r.CreatedAt,
		Anchor: review.Anchor{Path: review.RelPath(r.Path), Line: r.Line, Side: review.Side(r.Side)},
	}
	at := func() (time.Time, error) {
		if r.StateAt == nil {
			return time.Time{}, errors.New("has no state time")
		}
		return *r.StateAt, nil
	}
	var err error
	switch r.State {
	case "open":
		n.State = review.NoteOpen{}
	case "sending":
		var t time.Time
		if t, err = at(); err == nil {
			n.State = review.NoteSending{Batch: review.BatchID(r.Batch), At: t}
		}
	case "sent":
		var t time.Time
		if t, err = at(); err == nil {
			n.State = review.NoteSent{Turn: review.TurnID(r.Turn), At: t}
		}
	case "outdated":
		var t time.Time
		if t, err = at(); err == nil {
			n.State = review.NoteOutdated{At: t}
		}
	default:
		err = fmt.Errorf("has unknown state %q", r.State)
	}
	if err != nil {
		return review.Note{}, fmt.Errorf("note %s %w", r.ID, err)
	}
	if err := n.Validate(); err != nil {
		return review.Note{}, fmt.Errorf("note %s: %w", r.ID, err)
	}
	return n, nil
}

// RowOfNote is the row to store for a note; the version is the caller's.
func RowOfNote(n review.Note) db.NoteRow {
	r := db.NoteRow{
		ID: string(n.ID), Task: n.Task, Repo: n.Repo, Path: string(n.Anchor.Path), Line: n.Anchor.Line,
		Side: string(n.Anchor.Side), Text: n.Text, DiffHash: n.DiffHash, CreatedAt: n.Created,
	}
	switch s := n.State.(type) {
	case review.NoteOpen:
		r.State = "open"
	case review.NoteSending:
		r.State, r.Batch, r.StateAt = "sending", string(s.Batch), &s.At
	case review.NoteSent:
		r.State, r.Turn, r.StateAt = "sent", string(s.Turn), &s.At
	case review.NoteOutdated:
		r.State, r.StateAt = "outdated", &s.At
	}
	return r
}
