package git

import (
	"testing"

	"github.com/brendatamaar/yggdrasil/internal/safety/safetytest"
)

func TestSnapshotContract(t *testing.T) {
	f := newFx(t)
	safetytest.SnapshotterContract(t, safetytest.SnapshotHarness{
		Snap: f.g,
		NewRepo: func(t *testing.T) string {
			d := f.repo("r")
			f.write(d, "a.txt", "one\n")
			f.write(d, "tool.sh", "echo\n")
			f.git(d, "add", "-A")
			f.git(d, "commit", "-q", "-m", "init")
			return d
		},
		NonRepo: func(t *testing.T) string { return t.TempDir() },
		Commit: func(t *testing.T, d string) {
			f.git(d, "add", "-A")
			f.git(d, "commit", "-q", "--allow-empty", "-m", "next")
		},
		Ignore: func(t *testing.T, d, name string) {
			f.write(d, ".gitignore", name+"\n")
		},
		SetExec: func(t *testing.T, d, rel string) {
			f.git(d, "update-index", "--chmod=+x", rel)
		},
	})
}

func TestSnapshotNoCommitsIsError(t *testing.T) {
	f := newFx(t)
	d := f.repo("empty")
	if _, err := f.g.Snapshot(t.Context(), d); err == nil {
		t.Fatal("snapshot of a repo without commits succeeded")
	}
}
