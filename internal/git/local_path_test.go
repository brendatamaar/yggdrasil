package git

import "testing"

func TestUNCRemotesAreNotLocalPaths(t *testing.T) {
	for _, u := range []string{`\\host\share\repo.git`, `//host/share/repo.git`, `\\?\C:\repo.git`} {
		if IsLocalPath(u) {
			t.Errorf("IsLocalPath(%q) = true", u)
		}
		if checkRemoteURL(u) == nil {
			t.Errorf("checkRemoteURL(%q) accepted a network share", u)
		}
	}
}
