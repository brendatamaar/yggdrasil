package verify_test

import (
	"encoding/json"
	"reflect"
	"strings"
	"testing"
	"testing/fstest"
	"time"
	"unicode/utf8"

	"github.com/brendatamaar/yggdrasil/internal/builds"
	corebuilds "github.com/brendatamaar/yggdrasil/internal/core/builds"
	cv "github.com/brendatamaar/yggdrasil/internal/core/verify"
	"github.com/brendatamaar/yggdrasil/internal/db"
	"github.com/brendatamaar/yggdrasil/internal/verify"
)

var at0 = time.Date(2026, 3, 1, 10, 0, 0, 0, time.UTC)

func plan() cv.Plan {
	return cv.Plan{
		Bootstrap: []cv.StepSpec{{Name: "install", Argv: []string{"npm", "ci"}, Env: []string{"CI=true"}}},
		Verify:    []cv.StepSpec{{Name: "lint", Argv: []string{"npm", "run", "lint"}}, {Name: "test", Argv: []string{"npm", "test"}}},
		DepsHash:  "h1",
	}
}

func allStates() map[string]cv.State {
	p := plan()
	return map[string]cv.State{
		"none":                 cv.None{},
		"skipped":              cv.Skipped{Turn: "t1", TurnSeq: 1, Reason: cv.SkipNoChanges},
		"skipped disabled":     cv.Skipped{Turn: "t1", TurnSeq: 2, Reason: cv.SkipDisabled},
		"unverifiable":         cv.Unverifiable{Turn: "t1", TurnSeq: 1, Attempt: 2, Reason: cv.UnverifiableNotTrusted},
		"bootstrapping":        cv.Bootstrapping{Turn: "t1", TurnSeq: 3, Attempt: 1, Build: "b1", NextBuild: "b2", Plan: p, DepsHash: "h1", Since: at0},
		"verifying":            cv.Verifying{Turn: "t1", TurnSeq: 3, Attempt: 2, Build: "b2", Plan: p, Since: at0},
		"verifying no bootstr": cv.Verifying{Turn: "t1", TurnSeq: 3, Attempt: 2, Build: "b2", Plan: cv.Plan{Verify: p.Verify, DepsHash: "h1"}, Since: at0},
		"passed":               cv.Passed{Turn: "t1", TurnSeq: 3, Attempt: 1, Build: "b2", At: at0},
		"failed exit":          cv.Failed{Turn: "t1", TurnSeq: 3, Attempt: 1, Stage: cv.StageVerify, Build: "b2", Step: 1, Reason: cv.ExitCode{Code: -1}, At: at0},
		"failed start":         cv.Failed{Turn: "t1", TurnSeq: 3, Attempt: 1, Stage: cv.StageBootstrap, Build: "b1", Step: 0, Reason: cv.StartFailed{Failure: "npm was not found"}, At: at0},
		"interrupted":          cv.Interrupted{Turn: "t1", TurnSeq: 3, Attempt: 1, Stage: cv.StageBootstrap, At: at0},
		"cancelled":            cv.Cancelled{Turn: "t1", TurnSeq: 3, Attempt: 1, Cause: cv.CancelExternal, At: at0},
	}
}

func verification(s cv.State) cv.Verification {
	return cv.Verification{ID: "ver_1", Task: "tsk_1", Repo: "rep_1", Worktree: "wt_1", BootHash: "h0", State: s}
}

func TestRowRoundTrip(t *testing.T) {
	for name, s := range allStates() {
		t.Run(name, func(t *testing.T) {
			v := verification(s)
			row, err := verify.RowFromVerification(v, "")
			if err != nil {
				t.Fatal(err)
			}
			rec, err := verify.VerificationFromRow(row)
			if err != nil {
				t.Fatalf("parse(format(x)): %v", err)
			}
			if !reflect.DeepEqual(rec.Verification, v) {
				t.Fatalf("round trip changed it:\n got  %#v\n want %#v", rec.Verification, v)
			}
			// The row survives the JSON text the database stores it as.
			var back json.RawMessage
			if err := json.Unmarshal(row.Plan, &back); err != nil {
				t.Fatalf("plan is not JSON: %v", err)
			}
		})
	}
}

func TestRowNoteRoundTrip(t *testing.T) {
	v := verification(cv.Unverifiable{Turn: "t1", TurnSeq: 1, Attempt: 1, Reason: cv.UnverifiableNoCommand})
	row, err := verify.RowFromVerification(v, verify.NoteTooManySteps)
	if err != nil {
		t.Fatal(err)
	}
	rec, err := verify.VerificationFromRow(row)
	if err != nil || rec.Note != verify.NoteTooManySteps {
		t.Fatalf("note = %q, %v", rec.Note, err)
	}
	for _, bad := range []string{strings.Repeat("x", 301), "a\nb"} {
		if _, err := verify.RowFromVerification(v, bad); err == nil {
			t.Errorf("note %q accepted", bad)
		}
	}
}

func rowOf(t *testing.T, s cv.State) db.VerificationRow {
	t.Helper()
	row, err := verify.RowFromVerification(verification(s), "")
	if err != nil {
		t.Fatal(err)
	}
	return row
}

// One test per rejection rule of VerificationFromRow.
func TestVerificationFromRowRejects(t *testing.T) {
	states := allStates()
	cases := []struct {
		name string
		base string
		edit func(r *db.VerificationRow)
	}{
		{"unknown state code", "passed", func(r *db.VerificationRow) { r.State = "dancing" }},
		{"empty state code", "passed", func(r *db.VerificationRow) { r.State = "" }},
		{"live without a build", "verifying", func(r *db.VerificationRow) { r.Build = "" }},
		{"bootstrapping without the next build", "bootstrapping", func(r *db.VerificationRow) { r.NextBuild = "" }},
		{"bootstrapping with the same two builds", "bootstrapping", func(r *db.VerificationRow) { r.NextBuild = r.Build }},
		{"live without a start time", "verifying", func(r *db.VerificationRow) { r.Since = nil }},
		{"bootstrapping without a chain", "bootstrapping", func(r *db.VerificationRow) { r.Plan = json.RawMessage(`{"seq":3}`) }},
		{"bootstrapping without the verify chain", "bootstrapping", func(r *db.VerificationRow) {
			r.Plan = json.RawMessage(`{"seq":3,"bootstrap":[{"name":"i","argv":["npm"]}]}`)
		}},
		{"verifying without a chain", "verifying", func(r *db.VerificationRow) { r.Plan = json.RawMessage(`{"seq":3}`) }},
		{"passed without a time", "passed", func(r *db.VerificationRow) { r.At = nil }},
		{"passed without a build", "passed", func(r *db.VerificationRow) { r.Build = "" }},
		{"failed without a time", "failed exit", func(r *db.VerificationRow) { r.At = nil }},
		{"failed with an unknown kind", "failed exit", func(r *db.VerificationRow) { r.FailKind = "melted" }},
		{"failed with no kind", "failed exit", func(r *db.VerificationRow) { r.FailKind = "" }},
		{"failed with exit code 0", "failed exit", func(r *db.VerificationRow) { r.ExitCode = 0 }},
		{"failed start with no text", "failed start", func(r *db.VerificationRow) { r.FailText = "" }},
		{"failed with a bad stage", "failed exit", func(r *db.VerificationRow) { r.Stage = "deploy" }},
		{"interrupted without a stage", "interrupted", func(r *db.VerificationRow) { r.Stage = "" }},
		{"cancelled with an unknown cause", "cancelled", func(r *db.VerificationRow) { r.Reason = "ghost" }},
		{"skipped with an unknown reason", "skipped", func(r *db.VerificationRow) { r.Reason = "bored" }},
		{"unverifiable with attempt 0", "unverifiable", func(r *db.VerificationRow) { r.Attempt = 0 }},
		{"attempt 0 on a running state", "verifying", func(r *db.VerificationRow) { r.Attempt = 0 }},
		{"a turn sequence of 0", "passed", func(r *db.VerificationRow) { r.Plan = json.RawMessage(`{"seq":0}`) }},
		{"a bad turn id", "passed", func(r *db.VerificationRow) { r.Turn = "has space" }},
		{"an empty turn id", "passed", func(r *db.VerificationRow) { r.Turn = "" }},
		{"a plan with an unknown field", "passed", func(r *db.VerificationRow) { r.Plan = json.RawMessage(`{"seq":3,"surprise":1}`) }},
		{"a plan with trailing data", "passed", func(r *db.VerificationRow) { r.Plan = json.RawMessage(`{"seq":3} {}`) }},
		{"a plan that is not JSON", "passed", func(r *db.VerificationRow) { r.Plan = json.RawMessage(`nope`) }},
		{"an empty plan", "passed", func(r *db.VerificationRow) { r.Plan = nil }},
		{"a step with no command", "verifying", func(r *db.VerificationRow) {
			r.Plan = json.RawMessage(`{"seq":3,"verify":[{"name":"x","argv":[]}]}`)
		}},
		{"stray failure data on a passed row", "passed", func(r *db.VerificationRow) { r.FailText = "x" }},
		{"stray build on a skipped row", "skipped", func(r *db.VerificationRow) { r.Build = "b1" }},
		{"stray chain on a passed row", "passed", func(r *db.VerificationRow) {
			r.Plan = json.RawMessage(`{"seq":3,"verify":[{"name":"x","argv":["a"]}]}`)
		}},
		{"stray time on a skipped row", "skipped", func(r *db.VerificationRow) { r.At = &at0 }},
		{"none with a turn", "none", func(r *db.VerificationRow) { r.Turn = "t1" }},
		{"none with an attempt", "none", func(r *db.VerificationRow) { r.Attempt = 1 }},
		{"a bad identity", "passed", func(r *db.VerificationRow) { r.Task = "" }},
	}
	for _, c := range cases {
		t.Run(c.name, func(t *testing.T) {
			row := rowOf(t, states[c.base])
			if _, err := verify.VerificationFromRow(row); err != nil {
				t.Fatalf("the unedited %s row does not parse: %v", c.base, err)
			}
			c.edit(&row)
			if _, err := verify.VerificationFromRow(row); err == nil {
				t.Fatalf("accepted: %+v", row)
			}
		})
	}
}

// accepted => valid: whatever the parser accepts is a valid verification that
// formats back to the same columns.
func checkAccepted(t *testing.T, row db.VerificationRow) {
	t.Helper()
	rec, err := verify.VerificationFromRow(row)
	if err != nil {
		return
	}
	if err := rec.Validate(); err != nil {
		t.Fatalf("accepted an invalid verification: %v\nrow %+v", err, row)
	}
	again, err := verify.RowFromVerification(rec.Verification, rec.Note)
	if err != nil {
		t.Fatalf("an accepted row does not format: %v", err)
	}
	back, err := verify.VerificationFromRow(again)
	if err != nil || !reflect.DeepEqual(back.Verification, rec.Verification) {
		t.Fatalf("an accepted row is not stable: %v", err)
	}
}

func FuzzVerificationFromRow(f *testing.F) {
	for _, s := range allStates() {
		v := verification(s)
		row, _ := verify.RowFromVerification(v, "")
		since, atT := int64(0), int64(0)
		if row.Since != nil {
			since = row.Since.UnixMilli()
		}
		if row.At != nil {
			atT = row.At.UnixMilli()
		}
		f.Add(row.State, row.Turn, row.Attempt, row.Reason, row.Stage, row.Build, row.NextBuild, row.DepsHash,
			row.FailStep, row.FailKind, row.ExitCode, row.FailText, string(row.Plan), since, atT)
	}
	f.Fuzz(func(t *testing.T, state, turn string, attempt int, reason, stage, build, next, deps string,
		failStep int, kind string, exitCode int, failText, plan string, since, atMs int64) {
		row := db.VerificationRow{
			ID: "ver_1", Task: "tsk_1", Repo: "rep_1", Worktree: "wt_1", State: state, Turn: turn, Attempt: attempt,
			Reason: reason, Stage: stage, Build: build, NextBuild: next, DepsHash: deps, FailStep: failStep,
			FailKind: kind, ExitCode: exitCode, FailText: failText, Plan: json.RawMessage(plan),
		}
		if since != 0 {
			s := time.UnixMilli(since).UTC()
			row.Since = &s
		}
		if atMs != 0 {
			a := time.UnixMilli(atMs).UTC()
			row.At = &a
		}
		checkAccepted(t, row)
	})
}

func TestBuildResultMapping(t *testing.T) {
	at := at0
	done := func(o corebuilds.Outcome) []corebuilds.StepState {
		return []corebuilds.StepState{corebuilds.StepDone{Attempt: 1, Outcome: corebuilds.Succeeded{}}, corebuilds.StepDone{Attempt: 1, Outcome: o}, corebuilds.StepSkipped{}}
	}
	fin := func(r corebuilds.Result, steps []corebuilds.StepState) corebuilds.State {
		return corebuilds.Finished{ID: "b1", Result: r, Steps: steps, StartedAt: &at, FinishedAt: at}
	}
	cases := []struct {
		name string
		st   corebuilds.State
		want cv.BuildResult
		ok   bool
	}{
		{"succeeded", fin(corebuilds.ResultSucceeded{}, nil), cv.BuildPassed{}, true},
		{"cancelled", fin(corebuilds.ResultCancelled{}, nil), cv.BuildCancelled{}, true},
		{"interrupted", fin(corebuilds.ResultInterrupted{Step: 1}, nil), cv.BuildInterrupted{}, true},
		{"failed with a code", fin(corebuilds.ResultFailed{Step: 1}, done(corebuilds.Failed{ExitCode: 4})), cv.BuildFailedAt{Step: 1, ExitCode: 4}, true},
		{"failed with a negative code", fin(corebuilds.ResultFailed{Step: 1}, done(corebuilds.Failed{ExitCode: -1})), cv.BuildFailedAt{Step: 1, ExitCode: -1}, true},
		{"start failed", fin(corebuilds.ResultFailed{Step: 1}, done(corebuilds.StartFailed{Reason: corebuilds.StartNotFound})),
			cv.BuildStartFailedAt{Step: 1, Reason: "The program for this step was not found. Check that it is installed."}, true},
		{"step beyond the chain", fin(corebuilds.ResultFailed{Step: 3}, done(corebuilds.Failed{ExitCode: 4})), cv.BuildInterrupted{}, true},
		{"negative step", fin(corebuilds.ResultFailed{Step: -1}, done(corebuilds.Failed{ExitCode: 4})), cv.BuildInterrupted{}, true},
		{"exit code 0", fin(corebuilds.ResultFailed{Step: 1}, done(corebuilds.Failed{ExitCode: 0})), cv.BuildInterrupted{}, true},
		{"step that did not fail", fin(corebuilds.ResultFailed{Step: 1}, done(corebuilds.Succeeded{})), cv.BuildInterrupted{}, true},
		{"step still pending", fin(corebuilds.ResultFailed{Step: 2}, done(corebuilds.Failed{ExitCode: 4})), cv.BuildInterrupted{}, true},
		{"no result", fin(nil, nil), cv.BuildInterrupted{}, true},
		{"queued", corebuilds.Queued{ID: "b1"}, nil, false},
		{"running", corebuilds.Running{ID: "b1"}, nil, false},
		{"cancelling", corebuilds.Cancelling{}, nil, false},
		{"none", corebuilds.None{}, nil, false},
		{"nil", nil, nil, false},
	}
	for _, c := range cases {
		t.Run(c.name, func(t *testing.T) {
			got, ok := verify.ToBuildResult(3, c.st)
			if ok != c.ok || got != c.want {
				t.Fatalf("got %#v ok=%v, want %#v ok=%v", got, ok, c.want, c.ok)
			}
		})
	}
}

// Whatever a finished build looks like, the result is one the core accepts:
// failures are inside the chain with a non-zero code or a valid start text.
func FuzzToBuildResult(f *testing.F) {
	f.Add(0, 0, 0, 3)
	f.Add(1, 2, 5, 3)
	f.Add(1, -4, -1, 2)
	f.Fuzz(func(t *testing.T, kind, step, code, chainLen int) {
		var res corebuilds.Result
		switch kind % 5 {
		case 0:
			res = corebuilds.ResultSucceeded{}
		case 1:
			res = corebuilds.ResultFailed{Step: step}
		case 2:
			res = corebuilds.ResultCancelled{}
		case 3:
			res = corebuilds.ResultInterrupted{Step: step}
		}
		var steps []corebuilds.StepState
		for i := 0; i < 4; i++ {
			if i == step {
				steps = append(steps, corebuilds.StepDone{Outcome: corebuilds.Failed{ExitCode: code}})
			} else {
				steps = append(steps, corebuilds.StepDone{Outcome: corebuilds.Succeeded{}})
			}
		}
		got, ok := verify.ToBuildResult(chainLen, corebuilds.Finished{Result: res, Steps: steps})
		if !ok || got == nil {
			t.Fatal("a finished build gave no result")
		}
		switch r := got.(type) {
		case cv.BuildFailedAt:
			if r.Step < 0 || r.Step >= chainLen || r.ExitCode == 0 {
				t.Fatalf("malformed failure %+v for chain %d", r, chainLen)
			}
		case cv.BuildStartFailedAt:
			if r.Step < 0 || r.Step >= chainLen || r.Reason.Validate() != nil {
				t.Fatalf("malformed start failure %+v", r)
			}
		}
	})
}

func TestTailLog(t *testing.T) {
	redact := func(s string) string { return strings.ReplaceAll(s, "SECRET", "[REDACTED]") }
	t.Run("short logs are kept whole", func(t *testing.T) {
		if got := verify.TailLog([]byte("a\nb\n"), 100, nil); got != "a\nb\n" {
			t.Fatalf("got %q", got)
		}
	})
	t.Run("invalid UTF-8 is replaced", func(t *testing.T) {
		got := verify.TailLog([]byte("ok \xff\xfe end"), 100, nil)
		if !utf8.ValidString(got) || !strings.Contains(got, "�") {
			t.Fatalf("got %q", got)
		}
	})
	t.Run("secrets are redacted before the cut", func(t *testing.T) {
		got := verify.TailLog([]byte("line1\ntoken SECRET here\nline3\n"), 100, redact)
		if strings.Contains(got, "SECRET") || !strings.Contains(got, "[REDACTED]") {
			t.Fatalf("got %q", got)
		}
	})
	t.Run("over the cap is cut at a line boundary", func(t *testing.T) {
		got := verify.TailLog([]byte("aaaa\nbbbb\ncccc\n"), 8, nil)
		if got != "cccc\n" {
			t.Fatalf("got %q", got)
		}
	})
	t.Run("one huge line is cut at a rune boundary", func(t *testing.T) {
		got := verify.TailLog([]byte(strings.Repeat("é", 50)), 11, nil)
		if len(got) > 11 || !utf8.ValidString(got) {
			t.Fatalf("got %q (%d bytes)", got, len(got))
		}
	})
	t.Run("a zero cap gives nothing", func(t *testing.T) {
		if got := verify.TailLog([]byte("abc"), 0, nil); got != "" {
			t.Fatalf("got %q", got)
		}
	})
}

func FuzzTailLog(f *testing.F) {
	f.Add([]byte("a\nb\n"), 3)
	f.Add([]byte("\xff\xfe\n\n"), 1)
	f.Fuzz(func(t *testing.T, raw []byte, limit int) {
		if limit > 1<<16 {
			limit = 1 << 16
		}
		got := verify.TailLog(raw, limit, nil)
		if limit <= 0 {
			limit = 0
		}
		if len(got) > limit && limit > 0 {
			// ToValidUTF8 may grow the text (each bad byte becomes 3), but the cut
			// is applied after it, so the result still fits.
			t.Fatalf("%d bytes over the cap %d", len(got), limit)
		}
		if !utf8.ValidString(got) {
			t.Fatalf("invalid UTF-8 in %q", got)
		}
	})
}

func TestParseSettings(t *testing.T) {
	rec := func(verifyJSON string) db.BuildSettingsRecord {
		return db.BuildSettingsRecord{Repo: "rep_1", Verify: json.RawMessage(verifyJSON), FullTests: true, Bootstrap: true}
	}
	t.Run("empty means default", func(t *testing.T) {
		s, err := verify.ParseSettings(rec(""))
		if err != nil || len(s.VerifyOverride) != 0 || !s.FullTests || !s.Bootstrap {
			t.Fatalf("%+v %v", s, err)
		}
	})
	t.Run("a command array", func(t *testing.T) {
		s, err := verify.ParseSettings(rec(`[{"name":"lint","argv":["npm","run","lint"]}]`))
		if err != nil || len(s.VerifyOverride) != 1 || s.VerifyOverride[0].Name != "lint" {
			t.Fatalf("%+v %v", s, err)
		}
	})
	t.Run("a settings object takes only its verify list", func(t *testing.T) {
		s, err := verify.ParseSettings(db.BuildSettingsRecord{Repo: "rep_1", FullTests: false, Bootstrap: false,
			Verify: json.RawMessage(`{"verify":[{"name":"a","argv":["x"]}],"fullTests":true,"bootstrap":true}`)})
		if err != nil || len(s.VerifyOverride) != 1 || s.FullTests || s.Bootstrap {
			t.Fatalf("%+v %v", s, err)
		}
	})
	steps := strings.Repeat(`{"name":"s","argv":["x"]},`, 65)
	rejects := map[string]string{
		"unknown field":      `[{"name":"a","argv":["x"],"shell":true}]`,
		"empty argv":         `[{"name":"a","argv":[]}]`,
		"empty program":      `[{"name":"a","argv":[""]}]`,
		"NUL in argv":        `[{"name":"a","argv":["x\u0000y"]}]`,
		"empty name":         `[{"name":" ","argv":["x"]}]`,
		"too many steps":     `[` + strings.TrimSuffix(steps, ",") + `]`,
		"trailing data":      `[{"name":"a","argv":["x"]}] []`,
		"not a list":         `"npm test"`,
		"a number":           `7`,
		"a batch metachar":   `[{"name":"a","argv":["x.cmd","a&b"]}]`,
		"object bad inside":  `{"verify":[{"name":"a","argv":[]}]}`,
		"object unknown key": `{"verify":[],"extra":1}`,
		"broken JSON":        `[{`,
	}
	for name, body := range rejects {
		t.Run("rejects "+name, func(t *testing.T) {
			if _, err := verify.ParseSettings(rec(body)); err == nil {
				t.Fatalf("accepted %s", body)
			}
		})
	}
}

func FuzzParseSettings(f *testing.F) {
	f.Add(`[{"name":"a","argv":["x"]}]`)
	f.Add(`{"verify":[]}`)
	f.Add(``)
	f.Fuzz(func(t *testing.T, body string) {
		s, err := verify.ParseSettings(db.BuildSettingsRecord{Repo: "rep_1", Verify: json.RawMessage(body), Bootstrap: true})
		if err != nil {
			return
		}
		if err := s.Validate(); err != nil {
			t.Fatalf("accepted invalid settings: %v", err)
		}
	})
}

func TestParseReviewTurn(t *testing.T) {
	if id, seq, err := verify.ParseReviewTurn(db.ReviewTurn{Task: "tsk_1", Turn: "t1", Seq: 2}); err != nil || id != "t1" || seq != 2 {
		t.Fatalf("%v %d %v", id, seq, err)
	}
	for name, bad := range map[string]db.ReviewTurn{
		"empty turn":   {Task: "tsk_1", Turn: "", Seq: 1},
		"spaced turn":  {Task: "tsk_1", Turn: "a b", Seq: 1},
		"seq 0":        {Task: "tsk_1", Turn: "t1", Seq: 0},
		"negative seq": {Task: "tsk_1", Turn: "t1", Seq: -1},
		"empty task":   {Task: "", Turn: "t1", Seq: 1},
	} {
		if _, _, err := verify.ParseReviewTurn(bad); err == nil {
			t.Errorf("%s accepted", name)
		}
	}
}

func TestResolvePlan(t *testing.T) {
	repoFS := newRig(t).files
	tc := []fakeTC{{
		bootstrap: stepsOf(1), verify: stepsOf(2), fullTests: stepsOf(3),
	}}
	use := func(tcs ...fakeTC) []builds.Toolchain {
		var out []builds.Toolchain
		for i := range tcs {
			out = append(out, tcRef{&tcs[i]})
		}
		return out
	}
	t.Run("defaults", func(t *testing.T) {
		p, note := verify.ResolvePlan(repoFS, use(tc...), builds.DefaultBuildSettings())
		if note != "" || len(p.Bootstrap) != 1 || len(p.Verify) != 2 || p.DepsHash == "" {
			t.Fatalf("%+v %q", p, note)
		}
	})
	t.Run("full tests are appended", func(t *testing.T) {
		s := builds.DefaultBuildSettings()
		s.FullTests = true
		p, _ := verify.ResolvePlan(repoFS, use(tc...), s)
		if len(p.Verify) != 5 {
			t.Fatalf("%d steps", len(p.Verify))
		}
	})
	t.Run("bootstrap off", func(t *testing.T) {
		s := builds.DefaultBuildSettings()
		s.Bootstrap = false
		p, _ := verify.ResolvePlan(repoFS, use(tc...), s)
		if len(p.Bootstrap) != 0 {
			t.Fatal("bootstrap kept")
		}
	})
	t.Run("a chain over the limit", func(t *testing.T) {
		big := []fakeTC{{verify: stepsOf(cv.MaxSteps), fullTests: stepsOf(1)}}
		s := builds.DefaultBuildSettings()
		s.FullTests = true
		p, note := verify.ResolvePlan(repoFS, use(big...), s)
		if note != verify.NoteTooManySteps || len(p.Verify) != 0 {
			t.Fatalf("%+v %q", p, note)
		}
		// Exactly at the limit is fine.
		s.FullTests = false
		if p, note := verify.ResolvePlan(repoFS, use(big...), s); note != "" || len(p.Verify) != cv.MaxSteps {
			t.Fatalf("%d steps, note %q", len(p.Verify), note)
		}
	})
	t.Run("a step the builds service would refuse", func(t *testing.T) {
		bad := []fakeTC{{verify: []builds.Step{{Name: "x", Argv: []string{"a"}, Env: []string{"NOT AN ENV"}}}}}
		if _, note := verify.ResolvePlan(repoFS, use(bad...), builds.DefaultBuildSettings()); note != verify.NoteInvalidStep {
			t.Fatalf("note = %q", note)
		}
	})
	t.Run("nothing detected", func(t *testing.T) {
		if _, note := verify.ResolvePlan(fstest.MapFS{}, use(tc...), builds.DefaultBuildSettings()); note != verify.NoteNoToolchain {
			t.Fatalf("note = %q", note)
		}
	})
	t.Run("the dependency hash follows the lockfile", func(t *testing.T) {
		a, _ := verify.ResolvePlan(repoFS, use(tc...), builds.DefaultBuildSettings())
		files := newRig(t).files
		files["deps.lock"].Data = []byte("changed")
		b, _ := verify.ResolvePlan(files, use(tc...), builds.DefaultBuildSettings())
		if a.DepsHash == b.DepsHash {
			t.Fatal("hash did not change")
		}
	})
}
