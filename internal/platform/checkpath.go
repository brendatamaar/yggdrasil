package platform

import (
	"errors"
	"fmt"
	"strings"
)

// ErrWSLPath is returned by CheckPath for \\wsl$ and \\wsl.localhost paths
// (PL-04). Its text is meant for the user.
var ErrWSLPath = errors.New(`folders inside WSL (\\wsl$) are not supported yet. Clone the repository to a normal Windows folder such as C:\code and add it from there`)

// ErrDevicePath is returned by CheckPath for raw device paths such as
// \\?\GLOBALROOT\Device\HarddiskVolume1: they can reach places (WSL, network
// redirectors) that the other checks exist to keep out. Its text is meant for
// the user.
var ErrDevicePath = errors.New(`this kind of device path is not supported. Use a normal folder path such as C:\code`)

// CheckPath refuses paths Yggdrasil does not support (PL-04). The error text
// says what to do next.
func CheckPath(path string) error {
	if strings.TrimSpace(path) == "" {
		return errors.New("the folder path is empty. Choose a folder")
	}
	p, err := normalizePath(path)
	if err != nil {
		return fmt.Errorf("%w (%s)", err, path)
	}
	if isWSLHost(p) {
		return fmt.Errorf("%w (%s)", ErrWSLPath, path)
	}
	return nil
}

// isWSLHost reports whether the normalised path p is on the \\wsl$ or
// \\wsl.localhost host. A host is the whole component; \\wsl$x is some other
// server.
func isWSLHost(p string) bool {
	for _, host := range []string{`\\wsl$`, `\\wsl.localhost`} {
		if p == host || strings.HasPrefix(p, host+`\`) {
			return true
		}
	}
	return false
}

// normalizePath lower-cases path, turns / into \, collapses the run of
// backslashes that opens a UNC path, and strips the Win32 namespace prefixes
// (\\?\, \\.\, \??\): \\?\UNC\host\share and \\.\UNC\host\share become
// \\host\share, and \\?\C:\x becomes C:\x. A device path that reaches the
// network provider, [GLOBALROOT\]Device\Mup\host\share, also becomes
// \\host\share. Any other path under GLOBALROOT, Device or Mup is refused
// (ErrDevicePath).
func normalizePath(path string) (string, error) {
	p := strings.ToLower(strings.ReplaceAll(path, "/", `\`))
	if strings.HasPrefix(p, `\\`) {
		p = `\\` + strings.TrimLeft(p, `\`)
	}
	for _, prefix := range []string{`\\?\`, `\\.\`, `\\??\`, `\??\`} {
		rest, ok := strings.CutPrefix(p, prefix)
		if !ok {
			continue
		}
		rest = strings.TrimLeft(rest, `\`)
		first, _, _ := strings.Cut(rest, `\`)
		switch first {
		case "globalroot", "device", "mup":
			return mupHost(rest)
		case "unc":
			if after, ok := strings.CutPrefix(rest, `unc\`); ok {
				return `\\` + strings.TrimLeft(after, `\`), nil
			}
		case "wsl$", "wsl.localhost":
			// \\?\\wsl$: the extra slashes make it a UNC host.
			return `\\` + rest, nil
		}
		return rest, nil
	}
	return p, nil
}

// mupHost maps [globalroot\]device\mup\host\share... to \\host\share...; the
// path is lower case with its namespace prefix already removed. It refuses
// every other path that starts with globalroot, device or mup.
func mupHost(rest string) (string, error) {
	r := rest
	if after, ok := strings.CutPrefix(r, `globalroot\`); ok {
		r = strings.TrimLeft(after, `\`)
	}
	r, ok := strings.CutPrefix(r, `device\`)
	if ok {
		r, ok = strings.CutPrefix(strings.TrimLeft(r, `\`), `mup\`)
	}
	r = strings.TrimLeft(r, `\`)
	// ";LanmanRedirector\;Z:..." is the redirector's own syntax, not a host.
	if !ok || r == "" || strings.HasPrefix(r, ";") {
		return "", ErrDevicePath
	}
	return `\\` + r, nil
}
