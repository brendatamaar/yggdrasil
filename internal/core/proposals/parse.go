package proposals

import (
	"fmt"

	"github.com/brendatamaar/yggdrasil/internal/core"
)

// supported reports whether a kind can be created in Phase 3. Publish
// (Phase 6) and subtasks (Phase 7) are refused so that no proposal that cannot
// run is ever created.
func supported(k core.ProposalKind) error {
	if k.Validate() != nil {
		return fmt.Errorf("%w: %q", ErrUnsupportedKind, string(k))
	}
	switch k {
	case core.ProposalCommit, core.ProposalPush, core.ProposalMergeRequest,
		core.ProposalAgentFile, core.ProposalDeleteFiles, core.ProposalInstall:
		return nil
	}
	return fmt.Errorf("%w: %q", ErrUnsupportedKind, string(k))
}

// ParseAction turns an MCP tool's arguments into an Action. It rejects an
// unknown kind or field, publish and subtasks, an absolute path, "..", a
// drive, a stream, a device name, an empty path, more than 200 paths, a
// message that is empty or over 4 KiB, a title over 200, a description with a
// newline, content over 256 KiB, an agent-file path that is not protected, a
// branch failing the git ref rules and a remote not matching
// [A-Za-z0-9._-]+.
//
// The kind check is real; the argument parsing is a STUB (task X1): a
// supported kind returns an error.
func ParseAction(kind string, data []byte) (Action, error) {
	_ = data
	if err := supported(core.ProposalKind(kind)); err != nil {
		return nil, err
	}
	return nil, fmt.Errorf("parse %s proposal: %w", kind, ErrNotImplemented)
}

// ActionFromRow rebuilds an Action from the stored kind and payload, with the
// same rules as ParseAction plus kind/payload mismatch.
//
// The kind check is real; the payload parsing is a STUB (task X1).
func ActionFromRow(kind core.ProposalKind, payload []byte) (Action, error) {
	_ = payload
	if err := supported(kind); err != nil {
		return nil, err
	}
	return nil, fmt.Errorf("read %s proposal: %w", kind, ErrNotImplemented)
}

// RowOf is the inverse of ActionFromRow: ActionFromRow(RowOf(a)) equals a.
//
// STUB (task X1 implements it): returns the kind and a nil payload.
func RowOf(a Action) (core.ProposalKind, []byte) {
	if a == nil {
		return "", nil
	}
	return a.Kind(), nil
}
