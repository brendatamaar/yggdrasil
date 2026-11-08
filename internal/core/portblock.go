package core

import "fmt"

// PortBlockSize is the number of consecutive ports one worktree owns (WE-02).
const PortBlockSize = 20

// PortBlock is a run of consecutive TCP ports owned by one worktree. Base is
// the first port; Size is PortBlockSize for newly allocated blocks.
// Used by core/worktreeenv (allocation) and core/devservers (expansion);
// domains may not import each other, so the type lives here.
type PortBlock struct {
	Base int
	Size int
}

// Empty reports whether the block holds no ports (a main checkout has none).
func (b PortBlock) Empty() bool { return b.Size <= 0 }

// Contains reports whether port lies in the block.
func (b PortBlock) Contains(port int) bool {
	return !b.Empty() && port >= b.Base && port < b.Base+b.Size
}

// Validate reports whether the block lies within 1..65535.
func (b PortBlock) Validate() error {
	if b.Size < 1 || b.Base < 1 || b.Base+b.Size-1 > 65535 {
		return fmt.Errorf("port block %d+%d: out of range", b.Base, b.Size)
	}
	return nil
}
