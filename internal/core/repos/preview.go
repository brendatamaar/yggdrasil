package repos

import "github.com/brendatamaar/yggdrasil/internal/core"

// BuildPreview sets what a scan of path found against the workspace: which
// candidates are known rows, which are Active, and whether path is already a
// project.
func BuildPreview(path CleanPath, scan ScanResult, ws Workspace) Preview {
	// Implemented in batch B1 (docs/phases/phase-2.md).
	return Preview{Path: path, Truncated: scan.Truncated, Skipped: scan.Skipped}
}

// Rescan compares a project's rows with a new scan of its folder.
func Rescan(scan ScanResult, ws Workspace, project core.ProjectID) RescanReport {
	// Implemented in batch B1 (docs/phases/phase-2.md).
	return RescanReport{}
}
