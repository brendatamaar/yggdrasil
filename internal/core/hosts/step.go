package hosts

import (
	"cmp"
	"slices"
	"strings"
	"time"

	"github.com/brendatamaar/yggdrasil/internal/core"
)

// maxRateLimitWait caps a RetryAt a host sends, so a wrong header cannot
// silence an instance for long.
const maxRateLimitWait = time.Hour

// Backoff is the wait after the nth consecutive transient failure:
// min(10 s * 3^(n-1), 10 min). It is deterministic.
func Backoff(n int) time.Duration {
	d := 10 * time.Second
	for i := 1; i < n && d < MaxBackoff; i++ {
		d *= 3
	}
	return min(d, MaxBackoff)
}

func nowOf(in Input) time.Time {
	switch v := in.(type) {
	case ConfigChanged:
		return v.Now
	case TargetsChanged:
		return v.Now
	case Tick:
		return v.Now
	case RefreshRequested:
		return v.Now
	case Fetched:
		return v.Now
	case NotModified:
		return v.Now
	case FetchFailed:
		return v.Now
	}
	return time.Time{}
}

func clonePoller(p Poller) Poller {
	out := p
	out.Targets = make(map[Key]Target, len(p.Targets))
	for k, v := range p.Targets {
		out.Targets[k] = v
	}
	out.Entries = make(map[Key]Entry, len(p.Entries))
	for k, v := range p.Entries {
		out.Entries[k] = v
	}
	out.Instances = make(map[InstanceID]Instance, len(p.Instances))
	for k, v := range p.Instances {
		out.Instances[k] = v
	}
	out.Limited = make(map[InstanceID]time.Time, len(p.Limited))
	for k, v := range p.Limited {
		out.Limited[k] = v
	}
	out.InFlight = make(map[InstanceID]int, len(p.InFlight))
	for k, v := range p.InFlight {
		out.InFlight[k] = v
	}
	return out
}

// prevOf returns the last good data a phase holds.
func prevOf(ph Phase) Idle {
	switch v := ph.(type) {
	case Idle:
		return v
	case Fetching:
		return v.Prev
	case Waiting:
		return v.Prev
	case Blocked:
		return v.Prev
	}
	return Idle{}
}

// drop removes a key and frees the slot it held.
func (p *Poller) drop(k Key, cmds *[]Command) {
	if e, ok := p.Entries[k]; ok {
		if _, f := e.Phase.(Fetching); f {
			p.release(p.Targets[k].Instance)
		}
		delete(p.Entries, k)
		*cmds = append(*cmds, PublishKey{Key: k})
	}
	delete(p.Targets, k)
}

func (p *Poller) release(inst InstanceID) {
	if p.InFlight[inst] > 0 {
		p.InFlight[inst]--
	}
	if p.InFlight[inst] == 0 {
		delete(p.InFlight, inst)
	}
}

// newEntry makes the entry of a key that has just become a target.
func (p *Poller) newEntry(t Target) Entry {
	if inst, ok := p.Instances[t.Instance]; !ok || inst.Kind.Validate() != nil {
		return Entry{Phase: Blocked{Why: BlockUnsupported}}
	}
	return Entry{Phase: Idle{}}
}

// Step is the poller step: (state2, commands) = Step(state1, input). An input
// that doesn't apply (a stale attempt, an untargeted key) returns the state
// unchanged and no commands.
func Step(p Poller, in Input) (Poller, []Command) {
	now := nowOf(in)
	q := clonePoller(p)
	var cmds []Command
	switch v := in.(type) {
	case ConfigChanged:
		q.config(v, &cmds)
	case TargetsChanged:
		q.targets(v, &cmds)
	case Tick:
		for inst, until := range q.Limited {
			if !now.Before(until) {
				delete(q.Limited, inst)
			}
		}
	case RefreshRequested:
		q.refresh(v)
	case Fetched:
		if !q.result(v.Key, v.Attempt, &cmds, func(e Entry, prev Idle, t Target) Entry {
			if !payloadFits(v.Key, v.Payload) {
				return q.fail(e, prev, FetchFailed{Kind: FailTransient, Now: now}, t)
			}
			return Entry{Attempt: e.Attempt, Phase: Idle{Data: v.Payload, FetchedAt: now, ETag: v.ETag}}
		}) {
			return p, nil
		}
	case NotModified:
		if !q.result(v.Key, v.Attempt, &cmds, func(e Entry, prev Idle, t Target) Entry {
			if prev.Data == nil {
				// A 304 needs data to stand for; without it, retry later.
				return q.fail(e, prev, FetchFailed{Kind: FailTransient, Now: now}, t)
			}
			prev.FetchedAt = now
			return Entry{Attempt: e.Attempt, Phase: prev}
		}) {
			return p, nil
		}
	case FetchFailed:
		if v.Kind.Validate() != nil {
			return p, nil
		}
		if !q.result(v.Key, v.Attempt, &cmds, func(e Entry, prev Idle, t Target) Entry {
			return q.fail(e, prev, v, t)
		}) {
			return p, nil
		}
	default:
		return p, nil
	}
	cmds = append(cmds, q.start(now)...)
	return q, cmds
}

func payloadFits(k Key, pl Payload) bool {
	switch k.(type) {
	case RepoKey:
		_, ok := pl.(RepoState)
		return ok
	case InboxKey:
		_, ok := pl.(Inbox)
		return ok
	}
	return false
}

func (p *Poller) config(v ConfigChanged, cmds *[]Command) {
	old := p.Instances
	p.Instances = make(map[InstanceID]Instance, len(v.Instances))
	for _, in := range v.Instances {
		if in.ID.Validate() == nil {
			p.Instances[in.ID] = in
		}
	}
	for k, t := range p.Targets {
		in, ok := p.Instances[t.Instance]
		if _, isInbox := k.(InboxKey); !ok || (isInbox && !in.Token) {
			p.drop(k, cmds)
		}
	}
	// New inbox keys: one per instance with a token.
	for id, in := range p.Instances {
		if !in.Token {
			continue
		}
		k := InboxKey{Instance: id}
		if _, ok := p.Targets[k]; !ok {
			t := Target{Instance: id, TTL: InboxTTL}
			p.Targets[k] = t
			p.Entries[k] = p.newEntry(t)
		}
	}
	// A changed token or kind makes a Blocked key worth a retry.
	for k, e := range p.Entries {
		t := p.Targets[k]
		b, blocked := e.Phase.(Blocked)
		if !blocked || p.Instances[t.Instance].Kind.Validate() != nil {
			continue
		}
		if o, had := old[t.Instance]; !had || o != p.Instances[t.Instance] || b.Why == BlockUnsupported {
			// Zero FetchedAt makes the data stale, so it is fetched at once
			// with the new token instead of waiting out the TTL.
			prev := b.Prev
			prev.FetchedAt = time.Time{}
			e.Phase = prev
			p.Entries[k] = e
		}
	}
	*cmds = append(*cmds, PublishHosts{})
}

func (p *Poller) targets(v TargetsChanged, cmds *[]Command) {
	want := map[Key]Target{}
	for _, r := range v.Repos {
		if r.Branch == "" || r.Repo.Validate() != nil {
			continue
		}
		if _, ok := p.Instances[r.Instance]; !ok {
			continue
		}
		want[RepoKey{Repo: r.Repo, Branch: r.Branch}] = Target{Instance: r.Instance, Project: r.Project, TTL: RepoTTL}
	}
	for k := range p.Targets {
		if _, isRepo := k.(RepoKey); !isRepo {
			continue
		}
		if t, ok := want[k]; !ok || t != p.Targets[k] {
			p.drop(k, cmds)
		}
	}
	for k, t := range want {
		if _, ok := p.Targets[k]; !ok {
			p.Targets[k] = t
			p.Entries[k] = p.newEntry(t)
		}
	}
}

func (p *Poller) refresh(v RefreshRequested) {
	keys := v.Keys
	if len(keys) == 0 {
		for k := range p.Targets {
			keys = append(keys, k)
		}
	}
	for _, k := range keys {
		e, ok := p.Entries[k]
		if _, targeted := p.Targets[k]; !ok || !targeted {
			continue
		}
		switch ph := e.Phase.(type) {
		case Idle:
			ph.FetchedAt = time.Time{}
			e.Phase = ph
		case Waiting:
			prev := ph.Prev
			prev.FetchedAt = time.Time{}
			e.Phase = prev
		case Blocked:
			if p.Instances[p.Targets[k].Instance].Kind.Validate() != nil {
				continue
			}
			prev := ph.Prev
			prev.FetchedAt = time.Time{}
			e.Phase = prev
		default:
			continue
		}
		p.Entries[k] = e
	}
}

// result applies a fetch result when the key is targeted and still waiting for
// this attempt. It returns false when the result is stale.
func (p *Poller) result(k Key, attempt int, cmds *[]Command, apply func(Entry, Idle, Target) Entry) bool {
	t, targeted := p.Targets[k]
	e, ok := p.Entries[k]
	if !targeted || !ok {
		return false
	}
	f, fetching := e.Phase.(Fetching)
	if !fetching || f.Attempt != attempt {
		return false
	}
	p.release(t.Instance)
	ne := apply(e, f.Prev, t)
	p.Entries[k] = ne
	*cmds = append(*cmds, PublishKey{Key: k})
	if b, blocked := ne.Phase.(Blocked); blocked && b.Why == BlockUnauthorized {
		*cmds = append(*cmds, PublishHosts{})
	}
	return true
}

// fail computes the entry after a failed fetch and applies its side effects
// on the instance (rate limit, blocking the token's other keys).
func (p *Poller) fail(e Entry, prev Idle, v FetchFailed, t Target) Entry {
	now := v.Now
	switch v.Kind {
	case FailRateLimited:
		until := now.Add(DefaultRateLimitWait)
		if v.RetryAt != nil && v.RetryAt.After(now) {
			until = *v.RetryAt
			if limit := now.Add(maxRateLimitWait); until.After(limit) {
				until = limit
			}
		}
		if cur, ok := p.Limited[t.Instance]; !ok || until.After(cur) {
			p.Limited[t.Instance] = until
		}
		return Entry{Attempt: e.Attempt, Failures: e.Failures, Phase: Waiting{Until: until, Failures: e.Failures, Prev: prev, Why: WaitRateLimited}}
	case FailUnauthorized:
		if p.Instances[t.Instance].Token {
			for k, o := range p.Entries {
				if p.Targets[k].Instance != t.Instance {
					continue
				}
				switch ph := o.Phase.(type) {
				case Idle, Waiting:
					p.Entries[k] = Entry{Attempt: o.Attempt, Failures: o.Failures, Phase: Blocked{Why: BlockUnauthorized, Since: now, Prev: prevOf(ph)}}
				}
			}
		}
		return Entry{Attempt: e.Attempt, Phase: Blocked{Why: BlockUnauthorized, Since: now, Prev: prev}}
	case FailNotFound:
		return Entry{Attempt: e.Attempt, Phase: Blocked{Why: BlockNotFound, Since: now, Prev: prev}}
	}
	n := e.Failures + 1
	return Entry{Attempt: e.Attempt, Failures: n, Phase: Waiting{Until: now.Add(Backoff(n)), Failures: n, Prev: prev, Why: WaitTransient}}
}

func due(e Entry, t Target, now time.Time) bool {
	switch ph := e.Phase.(type) {
	case Idle:
		return ph.Data == nil || !now.Before(ph.FetchedAt.Add(t.TTL))
	case Waiting:
		return !now.Before(ph.Until)
	}
	return false
}

func keyLess(a, b Key) int {
	ai, aInbox := a.(InboxKey)
	bi, bInbox := b.(InboxKey)
	switch {
	case aInbox && bInbox:
		return cmp.Compare(ai.Instance, bi.Instance)
	case aInbox:
		return -1
	case bInbox:
		return 1
	}
	ar, br := a.(RepoKey), b.(RepoKey)
	if c := cmp.Compare(ar.Repo, br.Repo); c != 0 {
		return c
	}
	return cmp.Compare(ar.Branch, br.Branch)
}

// start moves due keys to Fetching, inbox first and then repos by ID, while
// the instance is not rate limited and has a free slot.
func (p *Poller) start(now time.Time) []Command {
	keys := make([]Key, 0, len(p.Targets))
	for k := range p.Targets {
		keys = append(keys, k)
	}
	slices.SortFunc(keys, keyLess)
	var cmds []Command
	for _, k := range keys {
		t, e := p.Targets[k], p.Entries[k]
		if !due(e, t, now) || now.Before(p.Limited[t.Instance]) || p.InFlight[t.Instance] >= MaxInFlight {
			continue
		}
		prev := prevOf(e.Phase)
		p.Seq++
		e.Attempt = p.Seq
		e.Phase = Fetching{Attempt: e.Attempt, Prev: prev}
		p.Entries[k] = e
		p.InFlight[t.Instance]++
		f := Fetch{Key: k, Instance: t.Instance, Project: t.Project, Attempt: e.Attempt, ETag: prev.ETag}
		if rk, ok := k.(RepoKey); ok {
			f.Branch = rk.Branch
		}
		cmds = append(cmds, f)
	}
	return cmds
}

// FailedPipelines returns the repos whose current branch's pipeline failed.
// repo-status counts use it for the "Failed pipeline" filter (WS-03).
func FailedPipelines(p Poller) map[core.RepoID]struct{} {
	out := map[core.RepoID]struct{}{}
	for k, e := range p.Entries {
		rk, ok := k.(RepoKey)
		if !ok {
			continue
		}
		if rs, ok := prevOf(e.Phase).Data.(RepoState); ok && rs.Pipeline != nil && rs.Pipeline.Status == PipelineFailed {
			out[rk.Repo] = struct{}{}
		}
	}
	return out
}

// Links builds the host links for a project and branch. They are plain
// strings, built without IO.
func Links(inst Instance, project ProjectPath, branch string) LinkSet {
	base := strings.TrimRight(inst.Base, "/")
	repo := base + "/" + string(project)
	l := LinkSet{Repo: repo}
	br := ""
	if branch != "" {
		br = escapePath(branch)
	}
	switch inst.Kind {
	case KindGitLab:
		l.MRList = repo + "/-/merge_requests"
		l.Pipelines = repo + "/-/pipelines"
		if br != "" {
			l.Branch = repo + "/-/tree/" + br
		}
	case KindGitHub:
		l.MRList = repo + "/pulls"
		l.Pipelines = repo + "/actions"
		if br != "" {
			l.Branch = repo + "/tree/" + br
		}
	case KindGitea:
		l.MRList = repo + "/pulls"
		l.Pipelines = repo + "/actions"
		if br != "" {
			l.Branch = repo + "/src/branch/" + br
		}
	}
	return l
}

// escapePath percent-encodes everything but unreserved characters and "/".
func escapePath(s string) string {
	const hex = "0123456789ABCDEF"
	var b strings.Builder
	for i := 0; i < len(s); i++ {
		c := s[i]
		switch {
		case c >= 'a' && c <= 'z', c >= 'A' && c <= 'Z', c >= '0' && c <= '9', c == '-', c == '.', c == '_', c == '~', c == '/':
			b.WriteByte(c)
		default:
			b.WriteByte('%')
			b.WriteByte(hex[c>>4])
			b.WriteByte(hex[c&15])
		}
	}
	return b.String()
}
