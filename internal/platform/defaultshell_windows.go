//go:build windows

package platform

// DefaultShell returns the argv of the terminal shell (DV-04): Windows
// PowerShell, which every supported Windows has. StartPTY resolves
// powershell.exe on the PATH of the clean env it is given.
func DefaultShell() []string {
	return []string{"powershell.exe", "-NoLogo"}
}
