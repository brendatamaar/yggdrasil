//go:build !windows

package platform

// launcherPython: there is no py launcher outside Windows.
func launcherPython([]string) string { return "" }

// isStoreStub: only Windows has Microsoft Store alias stubs.
func isStoreStub(string) bool { return false }
