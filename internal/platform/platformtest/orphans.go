package platformtest

import (
	"slices"
	"strings"
	"sync"
	"testing"

	"github.com/brendatamaar/yggdrasil/internal/platform"
)

// Orphans is the orphan API of internal/platform (RecordProcess, ForgetProcess,
// ListOrphans, StopOrphan) with the data directory bound, so the fake and the
// real functions fit the same contract suite.
type Orphans struct {
	Record func(id string, pid int) error
	Forget func(id string) error
	List   func() ([]platform.Orphan, error)
	Stop   func(id string) error
}

// RealOrphans binds the real functions to dataDir.
func RealOrphans(dataDir string) Orphans {
	return Orphans{
		Record: func(id string, pid int) error { return platform.RecordProcess(dataDir, id, pid) },
		Forget: func(id string) error { return platform.ForgetProcess(dataDir, id) },
		List:   func() ([]platform.Orphan, error) { return platform.ListOrphans(dataDir) },
		Stop:   func(id string) error { return platform.StopOrphan(dataDir, id) },
	}
}

// FakeOrphans is an in-memory Orphans. Processes are numbers: Spawn makes a
// "running" one, Stop on a recorded id ends it.
type FakeOrphans struct {
	mu      sync.Mutex
	next    int
	alive   map[int]bool
	records map[string]int
}

// NewFakeOrphans returns an empty fake.
func NewFakeOrphans() *FakeOrphans {
	return &FakeOrphans{next: 1000, alive: map[int]bool{}, records: map[string]int{}}
}

// Spawn adds a running process and returns its pid.
func (f *FakeOrphans) Spawn() int {
	f.mu.Lock()
	defer f.mu.Unlock()
	f.next++
	f.alive[f.next] = true
	return f.next
}

// Alive reports whether the fake process pid is running.
func (f *FakeOrphans) Alive(pid int) bool {
	f.mu.Lock()
	defer f.mu.Unlock()
	return f.alive[pid]
}

// Orphans returns the fake as the contract's Orphans.
func (f *FakeOrphans) Orphans() Orphans {
	return Orphans{Record: f.record, Forget: f.forget, List: f.list, Stop: f.stop}
}

func (f *FakeOrphans) record(id string, pid int) error {
	if !validFakeID(id) {
		return platform.ErrInvalidProcessID
	}
	f.mu.Lock()
	defer f.mu.Unlock()
	f.records[id] = pid
	return nil
}

func (f *FakeOrphans) forget(id string) error {
	if !validFakeID(id) {
		return platform.ErrInvalidProcessID
	}
	f.mu.Lock()
	defer f.mu.Unlock()
	delete(f.records, id)
	return nil
}

func (f *FakeOrphans) list() ([]platform.Orphan, error) {
	f.mu.Lock()
	defer f.mu.Unlock()
	var out []platform.Orphan
	for id, pid := range f.records {
		if f.alive[pid] {
			out = append(out, platform.Orphan{ID: id, Pid: pid})
		}
	}
	slices.SortFunc(out, func(a, b platform.Orphan) int { return strings.Compare(a.ID, b.ID) })
	return out, nil
}

func (f *FakeOrphans) stop(id string) error {
	if !validFakeID(id) {
		return platform.ErrInvalidProcessID
	}
	f.mu.Lock()
	defer f.mu.Unlock()
	if pid, ok := f.records[id]; ok {
		delete(f.alive, pid)
		delete(f.records, id)
	}
	return nil
}

func validFakeID(id string) bool { return platform.CheckProcessID(id) == nil }

// OrphanWorld lets the contract suite make and observe processes.
type OrphanWorld struct {
	Orphans
	// Spawn starts a process that runs until killed and returns its pid. The
	// implementation under test cleans up what is left when the test ends.
	Spawn func(t *testing.T) int
	// Alive reports whether pid is still running.
	Alive func(pid int) bool
}

// OrphanContract is the contract of the orphan functions (DV-03), run against
// the fake and the real implementation (non-Windows; on Windows the job object
// replaces the whole mechanism and the real functions are no-ops).
func OrphanContract(t *testing.T, newWorld func(t *testing.T) OrphanWorld) {
	t.Helper()
	t.Run("recorded live process is listed", func(t *testing.T) {
		w := newWorld(t)
		pid := w.Spawn(t)
		if err := w.Record("dev1", pid); err != nil {
			t.Fatal(err)
		}
		got, err := w.List()
		if err != nil {
			t.Fatal(err)
		}
		if want := []platform.Orphan{{ID: "dev1", Pid: pid}}; !slices.Equal(got, want) {
			t.Fatalf("List = %v, want %v", got, want)
		}
	})
	t.Run("list is sorted by id", func(t *testing.T) {
		w := newWorld(t)
		for _, id := range []string{"b2", "a1", "c3"} {
			if err := w.Record(id, w.Spawn(t)); err != nil {
				t.Fatal(err)
			}
		}
		got, _ := w.List()
		var ids []string
		for _, o := range got {
			ids = append(ids, o.ID)
		}
		if !slices.Equal(ids, []string{"a1", "b2", "c3"}) {
			t.Fatalf("ids = %v", ids)
		}
	})
	t.Run("forget removes, twice is fine", func(t *testing.T) {
		w := newWorld(t)
		if err := w.Record("dev1", w.Spawn(t)); err != nil {
			t.Fatal(err)
		}
		for range 2 {
			if err := w.Forget("dev1"); err != nil {
				t.Fatal(err)
			}
		}
		if got, _ := w.List(); len(got) != 0 {
			t.Fatalf("List after Forget = %v", got)
		}
	})
	t.Run("forget does not stop the process", func(t *testing.T) {
		w := newWorld(t)
		pid := w.Spawn(t)
		_ = w.Record("dev1", pid)
		_ = w.Forget("dev1")
		if !w.Alive(pid) {
			t.Fatal("Forget killed the process")
		}
	})
	t.Run("stop kills the process and forgets it", func(t *testing.T) {
		w := newWorld(t)
		pid := w.Spawn(t)
		other := w.Spawn(t)
		_ = w.Record("dev1", pid)
		_ = w.Record("dev2", other)
		if err := w.Stop("dev1"); err != nil {
			t.Fatal(err)
		}
		if w.Alive(pid) {
			t.Error("process still running after Stop")
		}
		if !w.Alive(other) {
			t.Error("Stop killed another recorded process")
		}
		got, _ := w.List()
		if len(got) != 1 || got[0].ID != "dev2" {
			t.Errorf("List after Stop = %v", got)
		}
	})
	t.Run("stop of an unknown id is not an error", func(t *testing.T) {
		w := newWorld(t)
		if err := w.Stop("nobody"); err != nil {
			t.Fatal(err)
		}
	})
	t.Run("invalid ids are refused", func(t *testing.T) {
		w := newWorld(t)
		for _, id := range []string{"", "..", "a/b", `a\b`, ".hidden", strings.Repeat("x", 200)} {
			if w.Record(id, w.Spawn(t)) == nil {
				t.Errorf("Record accepted id %q", id)
			}
			if w.Forget(id) == nil || w.Stop(id) == nil {
				t.Errorf("Forget/Stop accepted id %q", id)
			}
		}
	})
}
