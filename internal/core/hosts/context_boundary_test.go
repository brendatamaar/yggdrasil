package hosts

// Boundary tests of the host context core (GH-05, TM1-H): ContextKey.Validate
// and ContextPayloadMatches are checked against ORACLES WRITTEN HERE from the
// design's rules, not against the production validators, so a change that
// loosens a rule fails these tests.

import (
	"encoding/json"
	"fmt"
	"math/rand"
	"strconv"
	"strings"
	"testing"
	"unicode/utf8"

	"github.com/brendatamaar/yggdrasil/internal/core"
)

// ---- independent oracle for ContextKey ----

const oracleMaxInt64 = "9223372036854775807"

// oracleObject: a positive decimal without sign, leading zero or any other
// character, that fits a 64-bit int.
func oracleObject(s string) bool {
	if s == "" || s[0] == '0' || len(s) > len(oracleMaxInt64) {
		return false
	}
	for i := 0; i < len(s); i++ {
		if s[i] < '0' || s[i] > '9' {
			return false
		}
	}
	return len(s) < len(oracleMaxInt64) || s <= oracleMaxInt64
}

// oracleHead: a full lowercase hexadecimal commit id (SHA-1 or SHA-256 length).
func oracleHead(s string) bool {
	if len(s) != 40 && len(s) != 64 {
		return false
	}
	for i := 0; i < len(s); i++ {
		c := s[i]
		if (c < '0' || c > '9') && (c < 'a' || c > 'f') {
			return false
		}
	}
	return true
}

// oracleRepo: valid UTF-8, not empty, no space, tab, CR, LF, slash or backslash.
func oracleRepo(s string) bool {
	return s != "" && utf8.ValidString(s) && !strings.ContainsAny(s, " \t\r\n/\\")
}

// oracleKey returns "" for a valid key, otherwise the name of the first broken
// rule. The rule names are the ones the rejection table below uses.
func oracleKey(repo, kind, obj, head string) string {
	if !utf8.ValidString(repo) {
		return "repo-utf8"
	}
	if !oracleRepo(repo) {
		return "repo-shape"
	}
	switch kind {
	case "issues":
		if obj != "" {
			return "list-has-object"
		}
		if head != "" {
			return "unpinned-has-head"
		}
	case "issue":
		if !oracleObject(obj) {
			return "object-not-positive-decimal"
		}
		if head != "" {
			return "unpinned-has-head"
		}
	case "failed_jobs":
		if !oracleObject(obj) {
			return "object-not-positive-decimal"
		}
		if head != "" && !oracleHead(head) {
			return "head-not-full-lowercase"
		}
	case "mr_diff":
		if !oracleObject(obj) {
			return "object-not-positive-decimal"
		}
		if !oracleHead(head) {
			return "head-not-full-lowercase"
		}
	default:
		return "unknown-kind"
	}
	return ""
}

func TestOracleKeyAgreesWithItselfOnKnownCases(t *testing.T) {
	// Guard against a broken oracle: these are decided by the rules above only.
	h := strings.Repeat("a", 40)
	for _, c := range []struct {
		repo, kind, obj, head, want string
	}{
		{"rep_1", "issues", "", "", ""},
		{"rep_1", "issue", "42", "", ""},
		{"rep_1", "failed_jobs", "7", "", ""},
		{"rep_1", "failed_jobs", "7", h, ""},
		{"rep_1", "mr_diff", "9", h, ""},
		{"rep_1", "mr_diff", "9", "", "head-not-full-lowercase"},
		{"rep_1", "failed_jobs", "7", strings.ToUpper(h), "head-not-full-lowercase"},
		{"rep_1", "issue", "07", "", "object-not-positive-decimal"},
		{"rep_1", "issue", "9223372036854775808", "", "object-not-positive-decimal"},
		{"rep_1", "issue", "9223372036854775807", "", ""},
	} {
		if got := oracleKey(c.repo, c.kind, c.obj, c.head); got != c.want {
			t.Errorf("%+v: oracle says %q", c, got)
		}
	}
}

var (
	keyRepoPool = []string{"rep_1", "rep-acme-shop", "r", "rep_é", "", " ", "rep 1", "rep\t1", "rep\n1", "rep\r1", "rep/1", "rep\\1", "\x80", "rep_\xff", "re\xc3", "shop-api"}
	keyKindPool = []string{"issues", "issue", "failed_jobs", "mr_diff", "", "files", "ISSUE", "issues ", "mr-diff"}
	keyObjPool  = []string{"", "1", "7", "42", "12345", "0", "-1", "+1", "007", "01", "1.0", "1e3", " 1", "1 ", "١٢", "https://x/issues/1", "9223372036854775807", "9223372036854775808", "99999999999999999999", "0x10", "1_0"}
)

func keyHeadPool() []string {
	h40, h64 := strings.Repeat("a", 40), strings.Repeat("0123456789abcdef", 4)
	return []string{"", h40, h64, strings.ToUpper(h40), h40[:39], h40 + "a", h64[:63], h64 + "a", strings.Repeat("g", 40), strings.Repeat("a", 39) + "A", strings.Repeat("a", 39) + "\n", "abc", strings.Repeat("a", 41), strings.Repeat("a", 63)}
}

// genKeyComponents mostly builds components that fit the kind, and corrupts one
// of them from the pools at random, so both accepted and rejected keys are
// plentiful.
func genKeyComponents(r *rand.Rand) (repo, kind, obj, head string) {
	heads := keyHeadPool()
	full := []string{strings.Repeat("a", 40), strings.Repeat("0123456789abcdef", 4)}
	repo = []string{"rep_1", "rep-acme-shop", "shop-api", "rep_é"}[r.Intn(4)]
	kind = []string{"issues", "issue", "failed_jobs", "mr_diff"}[r.Intn(4)]
	nums := []string{"1", "7", "42", "12345", "9223372036854775807"}
	switch kind {
	case "issue":
		obj = nums[r.Intn(len(nums))]
	case "failed_jobs":
		obj = nums[r.Intn(len(nums))]
		if r.Intn(2) == 0 {
			head = full[r.Intn(2)]
		}
	case "mr_diff":
		obj = nums[r.Intn(len(nums))]
		head = full[r.Intn(2)]
	}
	switch r.Intn(6) { // corrupt at most one component, most often none
	case 0:
		repo = keyRepoPool[r.Intn(len(keyRepoPool))]
	case 1:
		kind = keyKindPool[r.Intn(len(keyKindPool))]
	case 2:
		obj = keyObjPool[r.Intn(len(keyObjPool))]
	case 3:
		head = heads[r.Intn(len(heads))]
	}
	return repo, kind, obj, head
}

// Accepted if and only if the independent oracle accepts, over a generated
// space of components. A failure names the seed and the key.
func TestPropContextKeyMatchesIndependentOracle(t *testing.T) {
	if strconv.IntSize != 64 {
		t.Skip("the object bound of the oracle is the 64-bit int")
	}
	accepted, rejected := 0, 0
	for seed := int64(1); seed <= 4000; seed++ {
		r := rand.New(rand.NewSource(seed))
		repo, kind, obj, head := genKeyComponents(r)
		k := ContextKey{Repo: core.RepoID(repo), Kind: ContextKind(kind), ObjectID: obj, Head: head}
		rule := oracleKey(repo, kind, obj, head)
		err := k.Validate()
		if (rule == "") != (err == nil) {
			t.Fatalf("seed %d key %+v: oracle %q, Validate %v", seed, k, rule, err)
		}
		if err == nil {
			accepted++
		} else {
			rejected++
		}
	}
	if accepted < 100 || rejected < 100 {
		t.Fatalf("the generator is lopsided: %d accepted, %d rejected", accepted, rejected)
	}
}

// Every representable accepted key survives serialization unchanged and stays
// comparable (it is used as a map key by the shell).
func TestPropContextKeyRoundTrip(t *testing.T) {
	seen := map[ContextKey]bool{}
	for seed := int64(1); seed <= 3000; seed++ {
		r := rand.New(rand.NewSource(seed))
		repo, kind, obj, head := genKeyComponents(r)
		k := ContextKey{Repo: core.RepoID(repo), Kind: ContextKind(kind), ObjectID: obj, Head: head}
		if k.Validate() != nil {
			continue
		}
		raw, err := json.Marshal(k)
		if err != nil {
			t.Fatalf("seed %d: %v", seed, err)
		}
		var back ContextKey
		if err := json.Unmarshal(raw, &back); err != nil || back != k {
			t.Fatalf("seed %d: %+v -> %s -> %+v (%v)", seed, k, raw, back, err)
		}
		if back.Validate() != nil {
			t.Fatalf("seed %d: a round-tripped key is no longer valid", seed)
		}
		seen[back] = true
	}
	if len(seen) < 20 {
		t.Fatalf("the round-trip generator reached only %d distinct keys", len(seen))
	}
}

// One test per rejection rule: a valid base key with exactly one fault.
func TestContextKeyRejectsEachRule(t *testing.T) {
	h := strings.Repeat("a", 40)
	type c struct {
		rule string
		key  ContextKey
	}
	cases := []c{
		{"repo-utf8", ContextKey{Repo: "rep_\xff", Kind: ContextIssues}},
		{"repo-shape", ContextKey{Repo: "", Kind: ContextIssues}},
		{"repo-shape", ContextKey{Repo: "rep 1", Kind: ContextIssues}},
		{"repo-shape", ContextKey{Repo: "rep\t1", Kind: ContextIssues}},
		{"repo-shape", ContextKey{Repo: "rep\n1", Kind: ContextIssues}},
		{"repo-shape", ContextKey{Repo: "rep/1", Kind: ContextIssues}},
		{"repo-shape", ContextKey{Repo: "rep\\1", Kind: ContextIssues}},
		{"unknown-kind", ContextKey{Repo: "rep_1", Kind: "files", ObjectID: "1"}},
		{"unknown-kind", ContextKey{Repo: "rep_1", Kind: "", ObjectID: "1"}},
		{"unknown-kind", ContextKey{Repo: "rep_1", Kind: "ISSUE", ObjectID: "1"}},
		{"list-has-object", ContextKey{Repo: "rep_1", Kind: ContextIssues, ObjectID: "1"}},
		{"unpinned-has-head", ContextKey{Repo: "rep_1", Kind: ContextIssues, Head: h}},
		{"unpinned-has-head", ContextKey{Repo: "rep_1", Kind: ContextIssue, ObjectID: "1", Head: h}},
		{"object-not-positive-decimal", ContextKey{Repo: "rep_1", Kind: ContextIssue, ObjectID: ""}},
		{"object-not-positive-decimal", ContextKey{Repo: "rep_1", Kind: ContextIssue, ObjectID: "0"}},
		{"object-not-positive-decimal", ContextKey{Repo: "rep_1", Kind: ContextIssue, ObjectID: "-3"}},
		{"object-not-positive-decimal", ContextKey{Repo: "rep_1", Kind: ContextIssue, ObjectID: "+3"}},
		{"object-not-positive-decimal", ContextKey{Repo: "rep_1", Kind: ContextIssue, ObjectID: "007"}},
		{"object-not-positive-decimal", ContextKey{Repo: "rep_1", Kind: ContextIssue, ObjectID: " 7"}},
		{"object-not-positive-decimal", ContextKey{Repo: "rep_1", Kind: ContextIssue, ObjectID: "7 "}},
		{"object-not-positive-decimal", ContextKey{Repo: "rep_1", Kind: ContextIssue, ObjectID: "1e3"}},
		{"object-not-positive-decimal", ContextKey{Repo: "rep_1", Kind: ContextIssue, ObjectID: "https://git.acme.test/acme/shop/issues/1"}},
		{"object-not-positive-decimal", ContextKey{Repo: "rep_1", Kind: ContextIssue, ObjectID: "9223372036854775808"}},
		{"object-not-positive-decimal", ContextKey{Repo: "rep_1", Kind: ContextFailedJobs, ObjectID: "0x10"}},
		{"object-not-positive-decimal", ContextKey{Repo: "rep_1", Kind: ContextMRDiff, ObjectID: "", Head: h}},
		{"head-not-full-lowercase", ContextKey{Repo: "rep_1", Kind: ContextMRDiff, ObjectID: "1"}},
		{"head-not-full-lowercase", ContextKey{Repo: "rep_1", Kind: ContextMRDiff, ObjectID: "1", Head: h[:39]}},
		{"head-not-full-lowercase", ContextKey{Repo: "rep_1", Kind: ContextMRDiff, ObjectID: "1", Head: h + "a"}},
		{"head-not-full-lowercase", ContextKey{Repo: "rep_1", Kind: ContextMRDiff, ObjectID: "1", Head: strings.ToUpper(h)}},
		{"head-not-full-lowercase", ContextKey{Repo: "rep_1", Kind: ContextMRDiff, ObjectID: "1", Head: strings.Repeat("g", 40)}},
		{"head-not-full-lowercase", ContextKey{Repo: "rep_1", Kind: ContextFailedJobs, ObjectID: "1", Head: "abc"}},
		{"head-not-full-lowercase", ContextKey{Repo: "rep_1", Kind: ContextFailedJobs, ObjectID: "1", Head: strings.ToUpper(h)}},
		{"head-not-full-lowercase", ContextKey{Repo: "rep_1", Kind: ContextFailedJobs, ObjectID: "1", Head: strings.Repeat("a", 63)}},
	}
	for i, tc := range cases {
		t.Run(fmt.Sprintf("%02d-%s", i, tc.rule), func(t *testing.T) {
			if got := oracleKey(string(tc.key.Repo), string(tc.key.Kind), tc.key.ObjectID, tc.key.Head); got != tc.rule {
				t.Fatalf("the case does not break exactly %q (oracle: %q)", tc.rule, got)
			}
			if tc.key.Validate() == nil {
				t.Fatalf("%+v accepted", tc.key)
			}
		})
	}
	// Every rule has at least one case.
	rules := map[string]bool{}
	for _, tc := range cases {
		rules[tc.rule] = true
	}
	for _, want := range []string{"repo-utf8", "repo-shape", "unknown-kind", "list-has-object", "unpinned-has-head", "object-not-positive-decimal", "head-not-full-lowercase"} {
		if !rules[want] {
			t.Errorf("no case for rule %s", want)
		}
	}
}

// The fuzz target compares with the independent oracle in both directions on
// arbitrary components (the older FuzzContextKey keeps its corpus and checks
// the JSON round trip).
func FuzzContextKeyAgainstOracle(f *testing.F) {
	h := strings.Repeat("a", 40)
	f.Add("rep_1", "issues", "", "")
	f.Add("rep_1", "failed_jobs", "7", h)
	f.Add("rep_1", "failed_jobs", "7", strings.ToUpper(h))
	f.Add("rep_1", "mr_diff", "9223372036854775808", h)
	f.Add("rep\x80", "issue", "1", "")
	f.Add("rep/1", "issue", "007", "")
	f.Fuzz(func(t *testing.T, repo, kind, obj, head string) {
		if strconv.IntSize != 64 {
			t.Skip()
		}
		k := ContextKey{Repo: core.RepoID(repo), Kind: ContextKind(kind), ObjectID: obj, Head: head}
		rule := oracleKey(repo, kind, obj, head)
		err := k.Validate()
		if (rule == "") != (err == nil) {
			t.Fatalf("%+v: oracle %q, Validate %v", k, rule, err)
		}
		if err == nil {
			raw, _ := json.Marshal(k)
			var back ContextKey
			if json.Unmarshal(raw, &back) != nil || back != k {
				t.Fatalf("round trip %+v", k)
			}
		}
	})
}

// ---- ContextPayloadMatches against an independent oracle ----

func oraclePayload(k ContextKey, p ContextPayload) bool {
	switch v := p.(type) {
	case JobLogs:
		if k.Kind != ContextFailedJobs || v.RunID != k.ObjectID || !oracleHead(v.Head) || len(v.Jobs) > 500 {
			return false
		}
		if k.Head != "" && v.Head != k.Head {
			return false
		}
		for _, j := range v.Jobs {
			if !oracleObject(j.JobID) || len(j.Log) > 256*1024 {
				return false
			}
		}
		return true
	case IssueList:
		if k.Kind != ContextIssues || len(v.Items) > 500 {
			return false
		}
		for _, it := range v.Items {
			if it.Number < 1 {
				return false
			}
		}
		return true
	case IssueDetail:
		return k.Kind == ContextIssue && strconv.Itoa(v.Summary.Number) == k.ObjectID && v.Summary.Number >= 1 && len(v.Body) <= 64*1024
	case MRContext:
		return k.Kind == ContextMRDiff && strconv.Itoa(v.MR.Number) == k.ObjectID && v.MR.Number >= 1 && oracleHead(v.Head) && v.Head == k.Head && len(v.Diff) <= 64*1024
	}
	return false
}

func TestPropContextPayloadMatchesIndependentOracle(t *testing.T) {
	ha, hb := strings.Repeat("a", 40), strings.Repeat("b", 40)
	keys := []ContextKey{
		{Repo: "rep_1", Kind: ContextIssues},
		{Repo: "rep_1", Kind: ContextIssue, ObjectID: "5"},
		{Repo: "rep_1", Kind: ContextMRDiff, ObjectID: "5", Head: ha},
		{Repo: "rep_1", Kind: ContextFailedJobs, ObjectID: "5", Head: ha},
		{Repo: "rep_1", Kind: ContextFailedJobs, ObjectID: "5"},
	}
	mk := func(r *rand.Rand) ContextPayload {
		num := []int{5, 5, 5, 6, 0, -5}[r.Intn(6)]
		head := []string{ha, ha, hb, "", strings.ToUpper(ha), ha[:39]}[r.Intn(6)]
		switch r.Intn(6) {
		case 0:
			var jobs []JobLog
			for i, n := 0, r.Intn(4); i < n; i++ {
				id := []string{"1", "2", "0", "x", "007", ""}[r.Intn(6)]
				log := ""
				switch r.Intn(5) {
				case 0:
					log = strings.Repeat("l", 256*1024)
				case 1:
					log = strings.Repeat("l", 256*1024+1)
				}
				jobs = append(jobs, JobLog{JobID: id, Log: log})
			}
			if r.Intn(10) == 0 {
				jobs = make([]JobLog, 500+r.Intn(2))
				for i := range jobs {
					jobs[i].JobID = "1"
				}
			}
			return JobLogs{RunID: []string{"5", "5", "6", ""}[r.Intn(4)], Head: head, Jobs: jobs}
		case 1:
			items := make([]IssueSummary, r.Intn(3))
			for i := range items {
				items[i].Number = []int{1, 2, 0, -1}[r.Intn(4)]
			}
			if r.Intn(8) == 0 {
				items = make([]IssueSummary, 500+r.Intn(2))
				for i := range items {
					items[i].Number = 1
				}
			}
			return IssueList{Items: items}
		case 2:
			body := ""
			if r.Intn(3) == 0 {
				body = strings.Repeat("b", 64*1024+r.Intn(3)-1)
			}
			return IssueDetail{Summary: IssueSummary{Number: num}, Body: body}
		case 3:
			diff := ""
			if r.Intn(3) == 0 {
				diff = strings.Repeat("d", 64*1024+r.Intn(3)-1)
			}
			return MRContext{MR: MergeRequest{Number: num}, Head: head, Diff: diff}
		case 4:
			return nil
		}
		return IssueList{}
	}
	yes, no := 0, 0
	for seed := int64(1); seed <= 6000; seed++ {
		r := rand.New(rand.NewSource(seed))
		k := keys[r.Intn(len(keys))]
		p := mk(r)
		got, want := ContextPayloadMatches(k, p), oraclePayload(k, p)
		if got != want {
			t.Fatalf("seed %d key %+v payload %T: ContextPayloadMatches %v, oracle %v", seed, k, p, got, want)
		}
		if got {
			yes++
		} else {
			no++
		}
	}
	if yes < 200 || no < 200 {
		t.Fatalf("lopsided generator: %d match, %d refuse", yes, no)
	}
}

// Exact bounds, both sides.
func TestContextPayloadMatchesBounds(t *testing.T) {
	h := strings.Repeat("a", 40)
	jobKey := ContextKey{Repo: "rep_1", Kind: ContextFailedJobs, ObjectID: "1", Head: h}
	okLog := JobLogs{RunID: "1", Head: h, Jobs: []JobLog{{JobID: "1", Log: strings.Repeat("x", 256*1024)}}}
	if !ContextPayloadMatches(jobKey, okLog) {
		t.Error("a 256 KiB log must match")
	}
	okLog.Jobs[0].Log += "x"
	if ContextPayloadMatches(jobKey, okLog) {
		t.Error("a 256 KiB + 1 log must not match")
	}
	mrKey := ContextKey{Repo: "rep_1", Kind: ContextMRDiff, ObjectID: "1", Head: h}
	if !ContextPayloadMatches(mrKey, MRContext{MR: MergeRequest{Number: 1}, Head: h, Diff: strings.Repeat("d", 64*1024)}) {
		t.Error("a 64 KiB diff must match")
	}
	if ContextPayloadMatches(mrKey, MRContext{MR: MergeRequest{Number: 1}, Head: h, Diff: strings.Repeat("d", 64*1024+1)}) {
		t.Error("a 64 KiB + 1 diff must not match")
	}
	if ContextPayloadMatches(mrKey, nil) {
		t.Error("a nil payload must not match")
	}
}
