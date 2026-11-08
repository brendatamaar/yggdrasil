//go:build windows

package platform

// On Windows every child lives in a job object that ends with Yggdrasil, so
// there are never orphans to track (DV-03): nothing is written or removed.

// RecordProcess does nothing on Windows.
func RecordProcess(string, string, int) error { return nil }

// ForgetProcess does nothing on Windows.
func ForgetProcess(string, string) error { return nil }

// ListOrphans returns nothing on Windows.
func ListOrphans(string) ([]Orphan, error) { return nil, nil }

// StopOrphan does nothing on Windows.
func StopOrphan(string, string) error { return nil }
