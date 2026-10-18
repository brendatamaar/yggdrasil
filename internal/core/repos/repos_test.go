package repos

import (
	"strings"
	"testing"
)

// Every variant of every closed set implements its sealed interface.
var (
	_ = []Life{Active{}, Untracked{}}
	_ = []Input{TrackProject{}, TrackRepos{}, UntrackRepo{}, UntrackProject{}, RenameProject{}}
	_ = []Command{InsertProject{}, InsertRepo{}, RestoreProject{}, RestoreRepo{}, MarkRepoUntracked{},
		MarkProjectUntracked{}, UpdateProject{}, WriteAudit{}, PublishReposChanged{}, RefreshStatus{}}
	_ = []Outcome{Tracked{}, AlreadyTracked{}, NameTaken{}, Overlaps{}, RepoBusy{}, NotFound{},
		NothingPicked{}, UntrackDone{}, Renamed{}}
	_ = []View{Unknown{}, Fresh{}, Problem{}}
	_ = []RefreshInput{ReposChanged{}, RefreshRequested{}, Tick{}, StatusRead{}, StatusFailed{}}
	_ = []RefreshCommand{ReadStatus{}, PublishStatus{}, PublishCounts{}}
)

func TestEnumsAcceptEveryConstantAndRejectOthers(t *testing.T) {
	type enum interface{ Validate() error }
	good := []enum{
		BusyBuildRunning, BusyTaskRunning,
		ProblemMissing, ProblemNotARepo, ProblemTimedOut, ProblemGitFailed,
		FlagMissing, FlagNotARepo, FlagTimedOut, FlagGitFailed, FlagDetached, FlagNoUpstream, FlagConflicts, FlagDiverged,
		FilterAll, FilterUncommitted, FilterBehind, FilterAhead, FilterFailedPipeline, FilterProblems,
		ReasonUser, ReasonFocus, ReasonGitAction, ReasonBuild, ReasonTracking, ReasonTick,
	}
	for _, e := range good {
		if err := e.Validate(); err != nil {
			t.Errorf("%T %q rejected: %v", e, e, err)
		}
	}
	bad := []enum{
		BusyReason(""), BusyReason("Build_Running"), ProblemKind(""), ProblemKind("other"),
		Flag(""), Flag("Detached"), Filter(""), Filter("everything"), RefreshReason(""), RefreshReason("sometime"),
	}
	for _, e := range bad {
		if e.Validate() == nil {
			t.Errorf("%T %q accepted", e, e)
		}
	}
}

func TestNewCleanPath(t *testing.T) {
	for _, p := range []string{"/", "/work/acme-shop", `C:\work\acme shop`, "C:/work", `c:\`, `\\nas\share\x`, `\\nas\share`} {
		got, err := NewCleanPath(p)
		if err != nil || got.String() != p {
			t.Errorf("NewCleanPath(%q) = %q, %v", p, got, err)
		}
	}
	bad := map[string]string{
		"empty": "", "relative": "work/x", "dot": "./x", "drive relative": `C:x`, "single backslash": `\work`,
		"trailing slash": "/work/", "trailing backslash": `C:\work\`, "dotdot": "/work/../x", "dot segment": "/work/./x",
		"empty segment": "/work//x", "NUL": "/work/\x00", "newline": "/work/\n", "extended": `\\?\C:\work`,
		"device": `\\.\pipe\x`, "nt": `\??\C:\x`, "unc without share": `\\nas`,
	}
	for name, p := range bad {
		if got, err := NewCleanPath(p); err == nil {
			t.Errorf("%s: NewCleanPath(%q) = %q, want an error", name, p, got)
		}
	}
}

func TestNewRelPath(t *testing.T) {
	for _, p := range []string{".", "a", "a/b", "services/shop-api", "a b/c"} {
		got, err := NewRelPath(p)
		if err != nil || got.String() != p {
			t.Errorf("NewRelPath(%q) = %q, %v", p, got, err)
		}
	}
	for _, p := range []string{"", "/a", "a/", "a//b", "a/./b", "a/../b", "..", `a\b`, "C:/a", "a\x00", "a\nb", "./a"} {
		if got, err := NewRelPath(p); err == nil {
			t.Errorf("NewRelPath(%q) = %q, want an error", p, got)
		}
	}
}

func TestValidName(t *testing.T) {
	for _, s := range []string{"shop-api", "a", strings.Repeat("é", MaxNameRunes)} {
		if err := ValidName(s); err != nil {
			t.Errorf("%q rejected: %v", s, err)
		}
	}
	for _, s := range []string{"", "  ", strings.Repeat("a", MaxNameRunes+1), "a\tb", "a\x00"} {
		if ValidName(s) == nil {
			t.Errorf("%q accepted", s)
		}
	}
}

// The placeholders change nothing until batch B1 lands the logic.
func TestPlaceholdersAreInert(t *testing.T) {
	ws := Workspace{}
	got, cmds, outs := Run(ws, []Input{UntrackRepo{RepoID: "r1"}})
	if len(got.Projects) != 0 || len(cmds) != 0 || len(outs) != 1 {
		t.Fatalf("Run = %#v, %v, %v", got, cmds, outs)
	}
	r := Refresher{Max: DefaultMax}
	r2, rc := RunRefresh(r, []RefreshInput{Tick{}})
	if r2.Max != DefaultMax || len(rc) != 0 {
		t.Fatalf("RunRefresh = %#v, %v", r2, rc)
	}
}
