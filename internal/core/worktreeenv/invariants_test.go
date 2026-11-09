package worktreeenv

import (
	"fmt"
	"math/rand"
	"slices"
	"strings"
	"testing"

	"github.com/brendatamaar/yggdrasil/internal/core"
)

// Safety invariant tests for the worktree environment (docs/AGENTS.md
// section 6). Each one fails if the invariant is broken.

var allSources = map[string]Source{
	"file": SourceFile{Size: 10}, "missing": SourceMissing{}, "dir": SourceDirectory{},
	"link": SourceLink{}, "unreadable": SourceUnreadable{},
}

// TestInvariant_1_profile_copy_never_overwrites: exhaustive over every
// observation the shell can report, CopyFile is emitted only for a regular
// file whose destination is absent, and no command can delete anything.
func TestInvariant_1_profile_copy_never_overwrites(t *testing.T) {
	for name, src := range allSources {
		for _, dest := range []Dest{DestAbsent, DestExists} {
			t.Run(fmt.Sprintf("%s/%v", name, dest), func(t *testing.T) {
				j, cmds := Profile(JobIdle{}, CopyRequested{Worktree: "wt_1", Trigger: TriggerUserStarted, Entries: []RelPath{"a.env"}, Now: epoch})
				if len(cmds) != 1 {
					t.Fatalf("want one Observe, got %v", cmds)
				}
				_, cmds = Profile(j, Observed{Entry: "a.env", Source: src, Dest: dest})
				copies := 0
				for _, c := range cmds {
					switch c.(type) {
					case CopyFile:
						copies++
					case Observe, ProfileAudit, ProfilePublish:
					default:
						t.Fatalf("unexpected command %T: the closed set has no way to delete or overwrite", c)
					}
				}
				_, isFile := src.(SourceFile)
				if want := isFile && dest == DestAbsent; (copies == 1) != want {
					t.Fatalf("copies=%d, want copy=%v", copies, want)
				}
			})
		}
	}
}

// TestInvariant_1_compose_argv_never_destructive: exhaustive over every op
// the parser accepts, the argv holds only the fixed flags and no destructive
// one; every other op string is refused.
func TestInvariant_1_compose_argv_never_destructive(t *testing.T) {
	forbidden := []string{"-v", "--volumes", "--rmi", "--remove-orphans", "--force-recreate", "-f", "rm", "kill", "--timeout", "-t"}
	allowedFlags := []string{"-p", "--file", "-d", "--format"}
	for _, op := range []ComposeOp{OpUp, OpStop, OpDown, OpPs} {
		for _, file := range []string{"compose.yml", "deploy dir/compose.yaml"} {
			rel, err := ParseComposeFile(file)
			if err != nil {
				t.Fatal(err)
			}
			argv, err := Argv(op, "ygg-a", rel)
			if err != nil {
				t.Fatalf("%s: %v", op, err)
			}
			for i, a := range argv {
				if slices.Contains(forbidden, a) {
					t.Errorf("%s: forbidden argument %q", op, a)
				}
				if strings.HasPrefix(a, "-") && !slices.Contains(allowedFlags, a) {
					t.Errorf("%s: unexpected flag %q at %d", op, a, i)
				}
			}
			if argv[0] != "docker" || argv[1] != "compose" || argv[2] != "-p" || argv[3] != "ygg-a" || argv[4] != "--file" || argv[5] != string(rel) {
				t.Errorf("%s: prefix %v", op, argv[:6])
			}
		}
	}
	for _, bad := range []ComposeOp{"", "UP", "down -v", "down --volumes", "rm", "kill", "down\x00", "up -d --remove-orphans", "pull", "exec"} {
		if _, err := Argv(bad, "ygg-a", "compose.yml"); err == nil {
			t.Errorf("op %q must be refused", bad)
		}
	}
}

// TestInvariant_2_compose_up_needs_confirmed_hash: exhaustive over every
// state, confirmed hash and current hash, `up` runs only from Down or Failed
// when the confirmed hash equals the file's current hash.
func TestInvariant_2_compose_up_needs_confirmed_hash(t *testing.T) {
	for _, conf := range []string{"", "h1", "h2"} {
		states := []Service{Off{}, NeedsConfirm{FileHash: conf}, Down{Confirmed: conf},
			ComposeStarting{Confirmed: conf, Attempt: 1}, Up{Confirmed: conf, Attempt: 1},
			ComposeStopping{Confirmed: conf, Op: OpStop, Attempt: 1}, ComposeFailed{Confirmed: conf}}
		for _, s := range states {
			for _, cur := range []string{"", "h1", "h2"} {
				_, cmds := Compose(s, UpRequested{Hash: cur, Attempt: 2, Now: epoch})
				ran := len(runs(cmds)) > 0
				want := false
				switch s.(type) {
				case Down, ComposeFailed:
					want = conf != "" && conf == cur
				}
				if ran != want {
					t.Errorf("%s confirmed=%q current=%q: ran=%v want=%v", kind(s), conf, cur, ran, want)
				}
			}
		}
	}
}

// TestInvariant_2_any_file_change_needs_reconfirmation: after a confirmation, a
// change of the file never lets `up` run on the old confirmation, even when
// the change arrives while the project runs.
func TestInvariant_2_any_file_change_needs_reconfirmation(t *testing.T) {
	s, _ := Compose(Off{}, Enabled{Hash: "h1"})
	s, _ = Compose(s, Confirmed{Hash: "h1"})
	s, cmds := Compose(s, UpRequested{Hash: "h1", Attempt: 1, Now: epoch})
	if len(runs(cmds)) != 1 {
		t.Fatalf("confirmed file must run: %v", cmds)
	}
	s, _ = Compose(s, OpFinished{Attempt: 1, Now: epoch})
	s, _ = Compose(s, FileChanged{Hash: "h2"}) // edited while running
	s, cmds = Compose(s, StopRequested{Attempt: 2, Now: epoch})
	if len(runs(cmds)) != 1 {
		t.Fatal("stopping a running project stays possible")
	}
	s, _ = Compose(s, OpFinished{Attempt: 2, Now: epoch})
	s, cmds = Compose(s, UpRequested{Hash: "h2", Attempt: 3, Now: epoch})
	if len(runs(cmds)) != 0 {
		t.Fatalf("up ran on the old confirmation of h1 with file h2 (state %s)", kind(s))
	}
	// a confirmation of a stale hash does not help either
	s, _ = Compose(s, Confirmed{Hash: "h1"})
	if _, cmds = Compose(s, UpRequested{Hash: "h2", Attempt: 3, Now: epoch}); len(runs(cmds)) != 0 {
		t.Fatal("confirming a stale hash let up run")
	}
	s, _ = Compose(s, Confirmed{Hash: "h2"})
	if _, cmds = Compose(s, UpRequested{Hash: "h2", Attempt: 3, Now: epoch}); len(runs(cmds)) != 1 {
		t.Fatal("up must run after confirming the current hash")
	}
}

// TestInvariant_2_failed_state_keeps_the_hash_rule: a failed project is
// retried only with the confirmed hash too.
func TestInvariant_2_failed_state_keeps_the_hash_rule(t *testing.T) {
	var s Service = ComposeFailed{Confirmed: "h1", Kind: FailComposeError}
	if _, cmds := Compose(s, UpRequested{Hash: "h2", Attempt: 1, Now: epoch}); len(runs(cmds)) != 0 {
		t.Fatal("retry ran with a changed file")
	}
	s, _ = Compose(s, FileChanged{Hash: "h2"})
	if _, cmds := Compose(s, UpRequested{Hash: "h2", Attempt: 1, Now: epoch}); len(runs(cmds)) != 0 {
		t.Fatalf("retry after a file change ran (state %s)", kind(s))
	}
}

// TestInvariant_8_every_state_change_has_an_audit_command: random sequences
// for ports and compose; whenever the observable state changes, the step
// returns an audit command. A profile job that finishes always audits.
func TestInvariant_8_every_state_change_has_an_audit_command(t *testing.T) {
	t.Run("ports", func(t *testing.T) {
		forSeeds(t, func(t *testing.T, r *rand.Rand) {
			walk(t, r, func(before Table, in AllocInput, after Table, cmds []AllocCommand) {
				if _, isLoad := in.(Loaded); isLoad {
					return // startup load mirrors storage; only conflicts are audited
				}
				changed := before.Range != after.Range || len(before.Live) != len(after.Live)
				for w, b := range after.Live {
					if before.Live[w] != b {
						changed = true
					}
				}
				audited := false
				for _, c := range cmds {
					if _, ok := c.(AllocAudit); ok {
						audited = true
					}
				}
				if changed && !audited {
					t.Fatalf("%T changed the table without an audit command", in)
				}
			})
		})
	})
	t.Run("compose", func(t *testing.T) {
		walkCompose(t, func(prev Service, in ComposeInput, next Service, cmds []ComposeCommand) {
			if kind(prev) != kind(next) && !has[ComposeAudit](cmds) {
				t.Fatalf("%s -> %s on %T has no audit", kind(prev), kind(next), in)
			}
			if len(runs(cmds)) > 0 && !has[ComposeAudit](cmds) {
				t.Fatalf("an operation started on %T without audit", in)
			}
		})
	})
	t.Run("profile", func(t *testing.T) {
		j, _ := Profile(JobIdle{}, CopyRequested{Worktree: core.WorktreeID("wt_1"), Trigger: TriggerUserStarted, Entries: []RelPath{"a.env"}, Now: epoch})
		_, cmds := Profile(j, Observed{Entry: "a.env", Source: SourceMissing{}, Dest: DestAbsent})
		found := false
		for _, c := range cmds {
			if _, ok := c.(ProfileAudit); ok {
				found = true
			}
		}
		if !found {
			t.Fatal("a finished profile copy wrote no audit command")
		}
	})
}

// TestPropPortBlocksSizeAndConfiguredRange: with random range changes, every
// block handed out has 20 ports, lies in the range current at that moment and
// is disjoint from all live blocks (WE-02).
func TestPropPortBlocksSizeAndConfiguredRange(t *testing.T) {
	forSeeds(t, func(t *testing.T, r *rand.Rand) {
		walk(t, r, func(before Table, in AllocInput, after Table, _ []AllocCommand) {
			req, ok := in.(AllocateRequested)
			if !ok {
				return
			}
			if _, had := before.Live[req.Worktree]; had {
				return
			}
			b, got := after.Live[req.Worktree]
			if !got {
				return
			}
			if b.Size != core.PortBlockSize {
				t.Fatalf("block size %d, want %d", b.Size, core.PortBlockSize)
			}
			if b.Base < after.Range.Lo || b.Base+b.Size-1 > after.Range.Hi {
				t.Fatalf("block %v outside range %v", b, after.Range)
			}
			for w, o := range before.Live {
				if o.Base < b.Base+b.Size && b.Base < o.Base+o.Size {
					t.Fatalf("block %v overlaps %s %v", b, w, o)
				}
			}
		})
	})
}
