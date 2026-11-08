package worktrees

import (
	"slices"

	"github.com/brendatamaar/yggdrasil/internal/core"
)

// Step is (state, input) -> (state, commands). An input that the current
// state does not wait for, or that echoes an old Seq, changes nothing.
func Step(s State, in Input) (State, []Command) {
	switch st := s.(type) {
	case Idle:
		if b, ok := in.(Begin); ok {
			r := Run{Task: b.Task, Seq: 1, Requested: slices.Clone(b.Repos)}
			return Loading{Run: r}, []Command{LoadExisting{Seq: r.Seq, Task: b.Task}}
		}
	case Loading:
		switch v := in.(type) {
		case ExistingLoaded:
			if v.Seq == st.Run.Seq {
				r := st.Run
				r.Known = activeByRepo(v.Existing)
				return advance(r)
			}
		case LoadFailed:
			if v.Seq == st.Run.Seq {
				return fail(st.Run, FailLoad, "")
			}
		}
	case Preparing:
		switch v := in.(type) {
		case Prepared:
			if v.Seq == st.Run.Seq {
				return draw(st.Run, st.Repo, v.Base, 0, nil)
			}
		case PrepareFailed:
			if v.Seq == st.Run.Seq {
				return fail(st.Run, kindOf(v.Cause), st.Repo)
			}
		}
	case Naming:
		if v, ok := in.(NameDrawn); ok && v.Seq == st.Run.Seq {
			return stepDrawn(st, v.Name)
		}
	case Probing:
		switch v := in.(type) {
		case Probed:
			if v.Seq == st.Run.Seq {
				if v.Taken {
					return retry(st.Run, st.Repo, st.Base, st.Attempt, st.Tried)
				}
				r := st.Run
				r.Seq++
				return Adding{Run: r, Repo: st.Repo, Base: st.Base, Candidate: st.Candidate}, []Command{
					AddWorktree{Seq: r.Seq, Repo: st.Repo, Name: st.Candidate, Branch: st.Candidate.Branch(), Base: st.Base},
				}
			}
		case ProbeFailed:
			if v.Seq == st.Run.Seq {
				return fail(st.Run, kindOf(v.Cause), st.Repo)
			}
		}
	case Adding:
		switch v := in.(type) {
		case Added:
			if v.Seq == st.Run.Seq {
				return stepAdded(st, v)
			}
		case AddFailed:
			if v.Seq == st.Run.Seq {
				return fail(st.Run, kindOf(v.Cause), st.Repo)
			}
		}
	case Recording:
		switch v := in.(type) {
		case Recorded:
			if v.Seq == st.Run.Seq {
				r := st.Run
				r.Placed = append(slices.Clone(r.Placed), st.Worktree)
				r.Next++
				return advance(r)
			}
		case RecordFailed:
			if v.Seq == st.Run.Seq {
				return fail(st.Run, FailRecord, st.Repo)
			}
		}
	}
	return s, nil
}

// Result reads the outcome of a finished state. done is false while commands
// are still outstanding. A Failed state returns its failure together with the
// worktrees placed before it.
func Result(s State) (placed []core.Worktree, failure *Failure, done bool) {
	switch st := s.(type) {
	case Done:
		return st.Placed, nil, true
	case Failed:
		f := st.Failure
		return st.Placed, &f, true
	}
	return nil, nil, false
}

// activeByRepo keeps the active rows, one per repo (the later row wins).
func activeByRepo(in []Known) []Known {
	var out []Known
	for _, k := range in {
		if k.Worktree.Status != core.WorktreeActive {
			continue
		}
		i := slices.IndexFunc(out, func(o Known) bool { return o.Worktree.RepoID == k.Worktree.RepoID })
		if i >= 0 {
			out[i] = k
		} else {
			out = append(out, k)
		}
	}
	return out
}

// advance places repos that need no new worktree and stops at the first that
// does. A repo asked for twice gets the worktree of its first mention.
func advance(r Run) (State, []Command) {
	for r.Next < len(r.Requested) {
		repo := r.Requested[r.Next]
		if i := slices.Index(r.Requested[:r.Next], repo); i >= 0 {
			r.Placed = append(slices.Clone(r.Placed), r.Placed[i])
			r.Next++
			continue
		}
		if i := slices.IndexFunc(r.Known, func(k Known) bool { return k.Worktree.RepoID == repo }); i >= 0 {
			if !r.Known[i].InRoot {
				return fail(r, FailOutsideRoot, repo)
			}
			r.Placed = append(slices.Clone(r.Placed), r.Known[i].Worktree)
			r.Next++
			continue
		}
		r.Seq++
		return Preparing{Run: r, Repo: repo}, []Command{PrepareRepo{Seq: r.Seq, Repo: repo}}
	}
	return Done{Placed: r.Placed}, nil
}

func draw(r Run, repo core.RepoID, base string, attempt int, tried []Name) (State, []Command) {
	r.Seq++
	return Naming{Run: r, Repo: repo, Base: base, Attempt: attempt, Tried: tried},
		[]Command{DrawName{Seq: r.Seq, Repo: repo, Attempt: attempt}}
}

// stepDrawn probes a candidate that is new; an empty or repeated one counts
// as taken, so the bound holds and a failed name is never offered again.
func stepDrawn(st Naming, n Name) (State, []Command) {
	if n == "" || slices.Contains(st.Tried, n) {
		return retry(st.Run, st.Repo, st.Base, st.Attempt, st.Tried)
	}
	tried := append(slices.Clone(st.Tried), n)
	r := st.Run
	r.Seq++
	return Probing{Run: r, Repo: st.Repo, Base: st.Base, Attempt: st.Attempt, Tried: tried, Candidate: n},
		[]Command{CheckName{Seq: r.Seq, Repo: st.Repo, Name: n, Branch: n.Branch()}}
}

// retry asks for another candidate, or gives up after MaxAttempts.
func retry(r Run, repo core.RepoID, base string, attempt int, tried []Name) (State, []Command) {
	if attempt+1 >= MaxAttempts {
		return fail(r, FailNameTaken, repo)
	}
	return draw(r, repo, base, attempt+1, tried)
}

func stepAdded(st Adding, v Added) (State, []Command) {
	r := st.Run
	r.Seq++
	w := core.Worktree{
		ID: v.ID, RepoID: st.Repo, TaskID: r.Task, Name: string(st.Candidate),
		Path: v.Path, Branch: st.Candidate.Branch(), Status: core.WorktreeActive, CreatedAt: v.At,
	}
	audit := AuditSpec{
		ID: v.AuditID, At: v.At, Actor: "system", Action: "worktree.create", Subject: "task:" + string(r.Task),
		Detail: AuditDetail{Worktree: w.ID, Repo: st.Repo, Branch: w.Branch, Base: st.Base},
	}
	return Recording{Run: r, Repo: st.Repo, Base: st.Base, Worktree: w},
		[]Command{RecordWorktree{Seq: r.Seq, Worktree: w, Base: st.Base, Audit: audit}}
}

func fail(r Run, k FailureKind, repo core.RepoID) (State, []Command) {
	return Failed{Placed: r.Placed, Failure: Failure{Kind: k, Repo: repo}}, nil
}

func kindOf(c Cause) FailureKind {
	switch c {
	case CauseUnusable:
		return FailUnusablePath
	case CauseCanceled:
		return FailCanceled
	}
	return FailGit
}
