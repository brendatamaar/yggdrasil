package proposals

import (
	"encoding/json"
	"errors"
	"reflect"
	"strings"
	"testing"

	"github.com/brendatamaar/yggdrasil/internal/core"
)

var (
	head40 = strings.Repeat("a", 40)
	hash64 = strings.Repeat("b", 64)
	mark16 = strings.Repeat("c", 16)
)

func phase6Actions() []Action {
	return []Action{
		IgnoreEntries{Where: MainCheckout{Repo: "rep_1"}, Entries: []string{"/node_modules/", "/web/dist/"}, ExpectedHash: hash64},
		IgnoreEntries{Where: MainCheckout{Repo: "rep_1"}, Entries: []string{"/bin/"}, ExpectedAbsent: true},
		ReviewComment{Repo: "rep_1", MRNumber: 7, ExpectedHead: head40, Body: "Looks fine.\nOne nit.", Marker: mark16},
		InitRepo{Operation: "op_1", Project: "prj_1", Repo: "rep_2", Leaf: "shop-new", Branch: "main", README: "# x\n", Ignore: "/bin/\n", CommitMessage: "Initial commit"},
		PublishRepo{Operation: "op_1", Repo: "rep_2", HostInstance: "github.com", Owner: "acme", OwnerKind: "org", Name: "shop-new", Visibility: "private", Branch: "main", ExpectedHead: head40, Marker: mark16},
		PullFF{Repo: "rep_1", Branch: "main", Upstream: "origin/main", ExpectHead: head40, Verify: true},
	}
}

// Every Phase 6 action validates, round-trips through its stored row, parses
// again with the defined-kind parser, has Describe text and a stable hash.
func TestPhase6ActionsRoundTripAndHash(t *testing.T) {
	seen := map[string]bool{}
	for _, a := range phase6Actions() {
		if err := a.Validate(); err != nil {
			t.Fatalf("%T: %v", a, err)
		}
		kind, payload := RowOf(a)
		if kind != a.Kind() || len(payload) == 0 {
			t.Fatalf("%T: RowOf = %q, %q", a, kind, payload)
		}
		back, err := ParseDefinedAction(string(kind), payload)
		if err != nil {
			t.Fatalf("%T: parse own row: %v", a, err)
		}
		if !reflect.DeepEqual(a, back) {
			t.Errorf("%T: round trip changed it:\n got %#v\nwant %#v", a, back, a)
		}
		if h := Hash(a); len(h) != 64 || h != Hash(back) {
			t.Errorf("%T: hash %q is not stable", a, h)
		} else if seen[h] {
			t.Errorf("%T: hash collides with another action", a)
		} else {
			seen[h] = true
		}
		if lines := Describe(a); len(lines) == 0 {
			t.Errorf("%T: Describe is empty", a)
		}
	}
}

// Every field is covered by the hash: changing any one changes it.
func TestPhase6HashCoversEveryField(t *testing.T) {
	for _, a := range phase6Actions() {
		base := Hash(a)
		rv := reflect.ValueOf(a)
		for i := 0; i < rv.NumField(); i++ {
			mut := reflect.New(rv.Type()).Elem()
			mut.Set(rv)
			f := mut.Field(i)
			switch f.Kind() {
			case reflect.String:
				f.SetString(f.String() + "x")
			case reflect.Int:
				f.SetInt(f.Int() + 1)
			case reflect.Bool:
				f.SetBool(!f.Bool())
			case reflect.Slice:
				f.Set(reflect.Append(f, reflect.ValueOf("/extra/")))
			case reflect.Interface:
				f.Set(reflect.ValueOf(MainCheckout{Repo: "rep_other"}))
			default:
				t.Fatalf("%T.%s: unhandled kind %s", a, rv.Type().Field(i).Name, f.Kind())
			}
			if Hash(mut.Interface().(Action)) == base {
				t.Errorf("%T: field %s is not covered by the hash", a, rv.Type().Field(i).Name)
			}
		}
	}
}

// The Phase 6 kinds are defined but not Enabled: ParseAction, ActionFromRow
// and Step refuse them, so no proposal that cannot run is ever created.
func TestPhase6KindsAreNotEnabledYet(t *testing.T) {
	for _, a := range phase6Actions() {
		k := a.Kind()
		if Enabled(k) {
			t.Errorf("%s is enabled before its executor exists", k)
		}
		_, payload := RowOf(a)
		if _, err := ParseAction(string(k), payload); !errors.Is(err, ErrUnsupportedKind) {
			t.Errorf("ParseAction(%s) = %v, want ErrUnsupportedKind", k, err)
		}
		if _, err := ActionFromRow(k, payload); !errors.Is(err, ErrUnsupportedKind) {
			t.Errorf("ActionFromRow(%s) = %v, want ErrUnsupportedKind", k, err)
		}
		p := Proposal{ID: "prp_1", ChangeSet: "cs_1", Task: "tsk_1", Repo: "rep_1", Source: UserSource{}, Action: a}
		got, cmds := Step(Proposal{}, Created{Proposal: p, Facts: Facts{RepoTracked: true, PathsOK: true, RemoteSupported: true}})
		if got.State != nil || len(cmds) != 1 {
			t.Fatalf("Step(Created %s) = %+v, %v", k, got, cmds)
		}
		if r, ok := cmds[0].(Refuse); !ok || r.Reason != RefuseUnsupported {
			t.Errorf("Step(Created %s) = %v, want Refuse{unsupported}", k, cmds[0])
		}
	}
	for _, k := range []core.ProposalKind{core.ProposalCommit, core.ProposalPush, core.ProposalInstall} {
		if !Enabled(k) {
			t.Errorf("%s was disabled", k)
		}
	}
	if _, err := ParseDefinedAction(string(core.ProposalSubtasks), []byte(`{}`)); !errors.Is(err, ErrUnsupportedKind) {
		t.Errorf("subtasks parsed: %v", err)
	}
}

func TestPhase6ParsersReject(t *testing.T) {
	good := func(a Action) map[string]any {
		_, payload := RowOf(a)
		var m map[string]any
		if err := json.Unmarshal(payload, &m); err != nil {
			t.Fatal(err)
		}
		return m
	}
	ignore := good(phase6Actions()[0])
	comment := good(phase6Actions()[2])
	initr := good(phase6Actions()[3])
	pub := good(phase6Actions()[4])
	pull := good(phase6Actions()[5])
	with := func(m map[string]any, k string, v any) map[string]any {
		out := map[string]any{}
		for key, val := range m {
			out[key] = val
		}
		if v == nil {
			delete(out, k)
		} else {
			out[k] = v
		}
		return out
	}
	cases := []struct {
		name string
		kind core.ProposalKind
		args map[string]any
	}{
		{"ignore: wildcard", core.ProposalIgnoreEntries, with(ignore, "entries", []string{"*"})},
		{"ignore: unanchored", core.ProposalIgnoreEntries, with(ignore, "entries", []string{"node_modules/"})},
		{"ignore: unescaped glob", core.ProposalIgnoreEntries, with(ignore, "entries", []string{"/a*b/"})},
		{"ignore: parent", core.ProposalIgnoreEntries, with(ignore, "entries", []string{"/../x/"})},
		{"ignore: git dir", core.ProposalIgnoreEntries, with(ignore, "entries", []string{"/.GIT/"})},
		{"ignore: newline", core.ProposalIgnoreEntries, with(ignore, "entries", []string{"/a\n/"})},
		{"ignore: duplicate", core.ProposalIgnoreEntries, with(ignore, "entries", []string{"/a/", "/a/"})},
		{"ignore: none", core.ProposalIgnoreEntries, with(ignore, "entries", []string{})},
		{"ignore: no expectation", core.ProposalIgnoreEntries, with(ignore, "expected_hash", nil)},
		{"ignore: absent with hash", core.ProposalIgnoreEntries, with(with(ignore, "expected_absent", true), "expected_hash", hash64)},
		{"ignore: other file", core.ProposalIgnoreEntries, with(ignore, "path", ".bashrc")},
		{"comment: no marker", core.ProposalReviewComment, with(comment, "marker", "")},
		{"comment: short head", core.ProposalReviewComment, with(comment, "expect_head", "abc1234")},
		{"comment: empty body", core.ProposalReviewComment, with(comment, "body", "  ")},
		{"comment: control", core.ProposalReviewComment, with(comment, "body", "a\x00b")},
		{"comment: MR zero", core.ProposalReviewComment, with(comment, "mr_number", 0)},
		{"init: nested leaf", core.ProposalInitRepo, with(initr, "leaf", "a/b")},
		{"init: traversal", core.ProposalInitRepo, with(initr, "leaf", "..")},
		{"init: stream", core.ProposalInitRepo, with(initr, "leaf", "a:b")},
		{"init: device", core.ProposalInitRepo, with(initr, "leaf", "NUL")},
		{"init: other branch", core.ProposalInitRepo, with(initr, "branch", "develop")},
		{"init: empty message", core.ProposalInitRepo, with(initr, "commit_message", " ")},
		{"publish: public typo", core.ProposalPublish, with(pub, "visibility", "everyone")},
		{"publish: other branch", core.ProposalPublish, with(pub, "branch", "dev")},
		{"publish: url owner", core.ProposalPublish, with(pub, "owner", "evil/x")},
		{"publish: host with userinfo", core.ProposalPublish, with(pub, "host", "u@github.com")},
		{"publish: short head", core.ProposalPublish, with(pub, "expect_head", "abc")},
		{"pull: no upstream slash", core.ProposalPullFF, with(pull, "upstream", "origin")},
		{"pull: option-like remote", core.ProposalPullFF, with(pull, "upstream", "-x/main")},
		{"pull: unknown field", core.ProposalPullFF, with(pull, "force", true)},
	}
	for _, c := range cases {
		b, _ := json.Marshal(c.args)
		if _, err := ParseDefinedAction(string(c.kind), b); err == nil {
			t.Errorf("%s was accepted", c.name)
		}
	}
	if _, err := ParseDefinedAction("review_comment", []byte(`{"repo":"rep_1"} {}`)); err == nil {
		t.Error("trailing data was accepted")
	}
}

// Describe shows the exact destination and content of each action.
func TestPhase6DescribeIsExact(t *testing.T) {
	text := func(a Action) string {
		var b strings.Builder
		for _, l := range Describe(a) {
			b.WriteString(l.Text + "\n")
		}
		return b.String()
	}
	as := phase6Actions()
	for _, want := range []string{"/node_modules/", "/web/dist/", hash64} {
		if !strings.Contains(text(as[0]), want) {
			t.Errorf("ignore description lacks %q", want)
		}
	}
	if !strings.Contains(text(as[1]), "does not exist yet") {
		t.Error("ignore (absent) description does not say the file will be created")
	}
	for _, want := range []string{"!7", head40, mark16, "Looks fine."} {
		if !strings.Contains(text(as[2]), want) {
			t.Errorf("comment description lacks %q", want)
		}
	}
	if !strings.Contains(text(as[3]), "shop-new") || !strings.Contains(text(as[3]), "Initial commit") {
		t.Error("init description lacks the folder or message")
	}
	pubText := text(as[4])
	for _, want := range []string{"private", "acme/shop-new", "github.com", head40, "never forced"} {
		if !strings.Contains(pubText, want) {
			t.Errorf("publish description lacks %q", want)
		}
	}
	pub := as[4].(PublishRepo)
	pub.Visibility = "public"
	if !strings.Contains(text(pub), "PUBLIC") {
		t.Error("a public publish does not say so")
	}
	if !strings.Contains(text(as[5]), "fast-forward") || !strings.Contains(text(as[5]), "origin/main") {
		t.Error("pull description lacks the fast-forward or upstream")
	}
}

func FuzzParseDefinedAction(f *testing.F) {
	for _, a := range phase6Actions() {
		k, p := RowOf(a)
		f.Add(string(k), string(p))
	}
	f.Fuzz(func(t *testing.T, kind, payload string) {
		a, err := ParseDefinedAction(kind, []byte(payload))
		if err != nil {
			return
		}
		// accepted => valid, and its own row parses to the same hash.
		if err := a.Validate(); err != nil {
			t.Fatalf("accepted but invalid: %v", err)
		}
		k, p := RowOf(a)
		b, err := ParseDefinedAction(string(k), p)
		if err != nil || Hash(a) != Hash(b) {
			t.Fatalf("row of an accepted action does not round trip: %v", err)
		}
	})
}
