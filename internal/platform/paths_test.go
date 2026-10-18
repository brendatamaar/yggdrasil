package platform

import (
	"errors"
	"strings"
	"testing"
)

// One case per rejection rule of CheckPath.
func TestCheckPathRejectionRules(t *testing.T) {
	rules := map[string]string{
		"plain \\\\wsl$":              `\\wsl$\Ubuntu\home`,
		"plain \\\\wsl.localhost":     `\\wsl.localhost\Ubuntu\home`,
		"upper case":                  `\\WSL$\Ubuntu`,
		"forward slashes":             `//wsl.localhost/Ubuntu/home`,
		"host alone":                  `\\wsl$`,
		"\\\\?\\UNC\\ prefix":         `\\?\UNC\wsl.localhost\Ubuntu\home`,
		"\\\\?\\UNC\\ prefix, wsl$":   `\\?\UNC\wsl$\Ubuntu`,
		"\\\\.\\UNC\\ prefix":         `\\.\UNC\wsl$\Ubuntu\home`,
		"\\\\.\\UNC\\ prefix, lower":  `\\.\unc\wsl.localhost\Ubuntu`,
		"plain \\\\?\\ before host":   `\\?\\wsl$\Ubuntu`,
		"plain \\\\.\\ before host":   `\\.\\wsl.localhost\Ubuntu`,
		"\\??\\UNC\\ NT prefix":       `\??\UNC\wsl$\Ubuntu`,
		"mixed slashes with prefix":   `//?/UNC/wsl$/Ubuntu`,
		"mixed case prefix":           `\\?\uNc\WSL$\Ubuntu`,
		"three leading slashes":       `\\\wsl$\Ubuntu`,
		"UNC with extra slashes":      `\\?\UNC\\wsl$\Ubuntu`,
		"GLOBALROOT Mup, \\\\?\\":     `\\?\GLOBALROOT\Device\Mup\wsl$\Ubuntu\home`,
		"GLOBALROOT Mup, NT prefix":   `\??\GLOBALROOT\Device\Mup\wsl.localhost\Ubuntu`,
		"GLOBALROOT Mup, \\\\.\\":     `\\.\GLOBALROOT\Device\Mup\wsl$\Ubuntu`,
		"Device Mup without GLOBALRT": `\\?\Device\Mup\wsl$\Ubuntu`,
		"GLOBALROOT Mup, odd slashes": `\\?\\GLOBALROOT\\Device\\Mup\\\wsl$\Ubuntu`,
		"GLOBALROOT Mup, fwd slashes": `//?/GLOBALROOT/Device/Mup/wsl$/Ubuntu`,
	}
	for name, p := range rules {
		if err := CheckPath(p); !errors.Is(err, ErrWSLPath) {
			t.Errorf("%s: CheckPath(%q) = %v, want ErrWSLPath", name, p, err)
		}
	}
	if err := CheckPath(""); err == nil || errors.Is(err, ErrWSLPath) {
		t.Errorf("empty path: %v, want the empty-path error", err)
	}
	for _, ok := range []string{
		`\\?\C:\code\repo`, `\\.\C:\code`, `\\?\UNC\server\share\repo`, `\\.\UNC\server\share`, `\\wsl$x\share`,
		`\\?\GLOBALROOT\Device\Mup\server\share`, `\\unc\wsl$`, `\wsl$\x`, `C:\wsl$\x`,
	} {
		if err := CheckPath(ok); err != nil {
			t.Errorf("CheckPath(%q) = %v, want nil", ok, err)
		}
	}
}

// Device paths that are not the network provider are refused outright.
func TestCheckPathRefusesOtherDevicePaths(t *testing.T) {
	for name, p := range map[string]string{
		"disk volume":          `\\?\GLOBALROOT\Device\HarddiskVolume1\code`,
		"disk volume, NT":      `\??\GLOBALROOT\Device\HarddiskVolume1\code`,
		"disk volume, \\\\.\\": `\\.\GLOBALROOT\Device\HarddiskVolume1`,
		"Device without root":  `\\?\Device\HarddiskVolume1\code`,
		"GLOBALROOT alone":     `\\?\GLOBALROOT`,
		"GLOBALROOT, no dev":   `\\?\GLOBALROOT\wsl$\Ubuntu`,
		"Mup first":            `\\?\Mup\server\share`,
		"Mup with no host":     `\\?\GLOBALROOT\Device\Mup`,
		"Mup, redirector":      `\\?\GLOBALROOT\Device\Mup\;LanmanRedirector\;Z:000\server\share`,
		"lanman device":        `\\?\GLOBALROOT\Device\LanmanRedirector\;Z:000\server\share`,
	} {
		err := CheckPath(p)
		if !errors.Is(err, ErrDevicePath) {
			t.Errorf("%s: CheckPath(%q) = %v, want ErrDevicePath", name, p, err)
		}
	}
}

// wslByIndependentRule decides, without using the parser's normaliser, whether
// path names a WSL host. Rule: lower-case, '/' is '\'. A UNC path (two leading
// '\') or an NT path (\??\) is split into components, ignoring empty ones.
// After a namespace marker component (?, ., ??) one optional "unc", or the
// chain globalroot, device, mup (each optional, in this order), is skipped.
// The next component must not be wsl$ or wsl.localhost.
func wslByIndependentRule(path string) bool {
	s := strings.ToLower(strings.ReplaceAll(path, "/", `\`))
	if !strings.HasPrefix(s, `\\`) && !strings.HasPrefix(s, `\??\`) {
		return false
	}
	var comps []string
	for _, c := range strings.Split(s, `\`) {
		if c != "" {
			comps = append(comps, c)
		}
	}
	if len(comps) > 0 && (comps[0] == "?" || comps[0] == "." || comps[0] == "??") {
		comps = comps[1:]
		if len(comps) > 0 && comps[0] == "unc" {
			comps = comps[1:]
		} else {
			for _, seg := range []string{"globalroot", "device", "mup"} {
				if len(comps) > 0 && comps[0] == seg {
					comps = comps[1:]
				}
			}
		}
	}
	return len(comps) > 0 && (comps[0] == "wsl$" || comps[0] == "wsl.localhost")
}

func TestIndependentRuleAgreesWithSeeds(t *testing.T) {
	if !wslByIndependentRule(`\\?\GLOBALROOT\Device\Mup\wsl$\x`) || wslByIndependentRule(`C:\wsl$`) || wslByIndependentRule(`\\wsl$x\a`) {
		t.Error("the independent rule is wrong on a known case")
	}
}

func FuzzCheckPath(f *testing.F) {
	for _, s := range []string{
		`\\wsl$\x`, `\\?\UNC\wsl$\x`, `\\.\UNC\wsl.localhost\x`, `\\?\\wsl$`, `//wsl.localhost/x`,
		`C:\code`, `\\?\C:\x`, `\\server\share`, `\??\UNC\wsl$`, "",
		`\\?\GLOBALROOT\Device\Mup\wsl$\Ubuntu`, `\??\GLOBALROOT\Device\Mup\wsl.localhost\x`,
		`\\.\GLOBALROOT\Device\Mup\wsl$`, `\\?\Device\Mup\wsl$`, `\\?\GLOBALROOT\wsl$`,
		`\\?\GLOBALROOT\Device\HarddiskVolume1\x`, `\\?\UNC\\wsl$\x`, `\\\wsl$\x`, `\\?\mup\wsl$`,
	} {
		f.Add(s)
	}
	f.Fuzz(func(t *testing.T, path string) {
		// accepted => not a WSL path by a rule that shares no code with the parser.
		if CheckPath(path) == nil && wslByIndependentRule(path) {
			t.Fatalf("CheckPath(%q) accepted a WSL path", path)
		}
	})
}
