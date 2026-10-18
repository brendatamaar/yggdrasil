package repos

import (
	"fmt"
	"sort"
	"time"

	"github.com/brendatamaar/yggdrasil/internal/core"
)

// Defaults of the status refresher (docs/design/repo-status.md).
const (
	// DefaultMax is the number of concurrent reads.
	DefaultMax = 8
	// TickEvery is the interval of the shell's Tick ticker.
	TickEvery = 30 * time.Second
	// MinAgeFocus and MinAgeTick: focus and tick requests skip repos whose
	// view is younger than this. Other reasons always enqueue.
	MinAgeFocus = 15 * time.Second
	MinAgeTick  = 2 * time.Minute
	// ReadTimeout is the shell's limit for one read.
	ReadTimeout = 15 * time.Second
)

// Refresher is the state of the status scheduler: the read model plus the
// queue of repos waiting to be read, with bounded concurrency.
type Refresher struct {
	Entries map[core.RepoID]Entry
	// Queue holds each ID at most once.
	Queue []core.RepoID
	// InFlight maps a repo to the attempt being read.
	InFlight map[core.RepoID]int
	// Orphans maps a repo to a read whose result is no longer wanted (the repo
	// was removed or moved, or the state was restarted). An orphan still counts
	// toward Max, because the shell's worker is still running, until its result
	// arrives; the result frees the slot and is otherwise ignored. A repo with
	// an orphan is not started again until the orphan has ended.
	Orphans map[core.RepoID]int
	// Seq is the last attempt number handed out. Every start takes the next
	// one, so an attempt is unique across IDs and across remove and re-add.
	Seq int
	// Max is the number of concurrent reads.
	Max int
}

// Entry is one repo's status.
type Entry struct {
	Path CleanPath
	// Attempt is the last attempt started (a Seq value).
	Attempt int
	View    View
}

// RefreshReason says why a refresh was requested.
type RefreshReason string

const (
	ReasonUser      RefreshReason = "user"
	ReasonFocus     RefreshReason = "focus"
	ReasonGitAction RefreshReason = "git_action"
	ReasonBuild     RefreshReason = "build"
	ReasonTracking  RefreshReason = "tracking"
	ReasonTick      RefreshReason = "tick"
)

// Validate reports whether r is a known reason.
func (r RefreshReason) Validate() error {
	switch r {
	case ReasonUser, ReasonFocus, ReasonGitAction, ReasonBuild, ReasonTracking, ReasonTick:
		return nil
	}
	return fmt.Errorf("unknown refresh reason %q", string(r))
}

// RefreshInput is one event for Refresh. It is a closed set.
type RefreshInput interface{ isRefreshInput() }

// RepoRef is an active repo and where it lives.
type RepoRef struct {
	ID   core.RepoID
	Path CleanPath
}

// ReposChanged carries the full active set (from tracking, and at startup).
type ReposChanged struct{ Active []RepoRef }

// RefreshRequested asks to read repos; empty IDs means all.
type RefreshRequested struct {
	IDs    []core.RepoID
	Reason RefreshReason
	Now    time.Time
}

// Tick is the shell ticker.
type Tick struct{ Now time.Time }

// StatusRead is a successful read.
type StatusRead struct {
	ID      core.RepoID
	Attempt int
	Snap    Snapshot
	Now     time.Time
}

// StatusFailed is a failed read.
type StatusFailed struct {
	ID      core.RepoID
	Attempt int
	Kind    ProblemKind
	Now     time.Time
}

func (ReposChanged) isRefreshInput()     {}
func (RefreshRequested) isRefreshInput() {}
func (Tick) isRefreshInput()             {}
func (StatusRead) isRefreshInput()       {}
func (StatusFailed) isRefreshInput()     {}

// RefreshCommand is an effect Refresh asks for. It is a closed set.
type RefreshCommand interface{ isRefreshCommand() }

// ReadStatus reads one repo: read-only git, deduplicated by (ID, Attempt).
type ReadStatus struct {
	ID      core.RepoID
	Path    CleanPath
	Attempt int
}

// PublishStatus tells subscribers that one repo's view changed.
type PublishStatus struct{ ID core.RepoID }

// PublishCounts tells subscribers that the counts changed.
type PublishCounts struct{}

func (ReadStatus) isRefreshCommand()    {}
func (PublishStatus) isRefreshCommand() {}
func (PublishCounts) isRefreshCommand() {}

// NewRefresher returns an empty Refresher reading at most max repos at once
// (DefaultMax when max < 1).
func NewRefresher(max int) Refresher {
	if max < 1 {
		max = DefaultMax
	}
	return Refresher{Entries: map[core.RepoID]Entry{}, InFlight: map[core.RepoID]int{}, Orphans: map[core.RepoID]int{}, Max: max}
}

// Restarted returns an empty Refresher for the same Max that keeps Seq and
// treats every read of r (in flight or orphaned) as an orphan, so attempts
// stay unique and the bound on concurrent reads still holds. The shell uses it
// to rebuild the state after a panic in Refresh.
func (r Refresher) Restarted() Refresher {
	n := NewRefresher(r.Max)
	n.Seq = r.Seq
	for id, a := range r.Orphans {
		n.Orphans[id] = a
	}
	for id, a := range r.InFlight {
		n.Orphans[id] = a
	}
	return n
}

// reads is the number of reads the shell may still be running.
func (r Refresher) reads() int { return len(r.InFlight) + len(r.Orphans) }

// reading reports whether a read of id is running, wanted or not.
func (r Refresher) reading(id core.RepoID) bool {
	_, a := r.InFlight[id]
	_, b := r.Orphans[id]
	return a || b
}

func (r Refresher) maxReads() int {
	if r.Max < 1 {
		return DefaultMax
	}
	return r.Max
}

// clone copies the containers, so Refresh never changes the state it was given.
func (r Refresher) clone() Refresher {
	n := Refresher{
		Entries:  make(map[core.RepoID]Entry, len(r.Entries)),
		Queue:    append([]core.RepoID(nil), r.Queue...),
		InFlight: make(map[core.RepoID]int, len(r.InFlight)),
		Orphans:  make(map[core.RepoID]int, len(r.Orphans)),
		Seq:      r.Seq,
		Max:      r.Max,
	}
	for k, v := range r.Orphans {
		n.Orphans[k] = v
	}
	for k, v := range r.Entries {
		n.Entries[k] = v
	}
	for k, v := range r.InFlight {
		n.InFlight[k] = v
	}
	return n
}

func (r Refresher) queued(id core.RepoID) bool {
	for _, q := range r.Queue {
		if q == id {
			return true
		}
	}
	return false
}

// Refresh is the status scheduler step: (state2, commands) =
// Refresh(state1, input). An input that doesn't apply (an unknown ID, a stale
// attempt) returns the state unchanged and no commands.
func Refresh(r Refresher, in RefreshInput) (Refresher, []RefreshCommand) {
	before := CountEntries(r.Entries, nil)
	n := r.clone()
	var cmds []RefreshCommand
	switch x := in.(type) {
	case ReposChanged:
		cmds = n.reposChanged(x)
	case RefreshRequested:
		n.request(x.IDs, x.Reason, x.Now)
	case Tick:
		n.request(nil, ReasonTick, x.Now)
	case StatusRead:
		var ok bool
		cmds, ok = n.finish(x.ID, x.Attempt, func(View) View { return Fresh{Snap: x.Snap, At: x.Now} })
		if !ok {
			return r, nil
		}
	case StatusFailed:
		var ok bool
		cmds, ok = n.finish(x.ID, x.Attempt, func(prev View) View {
			var last *Snapshot
			switch p := prev.(type) {
			case Fresh:
				s := p.Snap
				last = &s
			case Problem:
				last = p.Last
			}
			return Problem{Kind: x.Kind, At: x.Now, Last: last}
		})
		if !ok {
			return r, nil
		}
	default:
		return r, nil
	}
	cmds = append(cmds, n.startReads()...)
	if CountEntries(n.Entries, nil) != before {
		cmds = append(cmds, PublishCounts{})
	}
	return n, cmds
}

func (n *Refresher) reposChanged(x ReposChanged) []RefreshCommand {
	var cmds []RefreshCommand
	active := make(map[core.RepoID]struct{}, len(x.Active))
	for _, ref := range x.Active {
		active[ref.ID] = struct{}{}
		e, known := n.Entries[ref.ID]
		switch {
		case !known:
			n.Entries[ref.ID] = Entry{Path: ref.Path, View: Unknown{}}
			n.enqueue(ref.ID, false)
		case e.Path != ref.Path:
			// The folder moved: the read in flight is stale, so it becomes an
			// orphan and the repo starts over.
			n.orphan(ref.ID)
			n.Entries[ref.ID] = Entry{Path: ref.Path, Attempt: e.Attempt, View: Unknown{}}
			n.enqueue(ref.ID, false)
			cmds = append(cmds, PublishStatus{ID: ref.ID})
		}
	}
	var gone []core.RepoID
	for id := range n.Entries {
		if _, ok := active[id]; !ok {
			gone = append(gone, id)
		}
	}
	sort.Slice(gone, func(i, j int) bool { return gone[i] < gone[j] })
	for _, id := range gone {
		delete(n.Entries, id)
		n.orphan(id)
		n.dequeue(id)
		cmds = append(cmds, PublishStatus{ID: id})
	}
	return cmds
}

// orphan turns the read in flight of id, if any, into an orphan.
func (n *Refresher) orphan(id core.RepoID) {
	if a, busy := n.InFlight[id]; busy {
		n.Orphans[id] = a
		delete(n.InFlight, id)
	}
}

func (n *Refresher) enqueue(id core.RepoID, front bool) {
	if n.queued(id) {
		return
	}
	if front {
		n.Queue = append([]core.RepoID{id}, n.Queue...)
		return
	}
	n.Queue = append(n.Queue, id)
}

func (n *Refresher) dequeue(id core.RepoID) {
	out := make([]core.RepoID, 0, len(n.Queue))
	for _, q := range n.Queue {
		if q != id {
			out = append(out, q)
		}
	}
	n.Queue = out
}

// request enqueues the repos named (all when ids is empty). user and
// git_action requests go to the front, in the order given.
func (n *Refresher) request(ids []core.RepoID, reason RefreshReason, now time.Time) {
	if len(ids) == 0 {
		ids = make([]core.RepoID, 0, len(n.Entries))
		for id := range n.Entries {
			ids = append(ids, id)
		}
		sort.Slice(ids, func(i, j int) bool { return ids[i] < ids[j] })
	}
	var minAge time.Duration
	switch reason {
	case ReasonFocus:
		minAge = MinAgeFocus
	case ReasonTick:
		minAge = MinAgeTick
	}
	front := reason == ReasonUser || reason == ReasonGitAction
	var picked []core.RepoID
	seen := map[core.RepoID]struct{}{}
	for _, id := range ids {
		e, ok := n.Entries[id]
		_, dup := seen[id]
		if !ok || dup || (n.queued(id) && !front) {
			continue
		}
		seen[id] = struct{}{}
		if minAge > 0 {
			// A repo being read right now is as fresh as it gets.
			if _, busy := n.InFlight[id]; busy {
				continue
			}
			if at, has := ViewAt(e.View); has && now.Sub(at) < minAge {
				continue
			}
		}
		picked = append(picked, id)
	}
	if front {
		// A repo already waiting moves to the front: the user is looking at it.
		for i := len(picked) - 1; i >= 0; i-- {
			n.dequeue(picked[i])
			n.enqueue(picked[i], true)
		}
		return
	}
	for _, id := range picked {
		n.enqueue(id, false)
	}
}

// finish applies a result when its attempt is the one in flight. The result of
// an orphan only frees its slot. ok is false for an unknown ID or a stale
// attempt.
func (n *Refresher) finish(id core.RepoID, attempt int, next func(View) View) (cmds []RefreshCommand, ok bool) {
	if a, orphaned := n.Orphans[id]; orphaned && a == attempt {
		delete(n.Orphans, id)
		return nil, true
	}
	e, known := n.Entries[id]
	if !known {
		return nil, false
	}
	if a, busy := n.InFlight[id]; !busy || a != attempt {
		return nil, false
	}
	delete(n.InFlight, id)
	nv := next(e.View)
	changed := !viewsEqual(e.View, nv)
	e.View = nv
	n.Entries[id] = e
	if changed {
		cmds = append(cmds, PublishStatus{ID: id})
	}
	return cmds, true
}

// startReads moves queued repos into flight while there is room (orphans take
// room too). A queued repo that is being read, wanted or not, waits for that
// read to end.
func (n *Refresher) startReads() []RefreshCommand {
	var cmds []RefreshCommand
	for n.reads() < n.maxReads() {
		idx := -1
		for i, id := range n.Queue {
			if !n.reading(id) {
				idx = i
				break
			}
		}
		if idx < 0 {
			break
		}
		id := n.Queue[idx]
		n.Queue = append(n.Queue[:idx:idx], n.Queue[idx+1:]...)
		e := n.Entries[id]
		n.Seq++
		e.Attempt = n.Seq
		n.Entries[id] = e
		n.InFlight[id] = e.Attempt
		cmds = append(cmds, ReadStatus{ID: id, Path: e.Path, Attempt: e.Attempt})
	}
	return cmds
}

// viewsEqual compares two views, ignoring when they were read: a re-read with
// the same numbers is no change worth publishing.
func viewsEqual(a, b View) bool {
	switch x := a.(type) {
	case Unknown:
		_, ok := b.(Unknown)
		return ok
	case Fresh:
		y, ok := b.(Fresh)
		return ok && snapEqual(x.Snap, y.Snap)
	case Problem:
		y, ok := b.(Problem)
		if !ok || x.Kind != y.Kind || (x.Last == nil) != (y.Last == nil) {
			return false
		}
		return x.Last == nil || snapEqual(*x.Last, *y.Last)
	}
	return false
}

func snapEqual(a, b Snapshot) bool {
	ca, cb := a.LastCommit, b.LastCommit
	a.LastCommit, b.LastCommit = nil, nil
	if a != b || (ca == nil) != (cb == nil) {
		return false
	}
	return ca == nil || (ca.OID == cb.OID && ca.Subject == cb.Subject && ca.Author == cb.Author && ca.At.Equal(cb.At))
}

// RunRefresh feeds inputs to Refresh in order and returns the final state and
// every command in order. The property tests drive Refresh through it.
func RunRefresh(r Refresher, inputs []RefreshInput) (Refresher, []RefreshCommand) {
	var all []RefreshCommand
	for _, in := range inputs {
		var cmds []RefreshCommand
		r, cmds = Refresh(r, in)
		all = append(all, cmds...)
	}
	return r, all
}
