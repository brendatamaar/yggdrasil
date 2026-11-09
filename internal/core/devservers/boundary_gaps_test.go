package devservers

import (
	"errors"
	"fmt"
	"math/rand"
	"reflect"
	"slices"
	"strings"
	"testing"
	"time"

	"github.com/brendatamaar/yggdrasil/internal/core"
)

// Gap tests for the boundary parsers of docs/design/devservers.md: the
// rejection rules, round trips and fuzz targets that parse_test.go does not
// already pin.

// ---- NormalizePorts ----

func checkNormalized(t testing.TB, raw []int) {
	t.Helper()
	got, dropped := NormalizePorts(raw)
	if got == nil {
		t.Fatal("NormalizePorts returned nil")
	}
	if !validPorts(got) {
		t.Fatalf("NormalizePorts(%v) = %v: not sorted, unique and in range", raw, got)
	}
	valid := map[int]bool{}
	bad := 0
	for _, n := range raw {
		if n >= 1 && n <= MaxPort {
			valid[n] = true
		} else {
			bad++
		}
	}
	if dropped != bad {
		t.Fatalf("dropped = %d, want %d (input %v)", dropped, bad, raw)
	}
	if len(got) != len(valid) {
		t.Fatalf("kept %d ports, want %d distinct valid ones (input %v)", len(got), len(valid), raw)
	}
	for _, p := range got {
		if !valid[int(p)] {
			t.Fatalf("invented port %d from %v", p, raw)
		}
	}
	// Idempotent: normalising its own output changes nothing.
	again, d2 := NormalizePorts(toInts(got))
	if d2 != 0 || !slices.Equal(again, got) {
		t.Fatalf("not idempotent: %v -> %v", got, again)
	}
}

func toInts(ps []Port) []int {
	out := make([]int, len(ps))
	for i, p := range ps {
		out[i] = int(p)
	}
	return out
}

func TestNormalizePortsProperty(t *testing.T) {
	r := rand.New(rand.NewSource(7))
	for i := 0; i < 2000; i++ {
		raw := make([]int, r.Intn(12))
		for j := range raw {
			switch r.Intn(4) {
			case 0:
				raw[j] = r.Intn(10) - 3 // around zero
			case 1:
				raw[j] = 65530 + r.Intn(12) // around the top
			default:
				raw[j] = 3000 + r.Intn(4) // many duplicates
			}
		}
		checkNormalized(t, raw)
	}
}

func FuzzNormalizePorts(f *testing.F) {
	f.Add(0, 1, 65535, 65536)
	f.Add(-1, 3000, 3000, 80)
	f.Fuzz(func(t *testing.T, a, b, c, d int) { checkNormalized(t, []int{a, b, c, d}) })
}

// ---- OutsideBlock ----

func checkOutside(t testing.TB, ports []Port, block core.PortBlock) {
	t.Helper()
	got := OutsideBlock(ports, block)
	if block.Empty() {
		if got != nil {
			t.Fatalf("empty block: warnings %v, want none", got)
		}
		return
	}
	for _, p := range got {
		if block.Contains(int(p)) {
			t.Fatalf("warned about %d, which lies in %+v", p, block)
		}
		if !slices.Contains(ports, p) {
			t.Fatalf("warned about %d, which was not observed", p)
		}
	}
	if !slices.IsSorted(got) || len(slices.Compact(slices.Clone(got))) != len(got) {
		t.Fatalf("warnings %v are not sorted and unique", got)
	}
	// Completeness: every observed port outside the block is reported.
	for _, p := range ports {
		if !block.Contains(int(p)) && !slices.Contains(got, p) {
			t.Fatalf("%d is outside %+v but not reported (%v)", p, block, got)
		}
	}
}

func TestOutsideBlockProperty(t *testing.T) {
	r := rand.New(rand.NewSource(11))
	for i := 0; i < 2000; i++ {
		ports := make([]Port, r.Intn(8))
		for j := range ports {
			ports[j] = Port(19995 + r.Intn(30))
		}
		checkOutside(t, ports, blocks[r.Intn(len(blocks))])
	}
}

func FuzzOutsideBlock(f *testing.F) {
	f.Add(20000, 20000, 20)
	f.Add(1, 0, 0)
	f.Fuzz(func(t *testing.T, port, base, size int) {
		checkOutside(t, []Port{Port(port), Port(port + 1)}, core.PortBlock{Base: base, Size: size})
	})
}

// ---- ParseTemplate: the rules parse_test.go does not exercise ----

func TestParseTemplateRejectsMore(t *testing.T) {
	longArg := strings.Repeat("x", maxArgLen+1)
	manyEnv := map[string]string{}
	for i := 0; i <= MaxEnv; i++ {
		manyEnv[fmt.Sprintf("V%d", i)] = "1"
	}
	tests := []struct {
		name string
		argv []string
		env  map[string]string
	}{
		{"argument too long", []string{"a", longArg}, nil},
		{"program too long", []string{longArg}, nil},
		{"too many env vars", []string{"a"}, manyEnv},
		{"env value too long", []string{"a"}, map[string]string{"A": longArg}},
		{"empty env name", []string{"a"}, map[string]string{"": "1"}},
		{"env name with space", []string{"a"}, map[string]string{"A B": "1"}},
		{"env name starts with digit", []string{"a"}, map[string]string{"1A": "1"}},
		{"reserved PORT lower case", []string{"a"}, map[string]string{"port": "1"}},
		{"reserved YGG_PORT_BASE", []string{"a"}, map[string]string{"YGG_PORT_BASE": "1"}},
		{"reserved YGG_PORT_ prefix", []string{"a"}, map[string]string{"YGG_PORT_99": "1"}},
		{"secret SECRET", []string{"a"}, map[string]string{"APP_SECRET": "x"}},
		{"secret PASSWORD", []string{"a"}, map[string]string{"DB_PASSWORD": "x"}},
		{"secret suffix _KEY", []string{"a"}, map[string]string{"STRIPE_KEY": "x"}},
		{"secret API_KEY", []string{"a"}, map[string]string{"api_key": "x"}},
		{"secret mixed case token", []string{"a"}, map[string]string{"gitlab_Token": "x"}},
		{"secret CREDENTIAL", []string{"a"}, map[string]string{"AWS_CREDENTIALS": "x"}},
		{"secret PRIVATE_KEY", []string{"a"}, map[string]string{"SSH_PRIVATE_KEY": "x"}},
		{"DEL character", []string{"a", "b\x7fc"}, nil},
		{"C1 control character", []string{"a", "b\u0085c"}, nil},
		{"control in env value", []string{"a"}, map[string]string{"A": "x\ny"}},
		{"port index past the largest block", []string{"a", "{port:" + fmt.Sprint(core.PortBlockSize) + "}"}, nil},
		{"placeholder after a good one", []string{"a", "{port}", "{nope}"}, nil},
	}
	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			if got, err := ParseTemplate(tc.argv, tc.env); err == nil || !errors.Is(err, ErrBadTemplate) {
				t.Fatalf("accepted %#v (err %v)", got, err)
			}
		})
	}
	// Boundaries that are still fine.
	ok := make([]string, MaxArgs)
	for i := range ok {
		ok[i] = "a"
	}
	if _, err := ParseTemplate(ok, nil); err != nil {
		t.Errorf("exactly MaxArgs arguments: %v", err)
	}
	if _, err := ParseTemplate([]string{"a", strings.Repeat("x", maxArgLen)}, nil); err != nil {
		t.Errorf("an argument of exactly the limit: %v", err)
	}
	last := fmt.Sprintf("{port:%d}", core.PortBlockSize-1)
	if _, err := ParseTemplate([]string{"a", last}, nil); err != nil {
		t.Errorf("the last index of the largest block: %v", err)
	}
}

// Every accepted template survives Format and ParseTemplate unchanged, and
// expands inside any block big enough for its indexes.
func TestParseTemplateRoundTripProperty(t *testing.T) {
	r := rand.New(rand.NewSource(3))
	atoms := []string{"npm", "run", "dev", "--port", "{port}", "{port:0}", "{port:7}", "{port:19}", "--host=127.0.0.1", "${HOME}", `{"a":1}`, "{x}", "{port:20}", "{port:01}", "a\x00b", "", " "}
	envs := []map[string]string{nil, {"NODE_ENV": "development"}, {"PORT": "1"}, {"MY_TOKEN": "x"}, {"A": "b", "C": "d"}}
	var accepted, rejected int
	for i := 0; i < 3000; i++ {
		argv := make([]string, r.Intn(6))
		for j := range argv {
			argv[j] = atoms[r.Intn(len(atoms))]
		}
		tpl, err := ParseTemplate(argv, envs[r.Intn(len(envs))])
		if err != nil {
			rejected++
			if !errors.Is(err, ErrBadTemplate) {
				t.Fatalf("%v: error does not wrap ErrBadTemplate", argv)
			}
			continue
		}
		accepted++
		again, err := ParseTemplate(Format(tpl))
		if err != nil || !reflect.DeepEqual(again, tpl) {
			t.Fatalf("round trip of %#v: %#v, %v", tpl, again, err)
		}
		exp, err := Expand(tpl, core.PortBlock{Base: 30000, Size: core.PortBlockSize})
		if err != nil {
			t.Fatalf("accepted %#v does not expand: %v", tpl, err)
		}
		for _, a := range exp.Argv {
			if strings.Contains(a, "{port") {
				t.Fatalf("placeholder left in %q", a)
			}
		}
	}
	if accepted < 100 || rejected < 100 {
		t.Fatalf("generator is lopsided: %d accepted, %d rejected", accepted, rejected)
	}
}

// ---- Expand ----

func TestExpandRejectsABadBlock(t *testing.T) {
	for _, b := range []core.PortBlock{{Base: 0, Size: 5}, {Base: 65535, Size: 20}, {Base: -4, Size: 5}} {
		if _, err := Expand(Template{Argv: []string{"x"}}, b); !errors.Is(err, ErrBadTemplate) {
			t.Errorf("block %+v: err = %v, want ErrBadTemplate", b, err)
		}
	}
}

func TestExpandDoesNotAliasItsInput(t *testing.T) {
	tpl := Template{Argv: []string{"x", "{port}"}, Env: map[string]string{"A": "1"}}
	exp, err := Expand(tpl, core.PortBlock{Base: 100, Size: 5})
	if err != nil {
		t.Fatal(err)
	}
	exp.Argv[0], exp.Env["A"] = "changed", "2"
	if tpl.Argv[0] != "x" || tpl.Argv[1] != "{port}" || tpl.Env["A"] != "1" {
		t.Errorf("Expand shares memory with the template: %#v", tpl)
	}
}

// The environment Expand builds is exactly the template's plus PORT,
// YGG_PORT_BASE and YGG_PORT_0..Size-1 (D11, D12).
func TestExpandEnvKeysAreExactlyThePortFamily(t *testing.T) {
	for _, b := range blocks {
		exp, err := Expand(Template{Argv: []string{"x"}, Env: map[string]string{"NODE_ENV": "dev"}}, b)
		if err != nil {
			if b.Validate() != nil && !b.Empty() {
				continue // an invalid non-empty block is refused (TestExpandRejectsABadBlock)
			}
			t.Fatal(err)
		}
		want := 1
		if !b.Empty() {
			want += 2 + b.Size
		}
		if len(exp.Env) != want {
			t.Errorf("block %+v: %d env vars, want %d", b, len(exp.Env), want)
		}
		for k, v := range exp.Env {
			if secretName(k) || strings.ContainsAny(v, "/\\") {
				t.Errorf("env %s=%q looks like a secret or a path", k, v)
			}
		}
	}
}

// ---- ParseAnnouncement ----

// Round trip: a port formatted into each recognised pattern parses back.
func TestParseAnnouncementRoundTrip(t *testing.T) {
	hosts := []string{"localhost", "127.0.0.1", "[::1]"}
	lines := []string{"%s:%d", "  Local: http://%s:%d/", "Now listening on: http://%s:%d", "INFO: Uvicorn running on http://%s:%d (Press CTRL+C to quit)", "\x1b[32m%s:%d\x1b[0m"}
	for _, host := range hosts {
		for _, port := range []int{1, 80, 3000, 5173, 20019, 65535} {
			for _, l := range lines {
				line := fmt.Sprintf(l, host, port)
				if got := ParseAnnouncement(line); !slices.Equal(got, []Port{Port(port)}) {
					t.Errorf("ParseAnnouncement(%q) = %v, want [%d]", line, got, port)
				}
			}
		}
	}
}

func TestParseAnnouncementRejectionRules(t *testing.T) {
	tests := []struct{ name, line string }{
		{"port zero", "localhost:0"},
		{"port above range", "127.0.0.1:65536"},
		{"six digits", "localhost:100000"},
		{"all-interfaces host", "http://0.0.0.0:3000"},
		{"public ipv4", "http://203.0.113.7:3000"},
		{"public name", "http://example.test:3000"},
		{"subdomain of localhost", "http://app.localhost:3000"},
		{"prefixed host", "xlocalhost:3000"},
		{"suffixed host name", "localhost.example.test:3000"},
		{"port followed by a letter", "localhost:3000x"},
		{"no port", "localhost:/path"},
		{"no colon", "localhost 3000"},
		{"ipv6 non loopback", "[::2]:3000"},
		{"bare number", "3000"},
		{"truncated ansi", "\x1b[1"},
	}
	for _, tc := range tests {
		if got := ParseAnnouncement(tc.line); got != nil {
			t.Errorf("%s: ParseAnnouncement(%q) = %v, want nothing", tc.name, tc.line, got)
		}
	}
}

// A line is cut at maxLine before it is read, so a long line cannot make the
// parser slow or report a port that starts past the cut.
func TestParseAnnouncementIgnoresPastTheLineLimit(t *testing.T) {
	pad := strings.Repeat(" ", maxLine)
	if got := ParseAnnouncement(pad + "localhost:3000"); got != nil {
		t.Errorf("port after the limit reported: %v", got)
	}
	if got := ParseAnnouncement("localhost:3000" + pad); !slices.Equal(got, []Port{3000}) {
		t.Errorf("port before the limit lost: %v", got)
	}
}

func TestParseAnnouncementResultIsNilOrSortedUnique(t *testing.T) {
	r := rand.New(rand.NewSource(5))
	hosts := []string{"localhost", "127.0.0.1", "[::1]", "LOCALHOST", "0.0.0.0", "evil.localhost"}
	for i := 0; i < 2000; i++ {
		var b strings.Builder
		for j := 0; j < r.Intn(5); j++ {
			fmt.Fprintf(&b, "%s:%d ", hosts[r.Intn(len(hosts))], r.Intn(70000))
		}
		got := ParseAnnouncement(b.String())
		if got != nil && !validPorts(got) {
			t.Fatalf("%q -> %v", b.String(), got)
		}
	}
}

// ---- ServerFromRow ----

func TestServerFromRowRejectsMore(t *testing.T) {
	stopping := RowOf(testKey, Stopping{Attempt: 1, Cause: CauseUserStop, KillAttempt: 1, Since: rowTime, Pid: 3})
	starting := RowOf(testKey, Starting{Attempt: 1, Since: rowTime})
	failed := RowOf(testKey, Failed{Attempt: 1, Kind: FailCrashed, Code: 2, At: rowTime})
	orphan := RowOf(testKey, Orphaned{Attempt: 1, Pid: 4, SeenAt: rowTime})
	live := RowOf(testKey, Running{Attempt: 1, Since: rowTime, Pid: 9, Ports: []Port{80}, Announced: []Port{}})
	rest := RowOf(testKey, Stopped{Attempt: 1, Last: LastUserStopped, At: rowTime})
	tests := []struct {
		name string
		row  Row
		edit func(*Row)
	}{
		{"starting attempt 0", starting, func(r *Row) { r.Attempt = 0 }},
		{"starting without time", starting, func(r *Row) { r.Since = time.Time{} }},
		{"starting with announced ports", starting, func(r *Row) { r.Announced = []Port{80} }},
		{"starting with kill attempt", starting, func(r *Row) { r.KillAttempt = 1 }},
		{"stopping attempt 0", stopping, func(r *Row) { r.Attempt = 0 }},
		{"stopping without pid", stopping, func(r *Row) { r.Pid = 0 }},
		{"stopping unknown cause", stopping, func(r *Row) { r.StopCause = "later" }},
		{"stopping with ports", stopping, func(r *Row) { r.Ports = []Port{80} }},
		{"stopping with fail kind", stopping, func(r *Row) { r.FailKind = FailCrashed }},
		{"failed attempt negative", failed, func(r *Row) { r.Attempt = -2 }},
		{"failed with a pid", failed, func(r *Row) { r.Pid = 5 }},
		{"failed with ports", failed, func(r *Row) { r.Ports = []Port{80} }},
		{"failed with a kill attempt", failed, func(r *Row) { r.KillAttempt = 2 }},
		{"failed with a stop cause", failed, func(r *Row) { r.StopCause = CauseUserStop }},
		{"orphaned with ports", orphan, func(r *Row) { r.Ports = []Port{80} }},
		{"orphaned with a fail kind", orphan, func(r *Row) { r.FailKind = FailCrashed }},
		{"orphaned without time", orphan, func(r *Row) { r.Since = time.Time{} }},
		{"running unsorted announced", live, func(r *Row) { r.Announced = []Port{9, 8} }},
		{"running negative pid", live, func(r *Row) { r.Pid = -3 }},
		{"stopped with kill attempt", rest, func(r *Row) { r.KillAttempt = 1 }},
		{"stopped with stop cause", rest, func(r *Row) { r.StopCause = CauseRestart }},
		{"empty status", live, func(r *Row) { r.Status = "" }},
		{"status in the wrong case", live, func(r *Row) { r.Status = "Running" }},
	}
	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			r := tc.row
			tc.edit(&r)
			if got, err := ServerFromRow(r); !errors.Is(err, ErrBadRow) {
				t.Fatalf("accepted %#v (err %v)", got, err)
			}
		})
	}
}

// A live server needs Attempt >= 1 (design: ServerFromRow rejects "Attempt < 1
// live"), whatever the state.
func TestServerFromRowLiveNeedsAnAttempt(t *testing.T) {
	for _, s := range sampleServers() {
		switch s.(type) {
		case Starting, Running, Stopping, Orphaned:
		default:
			continue
		}
		row := RowOf(testKey, s)
		row.Attempt = 0
		if _, err := ServerFromRow(row); !errors.Is(err, ErrBadRow) {
			t.Errorf("%T with attempt 0: %v", s, err)
		}
	}
}

// Accepted ⇒ valid, and the second pass through RowOf is a fixed point.
func TestServerFromRowRandomRowsAcceptedAreValid(t *testing.T) {
	r := rand.New(rand.NewSource(13))
	statuses := []string{"stopped", "starting", "running", "stopping", "failed", "orphaned", "x"}
	var ok int
	for i := 0; i < 20000; i++ {
		row := Row{
			Status: statuses[r.Intn(len(statuses))], Attempt: r.Intn(3), Pid: r.Intn(3),
			Ports: []Port{}, Announced: []Port{},
		}
		if r.Intn(2) == 0 {
			row.Since = rowTime
		}
		if r.Intn(2) == 0 {
			row.Block = blocks[r.Intn(len(blocks))]
		}
		if r.Intn(3) == 0 {
			row.Ports = randPorts(r)
		}
		row.LastKind = []LastKind{"", LastNever, LastExited, LastUserStopped}[r.Intn(4)]
		row.FailKind = []FailKind{"", FailCrashed, FailStartFailed, FailStopFailed}[r.Intn(4)]
		row.StopCause = []StopCause{"", CauseUserStop, CauseRestart}[r.Intn(3)]
		row.KillAttempt = KillAttempt(r.Intn(3))
		s, err := ServerFromRow(row)
		if err != nil {
			if !errors.Is(err, ErrBadRow) {
				t.Fatalf("error does not wrap ErrBadRow: %v", err)
			}
			continue
		}
		ok++
		switch st := s.(type) {
		case Running:
			if st.Pid < 1 || st.Attempt < 1 || !validPorts(st.Ports) || !validPorts(st.Announced) {
				t.Fatalf("bad Running: %#v", st)
			}
		case Stopping:
			if st.Pid < 1 || st.Attempt < 1 || st.Cause == "" || st.KillAttempt.Validate() != nil {
				t.Fatalf("bad Stopping: %#v", st)
			}
		case Starting:
			if st.Attempt < 1 {
				t.Fatalf("Starting without attempt: %#v", st)
			}
		case Orphaned:
			if st.Pid < 1 || st.Attempt < 1 {
				t.Fatalf("bad Orphaned: %#v", st)
			}
		}
		again, err := ServerFromRow(RowOf(testKey, s))
		if err != nil || !reflect.DeepEqual(again, s) {
			t.Fatalf("not a fixed point: %#v -> %#v (%v)", s, again, err)
		}
	}
	if ok < 20 {
		t.Fatalf("generator accepted only %d rows", ok)
	}
}
