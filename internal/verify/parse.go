package verify

import (
	"bytes"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"strings"
	"time"
	"unicode/utf8"

	"github.com/brendatamaar/yggdrasil/internal/builds"
	"github.com/brendatamaar/yggdrasil/internal/core"
	corebuilds "github.com/brendatamaar/yggdrasil/internal/core/builds"
	cv "github.com/brendatamaar/yggdrasil/internal/core/verify"
	"github.com/brendatamaar/yggdrasil/internal/db"
)

// The parsers of this file are the boundary (docs/AGENTS.md 5.11): outside
// data (database rows, build states, settings, logs) becomes core/verify
// types here and invalid values are rejected here.

// Record is a verification as the shell holds it: the core aggregate, the
// stored version (the expected version of the next write) and a plain-language
// note for the user ("" when there is none).
type Record struct {
	cv.Verification
	Version int64
	// Note explains an Unverifiable result caused by the repo's own
	// configuration (for example a chain with too many steps). It never holds
	// a token or a path.
	Note string
	// Sent says that the failure of a Failed state was sent to the agent; it
	// is stored with the state and lost with it, so a new attempt can be sent.
	Sent      bool
	CreatedAt time.Time
	UpdatedAt time.Time
}

// State codes of the verifications.state column.
const (
	codeNone          = "none"
	codeSkipped       = "skipped"
	codeUnverifiable  = "unverifiable"
	codeBootstrapping = "bootstrapping"
	codeVerifying     = "verifying"
	codePassed        = "passed"
	codeFailed        = "failed"
	codeInterrupted   = "interrupted"
	codeCancelled     = "cancelled"

	failExit  = "exit_code"
	failStart = "start_failed"

	maxNoteRunes = 300
)

// IsLiveCode reports whether a state code is a running state.
func IsLiveCode(code string) bool { return code == codeBootstrapping || code == codeVerifying }

// StateCode returns the stored code of a state.
func StateCode(s cv.State) string {
	switch s.(type) {
	case cv.Skipped:
		return codeSkipped
	case cv.Unverifiable:
		return codeUnverifiable
	case cv.Bootstrapping:
		return codeBootstrapping
	case cv.Verifying:
		return codeVerifying
	case cv.Passed:
		return codePassed
	case cv.Failed:
		return codeFailed
	case cv.Interrupted:
		return codeInterrupted
	case cv.Cancelled:
		return codeCancelled
	}
	return codeNone
}

// stepJSON and planJSON are the stored shape of the plan column. It holds the
// turn sequence number of every state too (the table has no column for it) and
// the note. Chains are stored only while a build is live.
type stepJSON struct {
	Name string   `json:"name"`
	Argv []string `json:"argv"`
	Env  []string `json:"env,omitempty"`
}

type planJSON struct {
	Seq       int        `json:"seq"`
	Note      string     `json:"note,omitempty"`
	Sent      bool       `json:"sent,omitempty"`
	Bootstrap []stepJSON `json:"bootstrap,omitempty"`
	Verify    []stepJSON `json:"verify,omitempty"`
}

func stepsToJSON(in []cv.StepSpec) []stepJSON {
	if len(in) == 0 {
		return nil
	}
	out := make([]stepJSON, len(in))
	for i, s := range in {
		out[i] = stepJSON{Name: s.Name, Argv: append([]string(nil), s.Argv...), Env: append([]string(nil), s.Env...)}
	}
	return out
}

func stepsFromJSON(in []stepJSON) []cv.StepSpec {
	if len(in) == 0 {
		return nil
	}
	out := make([]cv.StepSpec, len(in))
	for i, s := range in {
		out[i] = cv.StepSpec{Name: s.Name, Argv: append([]string(nil), s.Argv...), Env: append([]string(nil), s.Env...)}
		if len(s.Env) == 0 {
			out[i].Env = nil
		}
	}
	return out
}

// RowFromVerification is the formatter: the row columns of a verification and
// its note. Version and the row times are the store's.
func RowFromVerification(v cv.Verification, note string) (db.VerificationRow, error) {
	return rowFromVerification(v, note, false)
}

func rowFromVerification(v cv.Verification, note string, sent bool) (db.VerificationRow, error) {
	if err := v.Validate(); err != nil {
		return db.VerificationRow{}, fmt.Errorf("verification %s: %w", v.ID, err)
	}
	if utf8.RuneCountInString(note) > maxNoteRunes || strings.ContainsFunc(note, isControl) {
		return db.VerificationRow{}, errors.New("a verification note must be at most 300 characters without control characters")
	}
	row := db.VerificationRow{
		ID: string(v.ID), Task: v.Task, Repo: v.Repo, Worktree: v.Worktree, BootHash: v.BootHash,
		State: StateCode(v.State),
	}
	if _, isFailed := v.State.(cv.Failed); sent && !isFailed {
		return db.VerificationRow{}, errors.New("only a failed verification can be marked as sent")
	}
	pj := planJSON{Note: note, Sent: sent}
	var since, at time.Time
	switch s := v.State.(type) {
	case cv.None:
		// nothing else
	case cv.Skipped:
		row.Turn, pj.Seq, row.Reason = string(s.Turn), s.TurnSeq, string(s.Reason)
	case cv.Unverifiable:
		row.Turn, pj.Seq, row.Attempt, row.Reason = string(s.Turn), s.TurnSeq, int(s.Attempt), string(s.Reason)
	case cv.Bootstrapping:
		row.Turn, pj.Seq, row.Attempt = string(s.Turn), s.TurnSeq, int(s.Attempt)
		row.Build, row.NextBuild, row.DepsHash = string(s.Build), string(s.NextBuild), s.DepsHash
		pj.Bootstrap, pj.Verify = stepsToJSON(s.Plan.Bootstrap), stepsToJSON(s.Plan.Verify)
		since = s.Since
		if s.Plan.DepsHash != s.DepsHash {
			return db.VerificationRow{}, errors.New("a bootstrapping verification's plan hash must match its dependency hash")
		}
	case cv.Verifying:
		row.Turn, pj.Seq, row.Attempt, row.Build = string(s.Turn), s.TurnSeq, int(s.Attempt), string(s.Build)
		pj.Bootstrap, pj.Verify = stepsToJSON(s.Plan.Bootstrap), stepsToJSON(s.Plan.Verify)
		row.DepsHash = s.Plan.DepsHash
		since = s.Since
	case cv.Passed:
		row.Turn, pj.Seq, row.Attempt, row.Build = string(s.Turn), s.TurnSeq, int(s.Attempt), string(s.Build)
		at = s.At
	case cv.Failed:
		row.Turn, pj.Seq, row.Attempt, row.Build = string(s.Turn), s.TurnSeq, int(s.Attempt), string(s.Build)
		row.Stage, row.FailStep, at = string(s.Stage), s.Step, s.At
		switch r := s.Reason.(type) {
		case cv.ExitCode:
			row.FailKind, row.ExitCode = failExit, r.Code
		case cv.StartFailed:
			row.FailKind, row.FailText = failStart, string(r.Failure)
		}
	case cv.Interrupted:
		row.Turn, pj.Seq, row.Attempt, row.Stage, at = string(s.Turn), s.TurnSeq, int(s.Attempt), string(s.Stage), s.At
	case cv.Cancelled:
		row.Turn, pj.Seq, row.Attempt, row.Reason, at = string(s.Turn), s.TurnSeq, int(s.Attempt), string(s.Cause), s.At
	default:
		return db.VerificationRow{}, fmt.Errorf("unknown verification state %T", v.State)
	}
	if !since.IsZero() {
		t := since.UTC()
		row.Since = &t
	}
	if !at.IsZero() {
		t := at.UTC()
		row.At = &t
	}
	raw, err := json.Marshal(pj)
	if err != nil {
		return db.VerificationRow{}, fmt.Errorf("format verification plan: %w", err)
	}
	row.Plan = raw
	return row, nil
}

func isControl(r rune) bool { return r < 0x20 || r == 0x7f }

// VerificationFromRow parses a stored row into a Record. It rejects an
// unknown state code, a live state without a build or a chain, a state that
// needs a time or an attempt and has none, and a row that carries data of
// another state (for example a passed row with a failure).
func VerificationFromRow(row db.VerificationRow) (Record, error) {
	pj, err := decodePlan(row.Plan)
	if err != nil {
		return Record{}, fmt.Errorf("verification %s: %w", row.ID, err)
	}
	v := cv.Verification{
		ID: cv.VerificationID(row.ID), Task: row.Task, Repo: row.Repo, Worktree: row.Worktree, BootHash: row.BootHash,
	}
	turn, attempt := cv.TurnID(row.Turn), cv.Attempt(row.Attempt)
	var since, at time.Time
	if row.Since != nil {
		since = row.Since.UTC()
	}
	if row.At != nil {
		at = row.At.UTC()
	}
	bad := func(format string, a ...any) (Record, error) {
		return Record{}, fmt.Errorf("verification %s: %s", row.ID, fmt.Sprintf(format, a...))
	}
	switch row.State {
	case codeNone:
		v.State = cv.None{}
	case codeSkipped:
		v.State = cv.Skipped{Turn: turn, TurnSeq: pj.Seq, Reason: cv.SkipReason(row.Reason)}
	case codeUnverifiable:
		v.State = cv.Unverifiable{Turn: turn, TurnSeq: pj.Seq, Attempt: attempt, Reason: cv.UnverifiableReason(row.Reason)}
	case codeBootstrapping:
		if row.Build == "" || row.NextBuild == "" {
			return bad("a bootstrapping verification needs both build IDs")
		}
		if len(pj.Verify) == 0 {
			// The hop to verifying needs the verify chain.
			return bad("a bootstrapping verification needs the verify chain too")
		}
		if row.Build == row.NextBuild {
			// The hop to the verify build would never be queued.
			return bad("a bootstrapping verification needs two different builds")
		}
		if since.IsZero() {
			return bad("a bootstrapping verification needs a start time")
		}
		v.State = cv.Bootstrapping{
			Turn: turn, TurnSeq: pj.Seq, Attempt: attempt, Build: core.BuildID(row.Build), NextBuild: core.BuildID(row.NextBuild),
			Plan:     cv.Plan{Bootstrap: stepsFromJSON(pj.Bootstrap), Verify: stepsFromJSON(pj.Verify), DepsHash: row.DepsHash},
			DepsHash: row.DepsHash, Since: since,
		}
	case codeVerifying:
		if row.Build == "" {
			return bad("a verifying verification needs a build ID")
		}
		if since.IsZero() {
			return bad("a verifying verification needs a start time")
		}
		v.State = cv.Verifying{
			Turn: turn, TurnSeq: pj.Seq, Attempt: attempt, Build: core.BuildID(row.Build),
			Plan:  cv.Plan{Bootstrap: stepsFromJSON(pj.Bootstrap), Verify: stepsFromJSON(pj.Verify), DepsHash: row.DepsHash},
			Since: since,
		}
	case codePassed:
		if at.IsZero() {
			return bad("a passed verification needs a time")
		}
		v.State = cv.Passed{Turn: turn, TurnSeq: pj.Seq, Attempt: attempt, Build: core.BuildID(row.Build), At: at}
	case codeFailed:
		if at.IsZero() {
			return bad("a failed verification needs a time")
		}
		var reason cv.FailReason
		switch row.FailKind {
		case failExit:
			reason = cv.ExitCode{Code: row.ExitCode}
		case failStart:
			reason = cv.StartFailed{Failure: cv.StartFailure(row.FailText)}
		default:
			return bad("unknown failure kind %q", row.FailKind)
		}
		v.State = cv.Failed{
			Turn: turn, TurnSeq: pj.Seq, Attempt: attempt, Stage: cv.Stage(row.Stage), Build: core.BuildID(row.Build),
			Step: row.FailStep, Reason: reason, At: at,
		}
	case codeInterrupted:
		if at.IsZero() {
			return bad("an interrupted verification needs a time")
		}
		v.State = cv.Interrupted{Turn: turn, TurnSeq: pj.Seq, Attempt: attempt, Stage: cv.Stage(row.Stage), At: at}
	case codeCancelled:
		if at.IsZero() {
			return bad("a cancelled verification needs a time")
		}
		v.State = cv.Cancelled{Turn: turn, TurnSeq: pj.Seq, Attempt: attempt, Cause: cv.CancelCause(row.Reason), At: at}
	default:
		return bad("unknown state %q", row.State)
	}
	if err := v.Validate(); err != nil {
		return bad("%v", err)
	}
	if !IsLiveCode(row.State) && (len(pj.Bootstrap) > 0 || len(pj.Verify) > 0) {
		return bad("the row keeps a chain although nothing is running")
	}
	// A row must hold exactly the columns of its state: format the parsed
	// state again and compare, so stray data of another state is refused.
	canon, err := rowFromVerification(v, pj.Note, pj.Sent)
	if err != nil {
		return bad("%v", err)
	}
	if !sameColumns(canon, row) {
		return bad("the row holds data that does not belong to the %s state", row.State)
	}
	return Record{Verification: v, Version: row.Version, Note: pj.Note, Sent: pj.Sent, CreatedAt: row.CreatedAt, UpdatedAt: row.UpdatedAt}, nil
}

// sameColumns compares the state columns of two rows (not version, times
// stored by the store, or the plan JSON text).
func sameColumns(a, b db.VerificationRow) bool {
	return a.ID == b.ID && a.Task == b.Task && a.Repo == b.Repo && a.Worktree == b.Worktree &&
		a.BootHash == b.BootHash && a.State == b.State && a.Turn == b.Turn && a.Attempt == b.Attempt &&
		a.Reason == b.Reason && a.Stage == b.Stage && a.Build == b.Build && a.NextBuild == b.NextBuild &&
		a.DepsHash == b.DepsHash && a.FailStep == b.FailStep && a.FailKind == b.FailKind &&
		a.ExitCode == b.ExitCode && a.FailText == b.FailText &&
		sameTime(a.Since, b.Since) && sameTime(a.At, b.At)
}

func sameTime(a, b *time.Time) bool {
	if a == nil || b == nil {
		return a == b
	}
	return a.Equal(*b)
}

func decodePlan(raw json.RawMessage) (planJSON, error) {
	var pj planJSON
	if len(bytes.TrimSpace(raw)) == 0 {
		return pj, errors.New("the stored plan is empty")
	}
	dec := json.NewDecoder(bytes.NewReader(raw))
	dec.DisallowUnknownFields()
	if err := dec.Decode(&pj); err != nil {
		return planJSON{}, fmt.Errorf("the stored plan is not valid: %w", err)
	}
	if _, err := dec.Token(); !errors.Is(err, io.EOF) {
		return planJSON{}, errors.New("the stored plan has extra data")
	}
	return pj, nil
}

// ParseReviewTurn parses the trigger row of a task idle in review.
func ParseReviewTurn(t db.ReviewTurn) (cv.TurnID, int, error) {
	id := cv.TurnID(t.Turn)
	if err := id.Validate(); err != nil {
		return "", 0, err
	}
	if t.Seq < 1 {
		return "", 0, fmt.Errorf("turn sequence %d: must be 1 or more", t.Seq)
	}
	if err := t.Task.Validate(); err != nil {
		return "", 0, err
	}
	return id, t.Seq, nil
}

// startFailureText is the plain-language text of a step that did not start.
func startFailureText(f corebuilds.StartFailure) cv.StartFailure {
	switch f {
	case corebuilds.StartNotFound:
		return "The program for this step was not found. Check that it is installed."
	case corebuilds.StartBadArgs:
		return "The command for this step is not valid."
	case corebuilds.StartDirMissing:
		return "The folder for this step does not exist."
	}
	return "The step could not be started."
}

// ToBuildResult maps a build state to a result the core accepts. ok is false
// while the build has not finished (queued, running, cancelling). Every
// Finished state, however malformed, becomes a well-formed BuildResult, so a
// verification can never wait forever for an outcome the core would drop (V13):
//
//   - a failure whose step is outside the chain, whose step shows no failure,
//     or whose exit code is 0, is reported as BuildInterrupted (the user can
//     verify again) rather than invented;
//   - a start failure gets a plain-language text from its reason.
//
// chainLen is the length of the chain the build was queued with.
func ToBuildResult(chainLen int, st corebuilds.State) (res cv.BuildResult, ok bool) {
	f, fin := st.(corebuilds.Finished)
	if !fin {
		return nil, false
	}
	switch r := f.Result.(type) {
	case corebuilds.ResultSucceeded:
		return cv.BuildPassed{}, true
	case corebuilds.ResultCancelled:
		return cv.BuildCancelled{}, true
	case corebuilds.ResultInterrupted:
		return cv.BuildInterrupted{}, true
	case corebuilds.ResultFailed:
		if r.Step < 0 || r.Step >= chainLen || r.Step >= len(f.Steps) {
			return cv.BuildInterrupted{}, true
		}
		done, isDone := f.Steps[r.Step].(corebuilds.StepDone)
		if !isDone {
			return cv.BuildInterrupted{}, true
		}
		switch o := done.Outcome.(type) {
		case corebuilds.Failed:
			if o.ExitCode == 0 {
				return cv.BuildInterrupted{}, true
			}
			return cv.BuildFailedAt{Step: r.Step, ExitCode: o.ExitCode}, true
		case corebuilds.StartFailed:
			return cv.BuildStartFailedAt{Step: r.Step, Reason: startFailureText(o.Reason)}, true
		}
		return cv.BuildInterrupted{}, true
	}
	// A finished build without a known result.
	return cv.BuildInterrupted{}, true
}

// settingsArray is the stored shape when the verify column holds the command
// array; settings objects are parsed by builds.ParseBuildSettings.
func parseVerifyOverride(raw json.RawMessage) ([]builds.Command, error) {
	trimmed := bytes.TrimSpace(raw)
	if len(trimmed) == 0 || bytes.Equal(trimmed, []byte("null")) {
		return nil, nil
	}
	switch trimmed[0] {
	case '[':
		dec := json.NewDecoder(bytes.NewReader(trimmed))
		dec.DisallowUnknownFields()
		var cmds []builds.Command
		if err := dec.Decode(&cmds); err != nil {
			return nil, fmt.Errorf("the verify command for this repo is not valid: %w", err)
		}
		if _, err := dec.Token(); !errors.Is(err, io.EOF) {
			return nil, errors.New("the verify command for this repo has extra data after the list")
		}
		return cmds, nil
	case '{':
		s, err := builds.ParseBuildSettings(trimmed)
		if err != nil {
			return nil, fmt.Errorf("the verify command for this repo is not valid: %w", err)
		}
		return s.VerifyOverride, nil
	}
	return nil, errors.New("the verify command for this repo is not valid: expected a list of commands")
}

// ParseSettings parses a stored build settings record. The verify column may
// hold the command array or a settings object (only its verify list is used);
// FullTests and Bootstrap come from their own columns. An invalid override is
// an error the caller shows as "The verify command for this repo is not valid".
func ParseSettings(rec db.BuildSettingsRecord) (builds.RepoBuildSettings, error) {
	cmds, err := parseVerifyOverride(rec.Verify)
	if err != nil {
		return builds.RepoBuildSettings{}, err
	}
	s := builds.RepoBuildSettings{VerifyOverride: cmds, FullTests: rec.FullTests, Bootstrap: rec.Bootstrap}
	if err := s.Validate(); err != nil {
		return builds.RepoBuildSettings{}, fmt.Errorf("the verify command for this repo is not valid: %w", err)
	}
	return s, nil
}

// TailLog turns the raw log bytes of a failed step into the text for
// ComposeFailure: invalid UTF-8 is replaced, secrets are redacted first, and
// the text is cut to at most limit bytes at a line boundary (a cut notice is
// the composer's job). redact may be nil only in tests.
func TailLog(raw []byte, limit int, redact func(string) string) string {
	if limit <= 0 {
		return ""
	}
	text := strings.ToValidUTF8(string(raw), "�")
	if redact != nil {
		text = redact(text)
	}
	if len(text) <= limit {
		return text
	}
	start := len(text) - limit
	if text[start-1] != '\n' {
		if nl := strings.IndexByte(text[start:], '\n'); nl >= 0 {
			start += nl + 1
		} else {
			for start < len(text) && !utf8.RuneStart(text[start]) {
				start++
			}
		}
	}
	return text[start:]
}
