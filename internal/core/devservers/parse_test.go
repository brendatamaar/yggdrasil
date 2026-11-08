package devservers

import (
	"errors"
	"reflect"
	"slices"
	"strings"
	"testing"
	"time"

	"github.com/brendatamaar/yggdrasil/internal/core"
)

func TestParseTemplateRoundTrip(t *testing.T) {
	for _, tpl := range templates[:3] {
		got, err := ParseTemplate(Format(tpl))
		if err != nil {
			t.Fatalf("%v: %v", tpl, err)
		}
		if !reflect.DeepEqual(got, tpl) {
			t.Errorf("round trip: %#v != %#v", got, tpl)
		}
	}
}

func TestParseTemplateRejects(t *testing.T) {
	long := make([]string, MaxArgs+1)
	for i := range long {
		long[i] = "a"
	}
	tests := []struct {
		name string
		argv []string
		env  map[string]string
		want string
	}{
		{"empty argv", nil, nil, "empty"},
		{"too many args", long, nil, "more than"},
		{"blank program", []string{" "}, nil, "program name is empty"},
		{"port in argv0", []string{"{port}"}, nil, "program name"},
		{"port index in argv0", []string{"x{port:1}"}, nil, "program name"},
		{"NUL", []string{"a", "b\x00"}, nil, "control"},
		{"control char", []string{"a", "b\nc"}, nil, "control"},
		{"unknown placeholder", []string{"a", "{host}"}, nil, "unknown placeholder"},
		{"port prefix typo", []string{"a", "{ports}"}, nil, "unknown placeholder"},
		{"index too large", []string{"a", "{port:20}"}, nil, "number from 0"},
		{"index not a number", []string{"a", "{port:x}"}, nil, "number from 0"},
		{"index leading zero", []string{"a", "{port:01}"}, nil, "number from 0"},
		{"empty index", []string{"a", "{port:}"}, nil, "number from 0"},
		{"bad env name", []string{"a"}, map[string]string{"A=B": "x"}, "not valid"},
		{"reserved PORT", []string{"a"}, map[string]string{"PORT": "1"}, "port block"},
		{"reserved YGG_PORT_3", []string{"a"}, map[string]string{"ygg_port_3": "1"}, "port block"},
		{"token env", []string{"a"}, map[string]string{"GITHUB_TOKEN": "x"}, "token"},
		{"env value NUL", []string{"a"}, map[string]string{"A": "x\x00"}, "value"},
	}
	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			_, err := ParseTemplate(tc.argv, tc.env)
			if err == nil || !errors.Is(err, ErrBadTemplate) || !strings.Contains(err.Error(), tc.want) {
				t.Fatalf("err = %v, want %q", err, tc.want)
			}
		})
	}
}

func TestParseTemplateKeepsOtherBraces(t *testing.T) {
	if _, err := ParseTemplate([]string{"node", `{"a":1}`, "{a,b}", "${HOME}"}, nil); err != nil {
		t.Fatalf("other braces rejected: %v", err)
	}
}

func TestParseTemplateAcceptedIsExpandable(t *testing.T) {
	block := core.PortBlock{Base: 20000, Size: core.PortBlockSize}
	for _, tpl := range templates {
		parsed, err := ParseTemplate(Format(tpl))
		if err != nil {
			continue
		}
		if _, err := Expand(parsed, block); err != nil {
			t.Errorf("accepted template %v does not expand in a full block: %v", parsed.Argv, err)
		}
	}
}

func TestParseTemplateCopies(t *testing.T) {
	argv := []string{"a", "b"}
	env := map[string]string{"A": "1"}
	tpl, _ := ParseTemplate(argv, env)
	argv[0], env["A"] = "z", "2"
	if tpl.Argv[0] != "a" || tpl.Env["A"] != "1" {
		t.Error("the template shares memory with its input")
	}
}

func TestExpandEnv(t *testing.T) {
	exp, err := Expand(Template{Argv: []string{"x", "{port:2}"}, Env: map[string]string{"A": "b"}}, core.PortBlock{Base: 100, Size: 5})
	if err != nil {
		t.Fatal(err)
	}
	if exp.Argv[1] != "102" || exp.Env["PORT"] != "100" || exp.Env["YGG_PORT_BASE"] != "100" || exp.Env["YGG_PORT_4"] != "104" || exp.Env["A"] != "b" {
		t.Errorf("env = %v", exp.Env)
	}
	if _, ok := exp.Env["YGG_PORT_5"]; ok {
		t.Error("YGG_PORT_5 outside the block of 5")
	}
	// Main checkout: nothing injected.
	exp, err = Expand(Template{Argv: []string{"x"}}, core.PortBlock{})
	if err != nil || len(exp.Env) != 0 {
		t.Errorf("main checkout: %v %v", exp, err)
	}
	if _, err := Expand(Template{Argv: []string{"x", "{port}"}}, core.PortBlock{}); !errors.Is(err, ErrBadTemplate) {
		t.Errorf("placeholder without a block: %v", err)
	}
	if _, err := Expand(Template{Argv: []string{"x", "{port:5}"}}, core.PortBlock{Base: 100, Size: 5}); !errors.Is(err, ErrBadTemplate) {
		t.Errorf("index past block: %v", err)
	}
}

func TestParseAnnouncement(t *testing.T) {
	tests := []struct {
		line string
		want []Port
	}{
		{"  Local:   http://localhost:5173/", []Port{5173}},
		{"Server at 127.0.0.1:8000 and [::1]:5000", []Port{5000, 8000}},
		{"Now listening on: http://localhost:5000", []Port{5000}},
		{"INFO: Uvicorn running on http://127.0.0.1:8000 (Press CTRL+C to quit)", []Port{8000}},
		{"\x1b[1mhttp://localhost:\x1b[1m3000\x1b[22m", []Port{3000}},
		{"LOCALHOST:3000 localhost:3000", []Port{3000}},
		{"http://0.0.0.0:3000", nil},
		{"http://192.168.1.4:3000", nil},
		{"http://my.localhost:3000", nil},
		{"localhost:0", nil},
		{"localhost:65536", nil},
		{"localhost:123456", nil},
		{"localhost:3000abc", nil},
		{"localhost without a port", nil},
		{"", nil},
	}
	for _, tc := range tests {
		t.Run(tc.line, func(t *testing.T) {
			got := ParseAnnouncement(tc.line)
			if !slices.Equal(got, tc.want) {
				t.Errorf("ParseAnnouncement(%q) = %v, want %v", tc.line, got, tc.want)
			}
			if !validPorts(got) {
				t.Errorf("not valid: %v", got)
			}
		})
	}
}

func TestNormalizePorts(t *testing.T) {
	got, dropped := NormalizePorts([]int{3000, 80, 3000, 0, -5, 70000, 65535})
	if !slices.Equal(got, []Port{80, 3000, 65535}) || dropped != 3 {
		t.Errorf("got %v dropped %d", got, dropped)
	}
	if got, d := NormalizePorts(nil); got == nil || len(got) != 0 || d != 0 {
		t.Errorf("nil input: %v %d", got, d)
	}
}

func FuzzParseTemplate(f *testing.F) {
	f.Add("npm\x00run\x00dev", "")
	f.Add("vite\x00--port\x00{port:3}", "A=b")
	f.Add("{port}", "PORT=1")
	f.Fuzz(func(t *testing.T, argv, env string) {
		var e map[string]string
		if k, v, ok := strings.Cut(env, "="); ok {
			e = map[string]string{k: v}
		}
		tpl, err := ParseTemplate(strings.Split(argv, "\x00"), e)
		if err != nil {
			if !errors.Is(err, ErrBadTemplate) {
				t.Fatalf("error does not wrap ErrBadTemplate: %v", err)
			}
			return
		}
		again, err := ParseTemplate(Format(tpl))
		if err != nil || !reflect.DeepEqual(again, tpl) {
			t.Fatalf("round trip failed: %v %#v %#v", err, tpl, again)
		}
		exp, err := Expand(tpl, core.PortBlock{Base: 20000, Size: core.PortBlockSize})
		if err != nil {
			t.Fatalf("accepted template does not expand: %v", err)
		}
		if strings.Contains(strings.Join(exp.Argv, " "), "{port") {
			t.Fatalf("placeholder left: %v", exp.Argv)
		}
	})
}

func FuzzParseAnnouncement(f *testing.F) {
	f.Add("localhost:3000")
	f.Add("[::1]:5000 127.0.0.1:99999")
	f.Add("\x1b[1m\x1b")
	f.Fuzz(func(t *testing.T, line string) {
		got := ParseAnnouncement(line)
		if len(got) == 0 && got != nil {
			t.Fatal("empty result must be nil")
		}
		if got != nil && !validPorts(got) {
			t.Fatalf("invalid result %v", got)
		}
	})
}

var rowTime = time.Date(2026, 3, 4, 5, 6, 7, 0, time.UTC)

func sampleServers() []Server {
	b := core.PortBlock{Base: 20000, Size: 20}
	return []Server{
		Stopped{},
		Stopped{Attempt: 2, Last: LastExited, Code: 3, At: rowTime},
		Stopped{Attempt: 2, Last: LastUserStopped, At: rowTime},
		Stopped{Attempt: 2, Last: LastInterrupted, At: rowTime},
		Starting{Attempt: 1, Since: rowTime, Block: b},
		Starting{Attempt: 1, Since: rowTime, Pid: 9},
		Running{Attempt: 1, Since: rowTime, Pid: 9, Block: b, Ports: []Port{3000, 20000}, Announced: []Port{20000}},
		Running{Attempt: 4, Since: rowTime, Pid: 9, Ports: []Port{}, Announced: []Port{}},
		Stopping{Attempt: 1, Cause: CauseRestart, KillAttempt: KillSecond, Since: rowTime, Pid: 9, Block: b},
		Failed{Attempt: 1, Kind: FailStartFailed, At: rowTime},
		Failed{Attempt: 1, Kind: FailCrashed, Code: 139, At: rowTime},
		Failed{Attempt: 1, Kind: FailExitedEarly, Code: 1, At: rowTime},
		Failed{Attempt: 1, Kind: FailStopFailed, At: rowTime},
		Orphaned{Attempt: 2, Pid: 77, SeenAt: rowTime},
	}
}

var testKey = Key{Repo: "rep_1", Worktree: "wt_1", CommandKey: "dev"}

func TestServerRowRoundTrip(t *testing.T) {
	for _, s := range sampleServers() {
		row := RowOf(testKey, s)
		got, err := ServerFromRow(row)
		if err != nil {
			t.Fatalf("%#v: %v", s, err)
		}
		if !reflect.DeepEqual(got, Persisted(s)) {
			t.Errorf("round trip:\n got %#v\nwant %#v", got, Persisted(s))
		}
	}
}

func TestServerFromRowAcceptedIsValid(t *testing.T) {
	for _, s := range sampleServers() {
		got, err := ServerFromRow(RowOf(testKey, s))
		if err != nil {
			t.Fatal(err)
		}
		switch st := got.(type) {
		case Running:
			if st.Pid < 1 || !validPorts(st.Ports) {
				t.Errorf("invalid running %#v", st)
			}
		case Stopping:
			if st.Pid < 1 || st.KillAttempt.Validate() != nil {
				t.Errorf("invalid stopping %#v", st)
			}
		}
	}
}

func TestServerFromRowRejects(t *testing.T) {
	live := RowOf(testKey, Running{Attempt: 1, Since: rowTime, Pid: 9, Block: core.PortBlock{Base: 20000, Size: 20}, Ports: []Port{20000}, Announced: []Port{}})
	rest := RowOf(testKey, Stopped{Attempt: 1, Last: LastUserStopped, At: rowTime})
	tests := []struct {
		name string
		row  Row
		edit func(*Row)
	}{
		{"unknown status", live, func(r *Row) { r.Status = "paused" }},
		{"running without pid", live, func(r *Row) { r.Pid = 0 }},
		{"running without attempt", live, func(r *Row) { r.Attempt = 0 }},
		{"negative attempt", rest, func(r *Row) { r.Attempt = -1 }},
		{"running without time", live, func(r *Row) { r.Since = time.Time{} }},
		{"running with unsorted ports", live, func(r *Row) { r.Ports = []Port{5, 3} }},
		{"running with duplicate ports", live, func(r *Row) { r.Ports = []Port{3, 3} }},
		{"running with port 0", live, func(r *Row) { r.Ports = []Port{0} }},
		{"running with port 70000", live, func(r *Row) { r.Announced = []Port{70000} }},
		{"running with fail kind", live, func(r *Row) { r.FailKind = FailCrashed }},
		{"running with stop cause", live, func(r *Row) { r.StopCause = CauseUserStop }},
		{"running with last kind", live, func(r *Row) { r.LastKind = LastExited }},
		{"bad block", live, func(r *Row) { r.Block = core.PortBlock{Base: 65535, Size: 20} }},
		{"stopped with pid", rest, func(r *Row) { r.Pid = 4 }},
		{"stopped with ports", rest, func(r *Row) { r.Ports = []Port{80} }},
		{"stopped with block", rest, func(r *Row) { r.Block = core.PortBlock{Base: 1, Size: 1} }},
		{"stopped with fail kind", rest, func(r *Row) { r.FailKind = FailCrashed }},
		{"stopped with code but not exited", rest, func(r *Row) { r.ExitCode = 2 }},
		{"unknown last kind", rest, func(r *Row) { r.LastKind = "weird" }},
		{"stopping without cause", RowOf(testKey, Stopping{Attempt: 1, Cause: CauseUserStop, KillAttempt: 1, Since: rowTime, Pid: 3}), func(r *Row) { r.StopCause = "" }},
		{"stopping kill attempt 0", RowOf(testKey, Stopping{Attempt: 1, Cause: CauseUserStop, KillAttempt: 1, Since: rowTime, Pid: 3}), func(r *Row) { r.KillAttempt = 0 }},
		{"stopping kill attempt 3", RowOf(testKey, Stopping{Attempt: 1, Cause: CauseUserStop, KillAttempt: 1, Since: rowTime, Pid: 3}), func(r *Row) { r.KillAttempt = 3 }},
		{"kill attempt outside stopping", live, func(r *Row) { r.KillAttempt = 1 }},
		{"failed without kind", RowOf(testKey, Failed{Attempt: 1, Kind: FailCrashed, At: rowTime}), func(r *Row) { r.FailKind = "" }},
		{"failed unknown kind", RowOf(testKey, Failed{Attempt: 1, Kind: FailCrashed, At: rowTime}), func(r *Row) { r.FailKind = "x" }},
		{"stop failed with code", RowOf(testKey, Failed{Attempt: 1, Kind: FailStopFailed, At: rowTime}), func(r *Row) { r.ExitCode = 1 }},
		{"orphaned without pid", RowOf(testKey, Orphaned{Attempt: 1, Pid: 4, SeenAt: rowTime}), func(r *Row) { r.Pid = 0 }},
		{"orphaned without attempt", RowOf(testKey, Orphaned{Attempt: 1, Pid: 4, SeenAt: rowTime}), func(r *Row) { r.Attempt = 0 }},
		{"starting with ports", RowOf(testKey, Starting{Attempt: 1, Since: rowTime}), func(r *Row) { r.Ports = []Port{80} }},
		{"starting negative pid", RowOf(testKey, Starting{Attempt: 1, Since: rowTime}), func(r *Row) { r.Pid = -1 }},
	}
	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			r := tc.row
			tc.edit(&r)
			if _, err := ServerFromRow(r); !errors.Is(err, ErrBadRow) {
				t.Fatalf("err = %v, want ErrBadRow", err)
			}
		})
	}
}

func FuzzServerFromRow(f *testing.F) {
	f.Add("running", 1, 9, 20000, 20, "", "", "", 0, int64(1))
	f.Add("stopping", 1, 9, 0, 0, "", "", "user_stop", 1, int64(1))
	f.Add("failed", 1, 0, 0, 0, "", "crashed", "", 0, int64(0))
	f.Fuzz(func(t *testing.T, status string, attempt, pid, base, size int, last, fail, cause string, kill int, secs int64) {
		row := Row{
			Status: status, Attempt: attempt, Pid: pid, Block: core.PortBlock{Base: base, Size: size},
			LastKind: LastKind(last), FailKind: FailKind(fail), StopCause: StopCause(cause), KillAttempt: KillAttempt(kill),
			Ports: []Port{}, Announced: []Port{},
		}
		if secs != 0 {
			row.Since = time.Unix(secs, 0).UTC()
		}
		s, err := ServerFromRow(row)
		if err != nil {
			if !errors.Is(err, ErrBadRow) {
				t.Fatalf("error does not wrap ErrBadRow: %v", err)
			}
			return
		}
		again, err := ServerFromRow(RowOf(Key{}, s))
		if err != nil || !reflect.DeepEqual(again, s) {
			t.Fatalf("accepted row does not round trip: %v %#v %#v", err, s, again)
		}
	})
}
