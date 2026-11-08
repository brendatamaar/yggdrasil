package worktreeenv

import "github.com/brendatamaar/yggdrasil/internal/core"

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

// AllocAudit is committed with the transition.
type AllocAudit struct {
	Action   string // worktreeenv.ports, worktreeenv.conflict, worktreeenv.range
	Worktree core.WorktreeID
	Block    Block
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

// Allocate is the port-block decision: (table2, commands) = Allocate(table1,
// input). Foundation stub; the worktreeenv task (E1) implements it.
func Allocate(t Table, in AllocInput) (Table, []AllocCommand) {
	return t, nil
}
