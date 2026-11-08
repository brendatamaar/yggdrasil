package worktreeenv

import (
	"sort"

	"github.com/brendatamaar/yggdrasil/internal/core"
)

// Job is the profile-copy state of one new worktree. It is a closed set:
// JobIdle, Observing, Copying and JobDone.
type Job interface{ isJob() }

// JobIdle has not started.
type JobIdle struct{}

// Observing waits for Lstat results of every entry.
type Observing struct {
	Worktree core.WorktreeID
	Entries  []RelPath
	Seen     map[RelPath]Observation
	Trigger  Trigger
	// Extra holds the results already fixed before observing (entries beyond
	// the entry cap).
	Extra map[RelPath]Result
}

// Observation is what the shell saw for one entry.
type Observation struct {
	Source Source
	Dest   Dest
}

// Copying waits for the planned copies.
type Copying struct {
	Worktree core.WorktreeID
	Trigger  Trigger
	Pending  []RelPath
	Results  map[RelPath]Result
}

// JobDone has a result for every entry.
type JobDone struct{ Results map[RelPath]Result }

func (JobIdle) isJob()   {}
func (Observing) isJob() {}
func (Copying) isJob()   {}
func (JobDone) isJob()   {}

// Result is the closed set Copied, Skipped, CopyFailed.
type Result interface{ isResult() }

// Copied means the file was created in the worktree.
type Copied struct{}

// Skipped means the entry was left alone for Reason.
type Skipped struct{ Reason SkipReason }

// CopyFailed means the copy failed; Reason is plain language without paths.
type CopyFailed struct{ Reason string }

func (Copied) isResult()     {}
func (Skipped) isResult()    {}
func (CopyFailed) isResult() {}

// ProfileInput is a closed set: CopyRequested, Observed, FileCopied, FileCopyFailed.
type ProfileInput interface{ isProfileInput() }

// CopyRequested starts a job. AgentCopy is the per-repo toggle (off by default).
type CopyRequested struct {
	Worktree  core.WorktreeID
	Trigger   Trigger
	Entries   []RelPath
	AgentCopy bool
	Now       Now
}

// Observed carries the Lstat results for one entry.
type Observed struct {
	Entry  RelPath
	Source Source
	Dest   Dest
}

// FileCopied reports a finished copy.
type FileCopied struct{ Entry RelPath }

// FileCopyFailed reports a failed copy.
type FileCopyFailed struct {
	Entry  RelPath
	Reason string
}

func (CopyRequested) isProfileInput()  {}
func (Observed) isProfileInput()       {}
func (FileCopied) isProfileInput()     {}
func (FileCopyFailed) isProfileInput() {}

// ProfileCommand is a closed set: Observe, CopyFile, ProfileAudit, ProfilePublish.
type ProfileCommand interface{ isProfileCommand() }

// Observe asks the shell to Lstat the entry (no link followed) in the repo
// and the worktree.
type Observe struct{ Entry RelPath }

// CopyFile asks for an exclusive-create copy. Emitted only for an entry
// observed as a regular file with an absent destination (invariant P1).
type CopyFile struct{ Entry RelPath }

// ProfileAction is the closed set of audit actions the profile copy writes.
type ProfileAction string

// The closed set of ProfileAction values.
const (
	ActionProfileCopied  ProfileAction = "worktreeenv.profile_copied"
	ActionProfileSkipped ProfileAction = "worktreeenv.profile_skipped"
)

// ProfileAudit carries counts and relative names only, never contents.
// Names lists the files that were copied.
type ProfileAudit struct {
	Action   ProfileAction
	Worktree core.WorktreeID
	Trigger  Trigger
	Copied   int
	Skipped  int
	Failed   int
	Names    []RelPath
}

// ProfilePublish tells subscribers the job finished.
type ProfilePublish struct{ Worktree core.WorktreeID }

func (Observe) isProfileCommand()        {}
func (CopyFile) isProfileCommand()       {}
func (ProfileAudit) isProfileCommand()   {}
func (ProfilePublish) isProfileCommand() {}

// Profile is the profile-copy decision: (job2, commands) = Profile(job1,
// input). It never changes the maps or slices of its input.
func Profile(j Job, in ProfileInput) (Job, []ProfileCommand) {
	switch st := j.(type) {
	case JobIdle:
		if v, ok := in.(CopyRequested); ok {
			return start(v)
		}
	case Observing:
		if v, ok := in.(Observed); ok {
			return observed(st, v)
		}
	case Copying:
		switch v := in.(type) {
		case FileCopied:
			return finishEntry(st, v.Entry, Copied{})
		case FileCopyFailed:
			return finishEntry(st, v.Entry, CopyFailed{Reason: v.Reason})
		}
	}
	return j, nil
}

func start(v CopyRequested) (Job, []ProfileCommand) {
	trigger := v.Trigger
	if trigger != TriggerUserStarted && trigger != TriggerUserConfirmed {
		trigger = TriggerAgentTask // unknown values get the safe default
	}
	var entries []RelPath
	seen := map[RelPath]bool{}
	results := map[RelPath]Result{}
	for _, e := range v.Entries {
		if seen[e] {
			continue
		}
		seen[e] = true
		if len(entries) >= MaxEntries {
			results[e] = Skipped{Reason: SkipTooManyEntries}
			continue
		}
		entries = append(entries, e)
	}
	if len(seen) == 0 {
		return JobDone{Results: map[RelPath]Result{}}, nil
	}
	if trigger == TriggerAgentTask && !v.AgentCopy {
		for _, e := range entries {
			results[e] = Skipped{Reason: SkipAgentTasksOff}
		}
		return JobDone{Results: results}, finish(v.Worktree, trigger, results, ActionProfileSkipped)
	}
	cmds := make([]ProfileCommand, len(entries))
	for i, e := range entries {
		cmds[i] = Observe{Entry: e}
	}
	return Observing{Worktree: v.Worktree, Entries: entries, Seen: map[RelPath]Observation{}, Trigger: trigger, Extra: results}, cmds
}

func observed(st Observing, v Observed) (Job, []ProfileCommand) {
	if !contains(st.Entries, v.Entry) {
		return st, nil
	}
	if _, dup := st.Seen[v.Entry]; dup {
		return st, nil
	}
	seen := make(map[RelPath]Observation, len(st.Seen)+1)
	for k, o := range st.Seen {
		seen[k] = o
	}
	seen[v.Entry] = Observation{Source: v.Source, Dest: v.Dest}
	st.Seen = seen
	if len(seen) < len(st.Entries) {
		return st, nil
	}
	results := make(map[RelPath]Result, len(st.Entries)+len(st.Extra))
	for k, r := range st.Extra {
		results[k] = r
	}
	var plan []RelPath
	var total int64
	var cmds []ProfileCommand
	for _, e := range st.Entries {
		o := seen[e]
		reason, size := judge(o)
		if reason == "" && total+size > MaxTotalByte {
			reason = SkipBudgetExceeded
		}
		if reason != "" {
			results[e] = Skipped{Reason: reason}
			continue
		}
		total += size
		plan = append(plan, e)
		cmds = append(cmds, CopyFile{Entry: e})
	}
	if len(plan) == 0 {
		return JobDone{Results: results}, finish(st.Worktree, st.Trigger, results, ActionProfileCopied)
	}
	return Copying{Worktree: st.Worktree, Trigger: st.Trigger, Pending: plan, Results: results}, cmds
}

// judge says why an observed entry may not be copied ("" when it may) and the
// size it would use.
func judge(o Observation) (SkipReason, int64) {
	switch src := o.Source.(type) {
	case SourceFile:
		switch {
		case src.Size < 0:
			return SkipSourceUnreadble, 0
		case o.Dest != DestAbsent:
			return SkipDestExists, 0
		case src.Size > MaxFileBytes:
			return SkipTooLarge, 0
		}
		return "", src.Size
	case SourceMissing:
		return SkipSourceMissing, 0
	case SourceDirectory, SourceLink:
		return SkipNotRegularFile, 0
	}
	return SkipSourceUnreadble, 0
}

func finishEntry(st Copying, e RelPath, r Result) (Job, []ProfileCommand) {
	if !contains(st.Pending, e) {
		return st, nil
	}
	pending := make([]RelPath, 0, len(st.Pending))
	for _, p := range st.Pending {
		if p != e {
			pending = append(pending, p)
		}
	}
	results := make(map[RelPath]Result, len(st.Results)+1)
	for k, v := range st.Results {
		results[k] = v
	}
	results[e] = r
	if len(pending) > 0 {
		st.Pending, st.Results = pending, results
		return st, nil
	}
	return JobDone{Results: results}, finish(st.Worktree, st.Trigger, results, ActionProfileCopied)
}

// finish builds the closing audit entry and the publish command. The audit
// entry names the copied files and counts the rest; reasons and contents stay
// out of it.
func finish(w core.WorktreeID, trigger Trigger, results map[RelPath]Result, action ProfileAction) []ProfileCommand {
	a := ProfileAudit{Action: action, Worktree: w, Trigger: trigger}
	for e, r := range results {
		switch r.(type) {
		case Copied:
			a.Copied++
			a.Names = append(a.Names, e)
		case Skipped:
			a.Skipped++
		case CopyFailed:
			a.Failed++
		}
	}
	sort.Slice(a.Names, func(i, j int) bool { return a.Names[i] < a.Names[j] })
	return []ProfileCommand{a, ProfilePublish{Worktree: w}}
}

func contains(list []RelPath, e RelPath) bool {
	for _, x := range list {
		if x == e {
			return true
		}
	}
	return false
}
