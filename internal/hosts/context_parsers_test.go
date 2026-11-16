package hosts

// Provider JSON parsers of host context (GH-05; TM1-H): GitHub and GitLab
// issues, issue detail, runs/jobs and the immutable MR/base/head/version diff,
// driven through ContextAPI over an in-memory transport serving the recorded
// fixtures (so thousands of generated cases cost no sockets). Unrelated provider
// fields are ignored on purpose: nothing here asks for a byte-for-byte wire
// round trip, only for the round trip of the representable domain projection.

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"math/rand"
	"net/http"
	"strconv"
	"strings"
	"testing"
	"time"
	"unicode/utf8"

	corehosts "github.com/brendatamaar/yggdrasil/internal/core/hosts"
)

// memRoute is one canned answer.
type memRoute struct {
	status int
	body   []byte
}

type memTransport struct{ routes map[string]memRoute }

func (m memTransport) RoundTrip(r *http.Request) (*http.Response, error) {
	rt, ok := m.routes[r.URL.Path+"?"+r.URL.RawQuery]
	if !ok {
		return &http.Response{StatusCode: 599, Body: io.NopCloser(strings.NewReader("")), Header: http.Header{}, Request: r}, nil
	}
	if rt.status == 0 {
		rt.status = 200
	}
	return &http.Response{StatusCode: rt.status, Body: io.NopCloser(strings.NewReader(string(rt.body))), Header: http.Header{}, Request: r}, nil
}

// fixtureRoutes loads named recorded answers into an in-memory route table.
func fixtureRoutes(t testing.TB, provider string, answers ...string) map[string]memRoute {
	t.Helper()
	p := loadRecordedProvider(t, provider)
	out := map[string]memRoute{}
	for _, name := range answers {
		for _, e := range p.Answers[name] {
			body := []byte(strings.ReplaceAll(string(e.Body), "{{BASE}}", "https://git.acme.test"))
			if e.BodyText != nil {
				body = []byte(*e.BodyText)
			}
			out[e.Path+"?"+e.Query] = memRoute{status: e.Status, body: body}
		}
	}
	return out
}

func memAPI(lab bool, routes map[string]memRoute) (ContextAPI, corehosts.Instance) {
	root := "https://git.acme.test/api/v3"
	if lab {
		root = "https://git.acme.test/api/v4"
	}
	g := NewGetter(&http.Client{Transport: memTransport{routes}}, func() time.Time { return time.Unix(1_000_000, 0) })
	return ContextAPI{Getter: g, Root: root, GitLab: lab}, corehosts.Instance{ID: "git.acme.test", Kind: corehosts.KindGitHub, Base: "https://git.acme.test"}
}

func gh(lab bool) string {
	if lab {
		return "gitlab"
	}
	return "github"
}

func issuePath(lab bool) string {
	if lab {
		return "/api/v4/projects/acme/shop/issues"
	}
	return "/api/v3/repos/acme/shop/issues"
}

func listQuery(lab bool) string {
	if lab {
		return "state=opened&per_page=50&page=1"
	}
	return "state=open&per_page=50&page=1"
}

// wireIssue is the independent encoder of a domain issue into each provider's
// wire shape (with an unrelated field the parser must ignore).
func wireIssue(lab bool, s corehosts.IssueSummary, body, link string) map[string]any {
	updated := s.UpdatedAt.UTC().Format(time.RFC3339)
	if lab {
		labels := []string{}
		labels = append(labels, s.Labels...)
		return map[string]any{"iid": s.Number, "title": s.Title, "description": body, "web_url": link, "author": map[string]string{"username": s.Author}, "labels": labels, "updated_at": updated, "unrelated": []int{1, 2}}
	}
	labels := []map[string]string{}
	for _, l := range s.Labels {
		labels = append(labels, map[string]string{"name": l, "color": "ededed"})
	}
	return map[string]any{"number": s.Number, "title": s.Title, "body": body, "html_url": link, "user": map[string]string{"login": s.Author}, "labels": labels, "updated_at": updated, "reactions": map[string]int{"+1": 3}}
}

func genSummary(r *rand.Rand, n int) corehosts.IssueSummary {
	words := []string{"cart", "coupon", "dark mode", "Überweisung", "日本語", "a", "fix: crash", "100%"}
	labels := make([]string, r.Intn(4))
	for i := range labels {
		labels[i] = words[r.Intn(len(words))]
	}
	return corehosts.IssueSummary{Number: n, Title: words[r.Intn(len(words))] + " " + words[r.Intn(len(words))], Author: []string{"ana", "ben", "cy"}[r.Intn(3)], Labels: labels, UpdatedAt: time.Date(2026, time.Month(1+r.Intn(11)), 1+r.Intn(27), r.Intn(24), 0, 0, 0, time.UTC)}
}

func sameSummary(a, b corehosts.IssueSummary) bool {
	if a.Number != b.Number || a.Title != b.Title || a.Author != b.Author || !a.UpdatedAt.Equal(b.UpdatedAt) || len(a.Labels) != len(b.Labels) {
		return false
	}
	for i := range a.Labels {
		if a.Labels[i] != b.Labels[i] {
			return false
		}
	}
	return true
}

// Round trip of the representable projection: format a domain list and detail
// into the provider wire, parse, and get the same domain values back.
func TestContextProviderIssueRoundTrip(t *testing.T) {
	for _, lab := range []bool{false, true} {
		for seed := int64(1); seed <= 150; seed++ {
			r := rand.New(rand.NewSource(seed))
			var want []corehosts.IssueSummary
			var wire []map[string]any
			for i, n := 0, 1+r.Intn(5); i < n; i++ {
				s := genSummary(r, 1+r.Intn(900))
				want = append(want, s)
				wire = append(wire, wireIssue(lab, s, "", ""))
			}
			raw, _ := json.Marshal(wire)
			api, in := memAPI(lab, map[string]memRoute{issuePath(lab) + "?" + listQuery(lab): {body: raw}})
			res, err := api.Issues(context.Background(), in, "tok", "acme/shop")
			if err != nil {
				t.Fatalf("%s seed %d: %v", gh(lab), seed, err)
			}
			got := res.Value.(corehosts.IssueList).Items
			if len(got) != len(want) {
				t.Fatalf("%s seed %d: %d items, want %d", gh(lab), seed, len(got), len(want))
			}
			for i := range want {
				if !sameSummary(got[i], want[i]) {
					t.Fatalf("%s seed %d item %d: got %+v want %+v", gh(lab), seed, i, got[i], want[i])
				}
			}
			// Detail, with a same-origin link and a body.
			one := want[0]
			link := "https://git.acme.test/acme/shop/issues/" + strconv.Itoa(one.Number)
			body := "Steps: " + one.Title
			raw1, _ := json.Marshal(wireIssue(lab, one, body, link))
			api, in = memAPI(lab, map[string]memRoute{issuePath(lab) + "/" + strconv.Itoa(one.Number) + "?": {body: raw1}})
			res, err = api.Issue(context.Background(), in, "tok", "acme/shop", one.Number)
			if err != nil {
				t.Fatalf("%s seed %d detail: %v", gh(lab), seed, err)
			}
			d := res.Value.(corehosts.IssueDetail)
			if !sameSummary(d.Summary, one) || d.Body != body || d.URL != link {
				t.Fatalf("%s seed %d detail: %+v", gh(lab), seed, d)
			}
		}
	}
}

// oracleIssue is the independent acceptance rule for one issue answer.
func oracleIssue(lab bool, number int, title string, pull, labelsOK bool) bool {
	return number > 0 && title != "" && !pull && labelsOK
}

// Accepted if and only if the independent oracle accepts; accepted values are
// valid for the key by an oracle of their own.
func TestContextProviderIssueAcceptedIffValid(t *testing.T) {
	accepted, rejected := 0, 0
	for _, lab := range []bool{false, true} {
		for seed := int64(1); seed <= 400; seed++ {
			r := rand.New(rand.NewSource(seed))
			number := []int{5, 5, 5, 0, -1, 6}[r.Intn(6)]
			title := []string{"Search is slow", "Search is slow", ""}[r.Intn(3)]
			pull := r.Intn(6) == 0
			badLabel := r.Intn(6) == 0
			w := wireIssue(lab, corehosts.IssueSummary{Number: number, Title: title, Author: "cy", Labels: []string{"perf"}, UpdatedAt: contractT1}, "text", "")
			if pull {
				w["pull_request"] = map[string]string{"url": "x"}
			}
			if badLabel {
				w["labels"] = []int{1, 2} // not strings / objects
			}
			raw, _ := json.Marshal(w)
			api, in := memAPI(lab, map[string]memRoute{issuePath(lab) + "/5?": {body: raw}})
			res, err := api.Issue(context.Background(), in, "tok", "acme/shop", 5)
			// For key number 5 a different positive number is a foreign object.
			want := oracleIssue(lab, number, title, pull, !badLabel) && number == 5
			if want != (err == nil) {
				t.Fatalf("%s seed %d: number %d title %q pull %v badLabel %v: oracle %v, got %v", gh(lab), seed, number, title, pull, badLabel, want, err)
			}
			if err == nil {
				accepted++
				d := res.Value.(corehosts.IssueDetail)
				if d.Summary.Number != 5 || d.Summary.Title == "" || !utf8.ValidString(d.Body) {
					t.Fatalf("accepted value is not valid: %+v", d)
				}
				if !corehosts.ContextPayloadMatches(corehosts.ContextKey{Repo: "rep_1", Kind: corehosts.ContextIssue, ObjectID: "5"}, d) {
					t.Fatal("accepted value does not match its key")
				}
			} else {
				rejected++
				if _, refused := contextRefusal(err); !refused {
					t.Fatalf("a rejected answer would be retried: %v", err)
				}
			}
		}
	}
	if accepted < 50 || rejected < 50 {
		t.Fatalf("lopsided: %d accepted, %d rejected", accepted, rejected)
	}
}

// One test per rejection rule of the run/job/MR/diff parsers. Each case swaps
// one route of a good recording for a faulty answer.
func TestContextProviderRejectsEachRule(t *testing.T) {
	type edit struct {
		lab    bool
		answer string // recorded answer providing the good routes
		key    corehosts.ContextKey
		route  string // path suffix (with query suffix after ?) of the route to replace
		status int
		body   string
		want   error // errors.Is target
		kind   corehosts.FailKind
	}
	jobsPage := func(lab bool) string {
		if lab {
			return "/pipelines/12/jobs?per_page=50&page=1"
		}
		return "/actions/runs/12/jobs?per_page=50&page=1"
	}
	runRoute := func(lab bool) string {
		if lab {
			return "/pipelines/12?"
		}
		return "/actions/runs/12?"
	}
	manyJobs := func(lab bool, n int) string {
		var items []string
		for i := 1; i <= n; i++ {
			if lab {
				items = append(items, fmt.Sprintf(`{"id":%d,"status":"failed"}`, i))
			} else {
				items = append(items, fmt.Sprintf(`{"id":%d,"conclusion":"failure"}`, i))
			}
		}
		if lab {
			return "[" + strings.Join(items, ",") + "]"
		}
		return `{"jobs":[` + strings.Join(items, ",") + `]}`
	}
	mrRoute := func(lab bool) string {
		if lab {
			return "/merge_requests/3?"
		}
		return "/pulls/3?"
	}
	var cases []struct {
		name string
		edit
	}
	add := func(name string, e edit) {
		cases = append(cases, struct {
			name string
			edit
		}{name, e})
	}
	for _, lab := range []bool{false, true} {
		sha := "sha"
		if !lab {
			sha = "head_sha"
		}
		_ = sha
		pre := gh(lab) + "/"
		add(pre+"run id is another run", edit{lab, "jobs_ok", jobsKey(), runRoute(lab), 200, `{"id":13,"sha":"` + contractHeadA + `","head_sha":"` + contractHeadA + `"}`, ErrContextScope, ""})
		add(pre+"run built another head than the key", edit{lab, "jobs_ok", jobsKey(), runRoute(lab), 200, `{"id":12,"sha":"` + contractHeadB + `","head_sha":"` + contractHeadB + `"}`, ErrContextScope, ""})
		add(pre+"run has no head", edit{lab, "jobs_ok", jobsKey(), runRoute(lab), 200, `{"id":12}`, ErrContextScope, ""})
		add(pre+"run is null", edit{lab, "jobs_ok", jobsKey(), runRoute(lab), 200, `null`, ErrContextInvalid, ""})
		add(pre+"run is not JSON", edit{lab, "jobs_ok", jobsKey(), runRoute(lab), 200, `{"id":`, ErrContextInvalid, ""})
		add(pre+"job id is not positive", edit{lab, "jobs_ok", jobsKey(), jobsPage(lab), 200, strings.Replace(manyJobs(lab, 1), `"id":1`, `"id":0`, 1), ErrContextInvalid, ""})
		add(pre+"job page over 50 entries", edit{lab, "jobs_ok", jobsKey(), jobsPage(lab), 200, manyJobs(lab, 51), ErrContextInvalid, ""})
		add(pre+"job list is not a list", edit{lab, "jobs_ok", jobsKey(), jobsPage(lab), 200, `"x"`, ErrContextInvalid, ""})
		add(pre+"job list 404", edit{lab, "jobs_ok", jobsKey(), jobsPage(lab), 404, ``, nil, corehosts.FailNotFound})
		add(pre+"MR is another number", edit{lab, "mr_ok", mrKey(), mrRoute(lab), 200, strings.NewReplacer(`"number": 3`, `"number": 4`, `"iid": 3`, `"iid": 4`).Replace(string(mustRoute(t, lab, "mr_ok", mrRoute(lab)))), ErrContextScope, ""})
		add(pre+"MR has an unknown state", edit{lab, "mr_ok", mrKey(), mrRoute(lab), 200, strings.NewReplacer(`"open"`, `"frozen"`, `"opened"`, `"frozen"`).Replace(string(mustRoute(t, lab, "mr_ok", mrRoute(lab)))), ErrContextInvalid, ""})
		add(pre+"MR head is not a full commit id", edit{lab, "mr_ok", mrKey(), mrRoute(lab), 200, strings.ReplaceAll(string(mustRoute(t, lab, "mr_ok", mrRoute(lab))), contractHeadA, "abc123"), ErrContextInvalid, ""})
		add(pre+"MR has no head", edit{lab, "mr_ok", mrKey(), mrRoute(lab), 200, strings.ReplaceAll(string(mustRoute(t, lab, "mr_ok", mrRoute(lab))), contractHeadA, ""), ErrContextInvalid, ""})
		add(pre+"MR is not JSON", edit{lab, "mr_ok", mrKey(), mrRoute(lab), 200, `[`, ErrContextInvalid, ""})
	}
	// Provider-specific rules.
	add("github/base commit not a full id", edit{false, "mr_ok", mrKey(), "/pulls/3?", 200, strings.ReplaceAll(string(mustRoute(t, false, "mr_ok", "/pulls/3?")), contractHeadB, "zz"), ErrContextInvalid, ""})
	add("gitlab/no diff version for the head yet", edit{true, "mr_ok", mrKey(), "/merge_requests/3/versions?per_page=50", 200, `[]`, nil, corehosts.FailTransient})
	add("gitlab/version belongs to another head", edit{true, "mr_ok", mrKey(), "/merge_requests/3/versions/77?", 200, `{"head_commit_sha":"` + contractHeadB + `","diffs":[]}`, ErrContextScope, ""})
	add("gitlab/more than 50 versions", edit{true, "mr_ok", mrKey(), "/merge_requests/3/versions?per_page=50", 200, "[" + strings.TrimSuffix(strings.Repeat(`{"id":1,"head_commit_sha":"x"},`, 51), ",") + "]", ErrContextTooLarge, ""})

	for _, c := range cases {
		t.Run(c.name, func(t *testing.T) {
			routes := fixtureRoutes(t, gh(c.lab), c.answer)
			replaced := false
			for k := range routes {
				p, q, _ := strings.Cut(k, "?")
				if strings.HasSuffix(p+"?"+q, c.route) || strings.HasSuffix(k, c.route) {
					routes[k] = memRoute{status: c.status, body: []byte(c.body)}
					replaced = true
				}
			}
			if !replaced {
				t.Fatalf("no route ends with %q", c.route)
			}
			api, in := memAPI(c.lab, routes)
			var err error
			num, _ := strconv.Atoi(c.key.ObjectID)
			switch c.key.Kind {
			case corehosts.ContextFailedJobs:
				_, err = api.FailedJobs(context.Background(), in, "tok", "acme/shop", c.key.ObjectID, c.key.Head)
			case corehosts.ContextMRDiff:
				_, err = api.MRDiff(context.Background(), in, "tok", "acme/shop", num)
			}
			if err == nil {
				t.Fatal("the faulty answer was accepted")
			}
			var fe *FetchError
			switch {
			case c.kind != "":
				if !errors.As(err, &fe) || fe.Kind != c.kind {
					t.Fatalf("want a %s failure, got %v", c.kind, err)
				}
			case !errors.Is(err, c.want):
				t.Fatalf("want %v, got %v", c.want, err)
			}
		})
	}
}

func mustRoute(t testing.TB, lab bool, answer, route string) []byte {
	t.Helper()
	for k, v := range fixtureRoutes(t, gh(lab), answer) {
		if strings.HasSuffix(k, route) {
			return v.body
		}
	}
	t.Fatalf("no route %q in %s", route, answer)
	return nil
}

// Unsafe links on an issue are dropped, never followed or returned.
func TestContextProviderDropsUnsafeIssueLinks(t *testing.T) {
	for _, lab := range []bool{false, true} {
		for _, link := range []string{"https://evil.acme.test/x", "http://git.acme.test/x", "https://u:p@git.acme.test/x", "javascript:alert(1)", "//git.acme.test/x", "not a url"} {
			raw, _ := json.Marshal(wireIssue(lab, corehosts.IssueSummary{Number: 5, Title: "t"}, "b", link))
			api, in := memAPI(lab, map[string]memRoute{issuePath(lab) + "/5?": {body: raw}})
			res, err := api.Issue(context.Background(), in, "tok", "acme/shop", 5)
			if err != nil {
				t.Fatal(err)
			}
			if u := res.Value.(corehosts.IssueDetail).URL; u != "" {
				t.Fatalf("%s: unsafe link %q was returned as %q", gh(lab), link, u)
			}
		}
	}
}

// Page and size limits: ten pages, 500 entries, labelled.
func TestContextProviderPageLimits(t *testing.T) {
	for _, lab := range []bool{false, true} {
		routes := map[string]memRoute{}
		var items []map[string]any
		for i := 1; i <= 50; i++ {
			items = append(items, wireIssue(lab, corehosts.IssueSummary{Number: i, Title: "t", UpdatedAt: contractT1}, "", ""))
		}
		raw, _ := json.Marshal(items)
		for page := 1; page <= 12; page++ {
			q := strings.Replace(listQuery(lab), "page=1", "page="+strconv.Itoa(page), 1)
			routes[issuePath(lab)+"?"+q] = memRoute{body: raw}
		}
		api, in := memAPI(lab, routes)
		res, err := api.Issues(context.Background(), in, "tok", "acme/shop")
		if err != nil {
			t.Fatal(err)
		}
		list := res.Value.(corehosts.IssueList)
		if len(list.Items) > corehosts.ContextMaxEntries || !list.Truncated {
			t.Fatalf("%s: %d items, truncated=%v: a read of full pages must stop at 10 pages and say so", gh(lab), len(list.Items), list.Truncated)
		}
	}
}

// Fuzz: arbitrary bytes replace one route of a good recording; whatever the
// parser accepts must match its key and the bounds, and nothing may panic.
func FuzzContextProviderBodies(f *testing.F) {
	for _, s := range []string{`{}`, `null`, `[]`, `{"jobs":[]}`, `{"number":3,"head":{"sha":"x"}}`, `{"iid":3,"diff_refs":{"head_sha":"` + contractHeadA + `"}}`, `[{"id":1,"status":"failed"}]`, `{"id":12,"sha":"` + contractHeadA + `"}`} {
		for mode := byte(0); mode < 6; mode++ {
			f.Add([]byte(s), mode)
		}
	}
	type call struct {
		lab    bool
		answer string
		key    corehosts.ContextKey
	}
	calls := []call{
		{false, "issues_ok", issuesKey()}, {true, "issues_ok", issuesKey()},
		{false, "issue_ok", issueKey()}, {true, "issue_ok", issueKey()},
		{false, "jobs_ok", jobsKey()}, {true, "jobs_ok", jobsKey()},
		{false, "mr_ok", mrKey()}, {true, "mr_ok", mrKey()},
	}
	f.Fuzz(func(t *testing.T, body []byte, mode byte) {
		c := calls[int(mode)%len(calls)]
		routes := fixtureRoutes(t, gh(c.lab), c.answer)
		// Replace one route, chosen by the high bits of mode, in sorted order.
		var keys []string
		for k := range routes {
			keys = append(keys, k)
		}
		if len(keys) == 0 {
			return
		}
		sortStrings(keys)
		k := keys[int(mode/8)%len(keys)]
		routes[k] = memRoute{body: body}
		api, in := memAPI(c.lab, routes)
		num, _ := strconv.Atoi(c.key.ObjectID)
		var res ContextResult
		var err error
		switch c.key.Kind {
		case corehosts.ContextIssues:
			res, err = api.Issues(context.Background(), in, "tok", "acme/shop")
		case corehosts.ContextIssue:
			res, err = api.Issue(context.Background(), in, "tok", "acme/shop", num)
		case corehosts.ContextFailedJobs:
			res, err = api.FailedJobs(context.Background(), in, "tok", "acme/shop", c.key.ObjectID, c.key.Head)
		case corehosts.ContextMRDiff:
			res, err = api.MRDiff(context.Background(), in, "tok", "acme/shop", num)
		}
		if err != nil {
			return
		}
		if !corehosts.ContextPayloadMatches(c.key, res.Value) {
			t.Fatalf("accepted a payload that does not match its key: %#v", res.Value)
		}
		raw, jerr := json.Marshal(res)
		if jerr != nil || len(raw) > corehosts.ContextMaxPromptBytes || !utf8.Valid(raw) {
			t.Fatalf("accepted a payload outside the bounds: %d bytes (%v)", len(raw), jerr)
		}
	})
}

func sortStrings(s []string) {
	for i := 1; i < len(s); i++ {
		for j := i; j > 0 && s[j] < s[j-1]; j-- {
			s[j], s[j-1] = s[j-1], s[j]
		}
	}
}
