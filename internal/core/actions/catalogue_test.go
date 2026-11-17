package actions

import (
	"reflect"
	"slices"
	"strings"
	"testing"

	"github.com/brendatamaar/yggdrasil/internal/core"
)

func allReasons(mask int) []Reason {
	var out []Reason
	for i, r := range Reasons() {
		if mask&(1<<i) != 0 {
			out = append(out, r)
		}
	}
	return out
}

func capsOf(mask int) Capabilities {
	return Capabilities{
		HostLogs: mask&1 != 0, HostIssues: mask&2 != 0, HostMR: mask&4 != 0,
		CommentPost: mask&8 != 0, BuildLog: mask&16 != 0,
	}
}

func factsOf(reasons []Reason, readable bool, caps int, ids bool) Facts {
	f := Facts{Repo: "rep_1", Readable: readable, Reasons: reasons, Caps: capsOf(caps), Head: "h1"}
	if ids {
		f.CIRunID, f.CIHead, f.BuildID = "77", "c1", "88"
	}
	return f
}

// kindsFor lists the offered kinds of one reason.
func kindsFor(offs []Offering, r Reason) []Kind {
	var out []Kind
	for _, o := range offs {
		if o.Reason == r {
			out = append(out, o.Kind)
		}
	}
	return out
}

// Every attention reason that is present yields at least one offering that
// starts an agent turn or a proposal-producing action, for every combination
// of reasons, readability, host capabilities and known source ids (E6).
func TestPropEveryAttentionReasonHasAnAction(t *testing.T) {
	meaningful := map[Kind]bool{}
	for _, k := range Kinds() {
		meaningful[k] = k.Effect() != ""
	}
	for mask := 0; mask < 1<<len(Reasons()); mask++ {
		reasons := allReasons(mask)
		for _, readable := range []bool{true, false} {
			for caps := 0; caps < 32; caps++ {
				for _, ids := range []bool{true, false} {
					f := factsOf(reasons, readable, caps, ids)
					offs := Catalogue(f)
					for _, r := range reasons {
						ks := kindsFor(offs, r)
						if len(ks) == 0 {
							t.Fatalf("reason %s has no offering (facts %+v)", r, f)
						}
						for _, k := range ks {
							if !meaningful[k] {
								t.Fatalf("reason %s offers %s, which starts nothing", r, k)
							}
						}
					}
					checkOfferings(t, f, offs)
				}
			}
		}
	}
}

// checkOfferings holds the shape rules of every catalogue.
func checkOfferings(t *testing.T, f Facts, offs []Offering) {
	t.Helper()
	present := map[Reason]bool{}
	for _, r := range f.Reasons {
		present[r] = true
	}
	last := -1
	fixes := 0
	for _, o := range offs {
		if err := o.Kind.Validate(); err != nil {
			t.Fatalf("%v", err)
		}
		if o.Reason != "" && !present[o.Reason] {
			t.Fatalf("offering %s answers absent reason %s", o.Kind, o.Reason)
		}
		if o.Mode != o.Kind.Mode() {
			t.Fatalf("offering %s carries mode %q", o.Kind, o.Mode)
		}
		if o.Mode == core.ModeCode {
			fixes++
			if o.Kind != FixPipeline {
				t.Fatalf("%s is offered in Code mode", o.Kind)
			}
			if !slices.Contains(kindsFor(offs, o.Reason), DiagnosePipeline) {
				t.Fatalf("a fix is offered without its diagnosis: %+v", offs)
			}
		}
		// The offering must be requestable as it is: its source passes the parser.
		req := Request{RequestID: "req_1", RepoID: f.Repo, Kind: o.Kind, SourceID: o.SourceID, Agent: "mock"}
		if err := req.Validate(); err != nil {
			t.Fatalf("offering %+v cannot be requested: %v", o, err)
		}
		if o.Context == ContextMetadataOnly && o.Note == "" {
			t.Fatalf("a degraded context is not explained: %+v", o)
		}
		if !f.Readable && !o.WaitingForFolder {
			t.Fatalf("offering %+v does not say it waits for the folder", o)
		}
		if f.Readable && o.WaitingForFolder {
			t.Fatalf("offering %+v waits for a folder that is readable", o)
		}
		if o.Kind == PullAndCheck != o.OptionalVerify {
			t.Fatalf("optional verification on %+v", o)
		}
		if o.Reason != "" {
			idx := slices.Index(Reasons(), o.Reason)
			if idx < last {
				t.Fatalf("offerings are not in reason order: %+v", offs)
			}
			last = idx
		}
	}
	if fixes > 1 {
		t.Fatalf("%d fix offerings", fixes)
	}
	// Deterministic regardless of the order or repetition of the reasons.
	g := f
	g.Reasons = slices.Clone(f.Reasons)
	slices.Reverse(g.Reasons)
	g.Reasons = append(g.Reasons, f.Reasons...)
	if !reflect.DeepEqual(Catalogue(g), offs) {
		t.Fatalf("the catalogue depends on the order of reasons: %+v", f)
	}
}

// TestCatalogueCoversEveryReason mirrors the property with explicit expectations,
// so a wrong kind for a reason reads as a plain table failure.
func TestCatalogueCoversEveryReason(t *testing.T) {
	type want struct {
		kind Kind
		ctx  ContextAvailability
		src  string
	}
	full := 31
	cases := []struct {
		name     string
		facts    Facts
		reason   Reason
		want     []want
		waiting  bool
		noteHas  string
		wantNone bool
	}{
		{"failed CI with logs", factsOf(nil, true, full, true), ReasonFailedCI,
			[]want{{DiagnosePipeline, ContextAvailable, "77"}, {FixPipeline, ContextAvailable, "77"}}, false, "", false},
		{"failed CI without token", factsOf(nil, true, 0, true), ReasonFailedCI,
			[]want{{DiagnosePipeline, ContextMetadataOnly, "77"}, {FixPipeline, ContextMetadataOnly, "77"}}, false, "token", false},
		{"failed CI unidentified", factsOf(nil, true, full, false), ReasonFailedCI,
			[]want{{DiagnoseRepo, ContextMetadataOnly, ""}}, false, "isn't identified", false},
		{"failed build with log", factsOf(nil, true, full, true), ReasonFailedBuild,
			[]want{{DiagnoseBuild, ContextAvailable, "88"}}, false, "", false},
		{"failed build without log", factsOf(nil, true, 0, true), ReasonFailedBuild,
			[]want{{DiagnoseBuild, ContextMetadataOnly, "88"}}, false, "can't be read", false},
		{"failed build unidentified", factsOf(nil, true, full, false), ReasonFailedBuild,
			[]want{{DiagnoseRepo, ContextMetadataOnly, ""}}, false, "isn't identified", false},
		{"dirty", factsOf(nil, true, 0, false), ReasonDirty,
			[]want{{ProposeCommits, ContextAvailable, ""}}, false, "exact paths", false},
		{"dirty unreadable", factsOf(nil, false, 0, false), ReasonDirty,
			[]want{{ProposeCommits, ContextMetadataOnly, ""}}, true, "waits", false},
		{"behind", factsOf(nil, true, 0, false), ReasonBehind,
			[]want{{PullAndCheck, ContextNone, ""}}, false, "fast-forward", false},
		{"conflicts", factsOf(nil, true, 0, false), ReasonConflicts,
			[]want{{DiagnoseRepo, ContextMetadataOnly, ""}}, false, "will not reset, merge or delete", false},
		{"diverged", factsOf(nil, true, 0, false), ReasonDiverged,
			[]want{{DiagnoseRepo, ContextMetadataOnly, ""}}, false, "will not reset, merge or delete", false},
		{"unreadable", factsOf(nil, false, 0, false), ReasonUnreadable,
			[]want{{DiagnoseRepo, ContextMetadataOnly, ""}}, true, "valid folder", false},
	}
	for _, c := range cases {
		f := c.facts
		f.Reasons = []Reason{c.reason}
		got := Catalogue(f)
		if len(got) != len(c.want) {
			t.Fatalf("%s: %d offerings, want %d: %+v", c.name, len(got), len(c.want), got)
		}
		for i, w := range c.want {
			o := got[i]
			if o.Kind != w.kind || o.Context != w.ctx || o.SourceID != w.src || o.Reason != c.reason {
				t.Errorf("%s[%d] = %+v, want %+v", c.name, i, o, w)
			}
			if o.WaitingForFolder != c.waiting {
				t.Errorf("%s[%d] waiting = %v", c.name, i, o.WaitingForFolder)
			}
			if c.noteHas != "" && !strings.Contains(o.Note, c.noteHas) {
				t.Errorf("%s[%d] note %q lacks %q", c.name, i, o.Note, c.noteHas)
			}
		}
	}
	// Every reason is in the table.
	seen := map[Reason]bool{}
	for _, c := range cases {
		seen[c.reason] = true
	}
	for _, r := range Reasons() {
		if !seen[r] {
			t.Errorf("reason %s has no table case", r)
		}
	}
}

// A reason this package does not know still gets the safe repo diagnosis, and
// duplicates collapse.
func TestCatalogueUnknownReasonFallsBackToDiagnosis(t *testing.T) {
	f := factsOf([]Reason{"strange", "strange", ReasonBehind}, true, 0, false)
	got := Catalogue(f)
	if k := kindsFor(got, "strange"); !slices.Equal(k, []Kind{DiagnoseRepo}) {
		t.Fatalf("unknown reason offers %v", k)
	}
	if len(got) != 2 {
		t.Fatalf("offerings = %+v", got)
	}
}

func TestCatalogueNoReasonsNoOfferings(t *testing.T) {
	if got := Catalogue(Facts{Repo: "rep_1", Readable: true}); len(got) != 0 {
		t.Fatalf("a healthy repo is offered %+v", got)
	}
}

// Reason-less offerings: an open MR is reviewed in Ask mode with its pinned
// number, and noise offers an ignore proposal only at the threshold on a
// readable repo (WS-09).
func TestCatalogueMRAndNoise(t *testing.T) {
	f := Facts{Repo: "rep_1", Readable: true, MRNumber: 12, MRHead: "m1", Caps: Capabilities{HostMR: true, CommentPost: true}, Head: "h"}
	got := Catalogue(f)
	if len(got) != 1 || got[0].Kind != ReviewMR || got[0].SourceID != "12" || got[0].Mode != core.ModeAsk ||
		got[0].Context != ContextAvailable || got[0].Reason != "" {
		t.Fatalf("MR offering = %+v", got)
	}
	f.Caps = Capabilities{}
	got = Catalogue(f)
	if got[0].Context != ContextMetadataOnly || !strings.Contains(got[0].Note, "can't be read") || !strings.Contains(got[0].Note, "copied") {
		t.Fatalf("MR offering without host = %+v", got[0])
	}
	f.MRHead = ""
	if len(Catalogue(f)) != 0 {
		t.Error("an MR without a head is offered")
	}

	n := Facts{Repo: "rep_1", Readable: true, UntrackedCount: UntrackedNoiseThreshold - 1}
	if len(Catalogue(n)) != 0 {
		t.Error("ignore suggestions below the threshold")
	}
	n.UntrackedCount = UntrackedNoiseThreshold
	got = Catalogue(n)
	if len(got) != 1 || got[0].Kind != ProposeIgnore || got[0].Mode != core.ModeAsk || got[0].Reason != "" {
		t.Fatalf("noise offering = %+v", got)
	}
	n.Reasons = []Reason{ReasonDirty}
	got = Catalogue(n)
	if len(got) != 2 || got[0].Kind != ProposeCommits || got[1].Kind != ProposeIgnore || got[1].Reason != ReasonDirty {
		t.Fatalf("noise with dirty = %+v", got)
	}
	n.Readable = false
	for _, o := range Catalogue(n) {
		if o.Kind == ProposeIgnore {
			t.Error("ignore suggestions for an unreadable repo")
		}
	}
}

// A failed CI offers diagnosis (Ask) first and the fix (Code) as its own
// offering; a diagnosis never carries the Code mode.
func TestPropDiagnoseIsAlwaysAsk(t *testing.T) {
	diag := map[Kind]bool{DiagnosePipeline: true, DiagnoseBuild: true, DiagnoseRepo: true, ReviewMR: true, ProposeCommits: true, ProposeIgnore: true}
	for mask := 0; mask < 1<<len(Reasons()); mask++ {
		for caps := 0; caps < 32; caps++ {
			for _, readable := range []bool{true, false} {
				f := factsOf(allReasons(mask), readable, caps, true)
				f.MRNumber, f.MRHead, f.UntrackedCount = 3, "m", 500
				for _, o := range Catalogue(f) {
					if diag[o.Kind] && o.Mode != core.ModeAsk {
						t.Fatalf("%s is offered in mode %q", o.Kind, o.Mode)
					}
					if o.Mode == core.ModeCode && o.Kind != FixPipeline {
						t.Fatalf("%s is offered in Code mode", o.Kind)
					}
				}
			}
		}
	}
	for k := range diag {
		if k.Mode() != core.ModeAsk {
			t.Errorf("%s mode = %q", k, k.Mode())
		}
	}
}

// The pin is the value a submit is checked against.
func TestPin(t *testing.T) {
	f := Facts{Head: "h", CIHead: "c", MRHead: "m"}
	for k, want := range map[Kind]string{
		ReviewMR: "m", DiagnosePipeline: "c", FixPipeline: "c", DiagnoseRepo: "h", ProposeCommits: "h", PullAndCheck: "h",
	} {
		if got := Pin(f, k); got != want {
			t.Errorf("Pin(%s) = %q, want %q", k, got, want)
		}
	}
	f.CIHead = ""
	if Pin(f, DiagnosePipeline) != "h" {
		t.Error("a pipeline without a commit pins the repo head")
	}
}
