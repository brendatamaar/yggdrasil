package worktreeenv

import (
	"sort"

	"github.com/brendatamaar/yggdrasil/internal/core"
)

// Table is the port-block allocation state. Blocks are aligned:
// Base = Range.Lo + k*Size, so live blocks cannot overlap by construction.
type Table struct {
	Range Range
	Live  map[core.WorktreeID]Block
}

// AllocInput is a closed set: AllocateRequested, Released, RangeChanged, Loaded.
type AllocInput interface{ isAllocInput() }

// AllocateRequested asks for a block. Busy holds ports already listening on
// the machine; a block containing one is skipped.
type AllocateRequested struct {
	Worktree core.WorktreeID
	Busy     []int
	Now      Now
}

// Released frees a worktree's block.
type Released struct {
	Worktree core.WorktreeID
	Now      Now
}

// RangeChanged replaces the range. Live blocks never move.
type RangeChanged struct{ Range Range }

// Loaded sets the live blocks at startup.
type Loaded struct{ Blocks map[core.WorktreeID]Block }

func (AllocateRequested) isAllocInput() {}
func (Released) isAllocInput()          {}
func (RangeChanged) isAllocInput()      {}
func (Loaded) isAllocInput()            {}

// AllocCommand is a closed set: PersistBlock, ReleaseBlock, AllocAudit, AllocReply.
type AllocCommand interface{ isAllocCommand() }

// PersistBlock stores a new block. Idempotent per worktree.
type PersistBlock struct {
	Worktree core.WorktreeID
	Block    Block
}

// ReleaseBlock marks a block released (released_at); no row is deleted.
type ReleaseBlock struct{ Worktree core.WorktreeID }

// AllocAction is the closed set of audit actions the allocator writes.
type AllocAction string

// The closed set of AllocAction values.
const (
	ActionPorts         AllocAction = "worktreeenv.ports"
	ActionPortsReleased AllocAction = "worktreeenv.ports_released"
	ActionConflict      AllocAction = "worktreeenv.conflict"
	ActionRange         AllocAction = "worktreeenv.range"
)

// AllocAudit is committed with the transition. Range changes carry the new
// range in Range and no worktree.
type AllocAudit struct {
	Action   AllocAction
	Worktree core.WorktreeID
	Block    Block
	Range    Range
}

// AllocRefusal says why an allocation was refused.
type AllocRefusal string

// RefusedRangeFull is the only refusal: no free, non-busy aligned block.
const RefusedRangeFull AllocRefusal = "range_full"

// AllocReply answers AllocateRequested.
type AllocReply struct {
	Worktree core.WorktreeID
	Block    Block
	Refusal  AllocRefusal // empty when granted
}

func (PersistBlock) isAllocCommand() {}
func (ReleaseBlock) isAllocCommand() {}
func (AllocAudit) isAllocCommand()   {}
func (AllocReply) isAllocCommand()   {}

// Validate reports whether r is a usable range: ports 1024..65535 and room
// for at least one block.
func (r Range) Validate() error {
	switch {
	case r.Lo < 1024:
		return errRangeLow
	case r.Hi > 65535:
		return errRangeHigh
	case r.Hi-r.Lo+1 < core.PortBlockSize:
		return errRangeSmall
	}
	return nil
}

// Allocate is the port-block decision: (table2, commands) = Allocate(table1,
// input). It never changes the maps of its input.
func Allocate(t Table, in AllocInput) (Table, []AllocCommand) {
	switch v := in.(type) {
	case AllocateRequested:
		return allocate(t, v)
	case Released:
		b, ok := t.Live[v.Worktree]
		if !ok {
			return t, nil
		}
		live := copyLive(t.Live)
		delete(live, v.Worktree)
		t.Live = live
		return t, []AllocCommand{
			ReleaseBlock{Worktree: v.Worktree},
			AllocAudit{Action: ActionPortsReleased, Worktree: v.Worktree, Block: b},
		}
	case RangeChanged:
		if v.Range.Validate() != nil || v.Range == t.Range {
			return t, nil
		}
		t.Range = v.Range
		return t, []AllocCommand{AllocAudit{Action: ActionRange, Range: v.Range}}
	case Loaded:
		return load(t, v)
	}
	return t, nil
}

func allocate(t Table, v AllocateRequested) (Table, []AllocCommand) {
	if v.Worktree == "" {
		return t, nil
	}
	if b, ok := t.Live[v.Worktree]; ok {
		return t, []AllocCommand{AllocReply{Worktree: v.Worktree, Block: b}}
	}
	if t.Range.Validate() == nil {
		for base := t.Range.Lo; base+core.PortBlockSize-1 <= t.Range.Hi; base += core.PortBlockSize {
			b := Block{Base: base, Size: core.PortBlockSize}
			if overlapsAny(b, t.Live) || anyBusy(b, v.Busy) {
				continue
			}
			live := copyLive(t.Live)
			live[v.Worktree] = b
			t.Live = live
			return t, []AllocCommand{
				PersistBlock{Worktree: v.Worktree, Block: b},
				AllocAudit{Action: ActionPorts, Worktree: v.Worktree, Block: b},
				AllocReply{Worktree: v.Worktree, Block: b},
			}
		}
	}
	return t, []AllocCommand{AllocReply{Worktree: v.Worktree, Refusal: RefusedRangeFull}}
}

// load keeps the blocks that are valid and pairwise disjoint, lowest base
// first; the others are dropped from the table with a conflict audit entry.
func load(t Table, v Loaded) (Table, []AllocCommand) {
	ids := make([]core.WorktreeID, 0, len(v.Blocks))
	for id := range v.Blocks {
		ids = append(ids, id)
	}
	sort.Slice(ids, func(i, j int) bool {
		a, b := v.Blocks[ids[i]], v.Blocks[ids[j]]
		if a.Base != b.Base {
			return a.Base < b.Base
		}
		return ids[i] < ids[j]
	})
	live := make(map[core.WorktreeID]Block, len(ids))
	var cmds []AllocCommand
	for _, id := range ids {
		b := v.Blocks[id]
		if id == "" || b.Validate() != nil || overlapsAny(b, live) {
			cmds = append(cmds, AllocAudit{Action: ActionConflict, Worktree: id, Block: b})
			continue
		}
		live[id] = b
	}
	t.Live = live
	return t, cmds
}

func copyLive(m map[core.WorktreeID]Block) map[core.WorktreeID]Block {
	out := make(map[core.WorktreeID]Block, len(m)+1)
	for k, v := range m {
		out[k] = v
	}
	return out
}

func overlapsAny(b Block, live map[core.WorktreeID]Block) bool {
	for _, o := range live {
		if b.Base < o.Base+o.Size && o.Base < b.Base+b.Size {
			return true
		}
	}
	return false
}

func anyBusy(b Block, busy []int) bool {
	for _, p := range busy {
		if b.Contains(p) {
			return true
		}
	}
	return false
}
