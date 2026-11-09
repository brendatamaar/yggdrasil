package worktreeenv_test

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"log/slog"
	"os"
	"path/filepath"
	"strings"
	"sync"
	"sync/atomic"
	"testing"
	"time"

	"github.com/brendatamaar/yggdrasil/internal/core"
	env "github.com/brendatamaar/yggdrasil/internal/core/worktreeenv"
	"github.com/brendatamaar/yggdrasil/internal/worktreeenv"
	"github.com/brendatamaar/yggdrasil/internal/worktreeenv/worktreeenvtest"
)

var now = time.Date(2026, 5, 1, 9, 0, 0, 0, time.UTC)

// logs is a Config.Logger that keeps what it is given.
func logs() (*slog.Logger, *bytes.Buffer) {
	var b bytes.Buffer
	return slog.New(slog.NewTextHandler(&b, nil)), &b
}

type rig struct {
	svc    *worktreeenv.Service
	store  *worktreeenvtest.Store
	prober *worktreeenvtest.Prober
	fs     *worktreeenvtest.FS
	pub    *[]core.WorktreeID
}

func newRig(t *testing.T, ports worktreeenv.PortStore) rig {
	t.Helper()
	store := worktreeenvtest.NewStore("rep_1")
	if ports == nil {
		ports = store
	}
	prober, fsys := worktreeenvtest.NewProber(), worktreeenvtest.NewFS()
	var n atomic.Int32
	var pub []core.WorktreeID
	svc, err := worktreeenv.New(worktreeenv.Config{
		Ports: ports, Settings: store, Audit: store, Prober: prober, FS: fsys,
		RepoDir:       func(context.Context, core.RepoID) (string, error) { return "/repos/shop-api", nil },
		Now:           func() time.Time { return now },
		NewID:         func(p string) string { return fmt.Sprintf("%s_%d", p, n.Add(1)) },
		OnProfileDone: func(w core.WorktreeID) { pub = append(pub, w) },
	})
	if err != nil {
		t.Fatal(err)
	}
	return rig{svc: svc, store: store, prober: prober, fs: fsys, pub: &pub}
}

func TestAllocateLowestFreeSkippingBusyBlocks(t *testing.T) {
	r := newRig(t, nil)
	ctx := context.Background()
	r.prober.Used[20005] = true // a foreign listener in the first block
	a, err := r.svc.Allocate(ctx, "wt_a")
	if err != nil || a != (core.PortBlock{Base: 20020, Size: 20}) {
		t.Fatalf("a = %v %v", a, err)
	}
	b, _ := r.svc.Allocate(ctx, "wt_b")
	if b.Base != 20040 {
		t.Fatalf("b = %v", b)
	}
	if again, _ := r.svc.BlockFor(ctx, "wt_a"); again != a {
		t.Fatalf("not idempotent: %v", again)
	}
	var detail map[string]int
	for _, e := range r.store.Audits() {
		if e.Action == "worktreeenv.ports" && e.Subject == "worktree:wt_a" {
			_ = json.Unmarshal(e.Detail, &detail)
		}
	}
	if detail["base"] != 20020 {
		t.Fatalf("audit detail %v", detail)
	}
}

func TestAllocateWidensProbeBeforeRefusing(t *testing.T) {
	r := newRig(t, nil)
	ctx := context.Background()
	if err := r.svc.SetPortRange(ctx, "30000-30999"); err != nil { // 50 blocks
		t.Fatal(err)
	}
	// Everything in the first 12 blocks is busy, so the narrow first probe is not enough.
	for b := 0; b < 12; b++ {
		r.prober.Used[30000+b*20] = true
	}
	got, err := r.svc.Allocate(ctx, "wt_a")
	if err != nil || got.Base != 30000+12*20 {
		t.Fatalf("got %v %v", got, err)
	}
}

func TestAllocateRangeFullIsPlain(t *testing.T) {
	r := newRig(t, nil)
	ctx := context.Background()
	if err := r.svc.SetPortRange(ctx, "30000-30039"); err != nil {
		t.Fatal(err)
	}
	for _, w := range []core.WorktreeID{"wt_a", "wt_b"} {
		if _, err := r.svc.Allocate(ctx, w); err != nil {
			t.Fatal(err)
		}
	}
	_, err := r.svc.Allocate(ctx, "wt_c")
	var ue *worktreeenv.Error
	if !errors.Is(err, worktreeenv.ErrRangeFull) || !errors.As(err, &ue) || !strings.Contains(ue.UserMessage(), "Widen the port range") {
		t.Fatalf("got %v", err)
	}
	if err := r.svc.Release(ctx, "wt_a"); err != nil {
		t.Fatal(err)
	}
	if got, err := r.svc.Allocate(ctx, "wt_c"); err != nil || got.Base != 30000 {
		t.Fatalf("freed block not reused: %v %v", got, err)
	}
	if err := r.svc.Release(ctx, "wt_a"); err != nil { // second release is a no-op
		t.Fatal(err)
	}
}

func TestBadRangeSettingIsReportedNotIgnored(t *testing.T) {
	r := newRig(t, nil)
	if err := r.svc.SetPortRange(context.Background(), "80-90"); err == nil {
		t.Fatal("range below 1024 accepted")
	}
	if err := r.svc.SetPortRange(context.Background(), "abc"); err == nil {
		t.Fatal("garbage accepted")
	}
}

func TestProbeFailureCountsAsFree(t *testing.T) {
	r := newRig(t, nil)
	r.prober.Fail = errors.New("boom")
	if got, err := r.svc.Allocate(context.Background(), "wt_a"); err != nil || got.Base != 20000 {
		t.Fatalf("%v %v", got, err)
	}
}

// racingStore loses the first N saves to a block another process took.
type racingStore struct {
	worktreeenv.PortStore
	lose atomic.Int32
	tr   *worktreeenvtest.Store
}

func (s *racingStore) SaveBlock(ctx context.Context, w core.WorktreeID, b core.PortBlock, at time.Time, a core.AuditEntry) error {
	if s.lose.Add(-1) >= 0 {
		// another process takes this base first
		_ = s.tr.SaveBlock(ctx, "wt_other"+core.WorktreeID(fmt.Sprint(b.Base)), b, at, a)
		return fmt.Errorf("save: %w", worktreeenv.ErrConflict)
	}
	return s.PortStore.SaveBlock(ctx, w, b, at, a)
}

func TestAllocateRetriesOnConflict(t *testing.T) {
	store := worktreeenvtest.NewStore("rep_1")
	rs := &racingStore{PortStore: store, tr: store}
	rs.lose.Store(2)
	r := newRig(t, rs)
	_ = r
	svc, _ := worktreeenv.New(worktreeenv.Config{
		Ports: rs, Settings: store, Audit: store, Prober: worktreeenvtest.NewProber(), FS: worktreeenvtest.NewFS(),
		RepoDir: func(context.Context, core.RepoID) (string, error) { return "/r", nil },
		Now:     func() time.Time { return now }, NewID: func(p string) string { return p + "_x" },
	})
	got, err := svc.Allocate(context.Background(), "wt_a")
	if err != nil || got.Base != 20040 {
		t.Fatalf("after two lost races: %v %v", got, err)
	}
	rs.lose.Store(99)
	if _, err := svc.Allocate(context.Background(), "wt_b"); !errors.Is(err, worktreeenv.ErrConflict) {
		t.Fatalf("bounded retries should give up with the conflict: %v", err)
	}
}

func TestConcurrentAllocationsGetDistinctBlocks(t *testing.T) {
	r := newRig(t, nil)
	var wg sync.WaitGroup
	got := make([]core.PortBlock, 12)
	for i := range got {
		wg.Add(1)
		go func() {
			defer wg.Done()
			b, err := r.svc.Allocate(context.Background(), core.WorktreeID(fmt.Sprintf("wt_%d", i)))
			if err != nil {
				t.Error(err)
			}
			got[i] = b
		}()
	}
	wg.Wait()
	seen := map[int]bool{}
	for _, b := range got {
		if seen[b.Base] {
			t.Fatalf("base %d handed out twice", b.Base)
		}
		seen[b.Base] = true
	}
}

func wt(repoID core.RepoID) core.Worktree {
	return core.Worktree{ID: "wt_1", RepoID: repoID, Path: "/wt/amber-fox"}
}

func TestProfileCopyUserStarted(t *testing.T) {
	r := newRig(t, nil)
	ctx := context.Background()
	r.fs.WriteFile("/repos/shop-api", ".env", []byte("TOKEN=fictional\r\n"))
	r.fs.WriteFile("/repos/shop-api", "config/local.json", []byte("{}"))
	r.fs.Link("/repos/shop-api", "linked.env")
	r.fs.WriteFile("/wt/amber-fox", "config/local.json", []byte("mine"))
	if err := r.svc.SetProfile(ctx, "rep_1", []string{".env", `config\local.json`, "linked.env", "gone.env"}, false); err != nil {
		t.Fatal(err)
	}
	if _, err := r.svc.CopyProfile(ctx, wt("rep_1"), env.TriggerUserStarted); err != nil {
		t.Fatal(err)
	}
	if got, ok := r.fs.Read("/wt/amber-fox", ".env"); !ok || string(got) != "TOKEN=fictional\r\n" {
		t.Fatalf("copy: %q", got)
	}
	if got, _ := r.fs.Read("/wt/amber-fox", "config/local.json"); string(got) != "mine" {
		t.Fatalf("existing file overwritten: %q", got)
	}
	if _, ok := r.fs.Read("/wt/amber-fox", "linked.env"); ok {
		t.Fatal("link copied")
	}
	if len(*r.pub) != 1 {
		t.Fatalf("published %d times", len(*r.pub))
	}
	var found bool
	for _, e := range r.store.Audits() {
		if e.Action != string(env.ActionProfileCopied) {
			continue
		}
		found = true
		if strings.Contains(string(e.Detail), "fictional") || e.Actor != "user" {
			t.Fatalf("audit %+v", e)
		}
		var d struct {
			Copied, Skipped int
			Names           []string
		}
		_ = json.Unmarshal(e.Detail, &d)
		if d.Copied != 1 || d.Skipped != 3 || len(d.Names) != 1 || d.Names[0] != ".env" {
			t.Fatalf("detail %s", e.Detail)
		}
	}
	if !found {
		t.Fatal("no profile audit entry")
	}
}

func TestProfileCopyAgentTaskToggle(t *testing.T) {
	r := newRig(t, nil)
	ctx := context.Background()
	r.fs.WriteFile("/repos/shop-api", ".env", []byte("A=1"))
	if err := r.svc.SetProfile(ctx, "rep_1", []string{".env"}, false); err != nil {
		t.Fatal(err)
	}
	if _, err := r.svc.CopyProfile(ctx, wt("rep_1"), env.TriggerAgentTask); err != nil {
		t.Fatal(err)
	}
	if _, ok := r.fs.Read("/wt/amber-fox", ".env"); ok {
		t.Fatal("agent task got the profile with the toggle off")
	}
	var skipped bool
	for _, e := range r.store.Audits() {
		skipped = skipped || e.Action == string(env.ActionProfileSkipped)
	}
	if !skipped {
		t.Fatal("skip not audited")
	}
	if err := r.svc.SetProfile(ctx, "rep_1", []string{".env"}, true); err != nil {
		t.Fatal(err)
	}
	if _, err := r.svc.CopyProfile(ctx, wt("rep_1"), env.TriggerAgentTask); err != nil {
		t.Fatal(err)
	}
	if _, ok := r.fs.Read("/wt/amber-fox", ".env"); !ok {
		t.Fatal("toggle on but nothing copied")
	}
}

func TestSetProfileRejectsBadPaths(t *testing.T) {
	r := newRig(t, nil)
	for _, bad := range []string{"../x", ".git/config", "/etc/passwd", "*.env"} {
		err := r.svc.SetProfile(context.Background(), "rep_1", []string{bad}, false)
		var ue *worktreeenv.Error
		if !errors.As(err, &ue) {
			t.Errorf("%q: %v", bad, err)
		}
	}
	if n := len(r.store.Audits()); n != 0 {
		t.Fatalf("rejected input left %d audit entries", n)
	}
}

func TestPrepareDoesBothAndJoinsErrors(t *testing.T) {
	r := newRig(t, nil)
	ctx := context.Background()
	r.fs.WriteFile("/repos/shop-api", ".env", []byte("A=1"))
	_ = r.svc.SetProfile(ctx, "rep_1", []string{".env"}, false)
	if err := r.svc.Prepare(ctx, wt("rep_1"), env.TriggerUserStarted); err != nil {
		t.Fatal(err)
	}
	if b, _ := r.svc.BlockFor(ctx, "wt_1"); b.Base != 20000 {
		t.Fatalf("block %v", b)
	}
	if _, ok := r.fs.Read("/wt/amber-fox", ".env"); !ok {
		t.Fatal("profile not copied")
	}
	// unknown repo: the port part still ran
	w2 := core.Worktree{ID: "wt_2", RepoID: "rep_unknown", Path: "/wt/x"}
	if err := r.svc.Prepare(ctx, w2, env.TriggerUserStarted); !errors.Is(err, worktreeenv.ErrNotFound) {
		t.Fatalf("got %v", err)
	}
	if b, _ := r.svc.BlockFor(ctx, "wt_2"); b.Base != 20020 {
		t.Fatalf("ports skipped after profile error: %v", b)
	}
}

// TestRealDiskProfileEndToEnd copies a CRLF file between temp folders through
// the SQLite store.
func TestRealDiskProfileEndToEnd(t *testing.T) {
	s := newDBStore(t)
	ctx := context.Background()
	root := t.TempDir()
	repoDir := filepath.Join(root, "shop api")
	wtDir := filepath.Join(root, "wt dir")
	for _, d := range []string{repoDir, wtDir} {
		if err := os.MkdirAll(d, 0o750); err != nil {
			t.Fatal(err)
		}
	}
	data := []byte("KEY=value\r\nOTHER=1\r\n")
	if err := os.WriteFile(filepath.Join(repoDir, ".env"), data, 0o600); err != nil {
		t.Fatal(err)
	}
	store := worktreeenv.DBStore{S: s}
	svc, err := worktreeenv.New(worktreeenv.Config{
		Ports: store, Settings: store, Audit: store, Prober: worktreeenv.PlatformProber{}, FS: worktreeenv.OSFileSystem{},
		RepoDir: func(context.Context, core.RepoID) (string, error) { return repoDir, nil },
		Now:     func() time.Time { return now },
		NewID:   func(p string) string { return p + "_" + fmt.Sprint(time.Now().UnixNano()) },
	})
	if err != nil {
		t.Fatal(err)
	}
	// the DB needs real rows
	dbRepo, wtRow := seedRows(t, s)
	if err := svc.SetProfile(ctx, dbRepo, []string{".env"}, false); err != nil {
		t.Fatal(err)
	}
	wtRow.Path = wtDir
	if err := svc.Prepare(ctx, wtRow, env.TriggerUserConfirmed); err != nil {
		t.Fatal(err)
	}
	got, err := os.ReadFile(filepath.Join(wtDir, ".env"))
	if err != nil || string(got) != string(data) {
		t.Fatalf("copied %q %v", got, err)
	}
	if b, err := svc.BlockFor(ctx, wtRow.ID); err != nil || b.Base < 20000 || b.Size != 20 {
		t.Fatalf("block %v %v", b, err)
	}
	// second copy: file exists, nothing changes, nothing fails
	if _, err := svc.CopyProfile(ctx, wtRow, env.TriggerUserConfirmed); err != nil {
		t.Fatal(err)
	}
	if err := svc.Release(ctx, wtRow.ID); err != nil {
		t.Fatal(err)
	}
}

func TestProbeFailureIsLoggedWithoutDetails(t *testing.T) {
	store := worktreeenvtest.NewStore("rep_1")
	prober := worktreeenvtest.NewProber()
	prober.Fail = errors.New("open /proc/net/tcp: secret-folder")
	lg, buf := logs()
	svc, _ := worktreeenv.New(worktreeenv.Config{
		Ports: store, Settings: store, Audit: store, Prober: prober, FS: worktreeenvtest.NewFS(), Logger: lg,
		RepoDir: func(context.Context, core.RepoID) (string, error) { return "/r", nil },
		Now:     func() time.Time { return now }, NewID: func(p string) string { return p + "_x" },
	})
	if _, err := svc.Allocate(context.Background(), "wt_a"); err != nil {
		t.Fatal(err)
	}
	out := buf.String()
	if !strings.Contains(out, "level=WARN") || !strings.Contains(out, "port probe failed") || strings.Contains(out, "secret-folder") {
		t.Fatalf("log = %q", out)
	}
}

func TestPersistentOverlapIsAuditedOnce(t *testing.T) {
	store := worktreeenvtest.NewStore("rep_1")
	store.SeedBlock("wt_old1", core.PortBlock{Base: 20000, Size: 20})
	store.SeedBlock("wt_old2", core.PortBlock{Base: 20010, Size: 20}) // overlaps the first
	svc, _ := worktreeenv.New(worktreeenv.Config{
		Ports: store, Settings: store, Audit: store, Prober: worktreeenvtest.NewProber(), FS: worktreeenvtest.NewFS(),
		RepoDir: func(context.Context, core.RepoID) (string, error) { return "/r", nil },
		Now:     func() time.Time { return now }, NewID: func(p string) string { return p + "_x" },
	})
	for i := range 5 {
		if _, err := svc.Allocate(context.Background(), core.WorktreeID(fmt.Sprintf("wt_%d", i))); err != nil {
			t.Fatal(err)
		}
	}
	conflicts := 0
	for _, e := range store.Audits() {
		if e.Action == string(env.ActionConflict) {
			conflicts++
		}
	}
	if conflicts != 1 {
		t.Fatalf("%d conflict audit entries for one persistent overlap, want 1", conflicts)
	}
}
