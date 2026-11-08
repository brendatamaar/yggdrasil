package terminal

import (
	"maps"
	"slices"
	"time"

	"github.com/brendatamaar/yggdrasil/internal/core"
)

// Step is the terminal state machine: (registry2, commands) = Step(registry1,
// input). An input that is illegal for the state returns the registry
// unchanged and no commands (docs/design/terminal.md). Step never changes the
// registry it is given: it works on a copy of the maps.
func Step(r Registry, in Input) (Registry, []Command) {
	switch in := in.(type) {
	case IssueRequested:
		return issue(r, in)
	case RedeemRequested:
		return redeem(r, in)
	case Opened:
		return opened(r, in)
	case OpenFailed:
		return openFailed(r, in)
	case Resized:
		return resized(r, in)
	case ClientClosed:
		return closeSession(r, in.Session, CauseClientClosed, 0, in.Now)
	case CloseRequested:
		return closeSession(r, in.Session, CauseUserClosed, 0, in.Now)
	case ShellExited:
		return shellExited(r, in)
	case Shutdown:
		return shutdown(r, in)
	case Wake:
		return wake(r, in)
	}
	return r, nil
}

// Run feeds inputs to Step in order and returns the final registry and every
// command emitted, in order.
func Run(r Registry, inputs []Input) (Registry, []Command) {
	var all []Command
	for _, in := range inputs {
		var cmds []Command
		r, cmds = Step(r, in)
		all = append(all, cmds...)
	}
	return r, all
}

func clone(r Registry) Registry {
	r.Tokens = maps.Clone(r.Tokens)
	r.Sessions = maps.Clone(r.Sessions)
	if r.Tokens == nil {
		r.Tokens = map[TokenHash]Token{}
	}
	if r.Sessions == nil {
		r.Sessions = map[core.TerminalSessionID]Session{}
	}
	return r
}

func pending(r Registry) int {
	n := 0
	for _, t := range r.Tokens {
		if _, ok := t.(Issued); ok {
			n++
		}
	}
	return n
}

func active(r Registry) int {
	n := 0
	for _, s := range r.Sessions {
		switch s.(type) {
		case Opening, Open:
			n++
		}
	}
	return n
}

func refuseIssue(r Registry, id ReplyID, why Refusal) (Registry, []Command) {
	return r, []Command{Reply{ID: id, Kind: ReplyIssue, Refusal: why}}
}

func issue(r Registry, in IssueRequested) (Registry, []Command) {
	if r.Phase == Draining {
		return refuseIssue(r, in.Reply, RefusedShuttingDown)
	}
	if _, dup := r.Tokens[in.Hash]; dup {
		return refuseIssue(r, in.Reply, RefusedCollision)
	}
	if pending(r) >= MaxPending {
		return refuseIssue(r, in.Reply, RefusedTooManyPending)
	}
	r = clone(r)
	exp := in.Now.Add(TokenTTL)
	r.Tokens[in.Hash] = Issued{Folder: in.Folder, ExpiresAt: exp}
	return r, []Command{
		ScheduleWake{Kind: WakeExpire, At: exp},
		Reply{ID: in.Reply, Kind: ReplyIssue, Granted: true},
	}
}

func refuseRedeem(id ReplyID, why Refusal) []Command {
	return []Command{Reply{ID: id, Kind: ReplyRedeem, Refusal: why}}
}

func redeem(r Registry, in RedeemRequested) (Registry, []Command) {
	tok, ok := r.Tokens[in.Hash]
	if !ok {
		return r, refuseRedeem(in.Reply, RefusedUnknown)
	}
	issued, ok := tok.(Issued)
	if !ok {
		return r, refuseRedeem(in.Reply, RefusedReused)
	}
	r = clone(r)
	if !in.Now.Before(issued.ExpiresAt) {
		delete(r.Tokens, in.Hash)
		return r, refuseRedeem(in.Reply, RefusedExpired)
	}
	// From here on the token is spent, whatever the outcome.
	r.Tokens[in.Hash] = Consumed{At: in.Now, ExpiresAt: issued.ExpiresAt}
	_, taken := r.Sessions[in.Session]
	switch {
	case in.Check != FolderTracked:
		return r, refuseRedeem(in.Reply, RefusedNotTracked)
	case r.Phase == Draining:
		return r, refuseRedeem(in.Reply, RefusedShuttingDown)
	case active(r) >= MaxSessions:
		return r, refuseRedeem(in.Reply, RefusedTooManySessions)
	case taken || in.Size.Validate() != nil:
		return r, refuseRedeem(in.Reply, RefusedCollision)
	}
	r.Sessions[in.Session] = Opening{Folder: issued.Folder, Size: in.Size, Since: in.Now}
	// The open is audited before the shell exists, so a store that cannot take
	// the entry stops the open (invariant 8; the shell refuses on failure).
	return r, []Command{
		OpenPTY{Session: in.Session, Folder: issued.Folder, Size: in.Size},
		WriteAudit{Action: AuditOpen, Session: in.Session, Folder: issued.Folder},
		ScheduleWake{Kind: WakeOpenDeadline, At: in.Now.Add(OpenDeadline)},
		Reply{ID: in.Reply, Kind: ReplyRedeem, Granted: true, Session: in.Session},
	}
}

func opened(r Registry, in Opened) (Registry, []Command) {
	s, ok := r.Sessions[in.Session].(Opening)
	if !ok {
		return r, nil
	}
	r = clone(r)
	r.Sessions[in.Session] = Open{Folder: s.Folder, Pid: in.Pid, Size: s.Size, Since: in.Now}
	return r, nil
}

func openFailed(r Registry, in OpenFailed) (Registry, []Command) {
	switch s := r.Sessions[in.Session].(type) {
	case Opening:
		r = clone(r)
		delete(r.Sessions, in.Session)
		return r, []Command{WriteAudit{Action: AuditOpenFailed, Session: in.Session, Folder: s.Folder, Cause: CauseOpenFailed}}
	case Closing:
		// The close was asked for while the shell was starting and the start
		// failed (or was cancelled): nothing is left to wait for.
		r = clone(r)
		delete(r.Sessions, in.Session)
		return r, nil
	}
	return r, nil
}

func resized(r Registry, in Resized) (Registry, []Command) {
	s, ok := r.Sessions[in.Session].(Open)
	if !ok || in.Size.Validate() != nil {
		return r, nil
	}
	r = clone(r)
	s.Size = in.Size
	r.Sessions[in.Session] = s
	return r, []Command{ResizePTY(in)}
}

func closeSession(r Registry, id core.TerminalSessionID, cause CloseCause, code int, now time.Time) (Registry, []Command) {
	var folder Folder
	switch s := r.Sessions[id].(type) {
	case Opening:
		folder = s.Folder
	case Open:
		folder = s.Folder
	default:
		return r, nil
	}
	r = clone(r)
	r.Sessions[id] = Closing{Folder: folder, Cause: cause, Since: now}
	return r, []Command{
		ClosePTY{Session: id},
		WriteAudit{Action: AuditClose, Session: id, Folder: folder, Cause: cause, Code: code},
	}
}

func shellExited(r Registry, in ShellExited) (Registry, []Command) {
	switch s := r.Sessions[in.Session].(type) {
	case Open:
		r = clone(r)
		delete(r.Sessions, in.Session)
		return r, []Command{WriteAudit{Action: AuditClose, Session: in.Session, Folder: s.Folder, Cause: CauseShellExited, Code: in.Code}}
	case Closing:
		r = clone(r)
		delete(r.Sessions, in.Session)
		return r, nil
	}
	return r, nil
}

func shutdown(r Registry, in Shutdown) (Registry, []Command) {
	r = clone(r)
	r.Phase = Draining
	ids := make([]core.TerminalSessionID, 0, len(r.Sessions))
	for id := range r.Sessions {
		ids = append(ids, id)
	}
	slices.Sort(ids)
	var cmds []Command
	for _, id := range ids {
		var c []Command
		r, c = closeSession(r, id, CauseAppShutdown, 0, in.Now)
		cmds = append(cmds, c...)
	}
	return r, cmds
}

func wake(r Registry, in Wake) (Registry, []Command) {
	switch in.Kind {
	case WakeExpire:
		return expire(r, in)
	case WakeOpenDeadline:
		return openDeadline(r, in)
	}
	return r, nil
}

// openDeadline gives up on sessions that stayed Opening past OpenDeadline: a
// granted redeem whose socket never came must not hold a slot forever.
func openDeadline(r Registry, in Wake) (Registry, []Command) {
	var late []core.TerminalSessionID
	for id, s := range r.Sessions {
		if o, ok := s.(Opening); ok && !o.Since.Add(OpenDeadline).After(in.Now) {
			late = append(late, id)
		}
	}
	if len(late) == 0 {
		return r, nil
	}
	slices.Sort(late)
	r = clone(r)
	var cmds []Command
	for _, id := range late {
		o := r.Sessions[id].(Opening)
		r.Sessions[id] = Closing{Folder: o.Folder, Cause: CauseOpenFailed, Since: in.Now}
		cmds = append(cmds, ClosePTY{Session: id},
			WriteAudit{Action: AuditOpenFailed, Session: id, Folder: o.Folder, Cause: CauseOpenFailed})
	}
	return r, cmds
}

func expire(r Registry, in Wake) (Registry, []Command) {
	r = clone(r)
	var next time.Time
	for h, t := range r.Tokens {
		exp := tokenExpiry(t)
		if !exp.After(in.Now) {
			delete(r.Tokens, h)
			continue
		}
		if next.IsZero() || exp.Before(next) {
			next = exp
		}
	}
	if next.IsZero() {
		return r, nil
	}
	return r, []Command{ScheduleWake{Kind: WakeExpire, At: next}}
}

func tokenExpiry(t Token) time.Time {
	switch t := t.(type) {
	case Issued:
		return t.ExpiresAt
	case Consumed:
		return t.ExpiresAt
	}
	return time.Time{}
}
