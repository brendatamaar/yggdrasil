package hosts

import (
	"encoding/json"
	"fmt"
	"math/rand"
	"reflect"
	"strings"
	"testing"
	"time"

	"github.com/brendatamaar/yggdrasil/internal/core"
)

func TestContextTransitions(t *testing.T) {
	key := ContextKey{Repo: "rep_1", Kind: ContextIssues}
	now := time.Unix(100, 0)
	e := ContextEntry{Key: key, State: CtxEmpty{}}
	e, cmd := ContextStep(e, ContextRequested{Key: key, Scope: ScopeTracked, Now: now})
	if _, ok := e.State.(CtxLoading); !ok || e.Seq != 1 || len(cmd) != 1 {
		t.Fatal(e, cmd)
	}
	unchanged, c := ContextStep(e, ContextFetched{Key: key, Attempt: 0, Value: IssueList{}, Now: now})
	if !reflect.DeepEqual(e, unchanged) || len(c) != 0 {
		t.Fatal("stale result accepted")
	}
	for retry := 1; retry <= 3; retry++ {
		e, _ = ContextStep(e, ContextFetchFailed{Key: key, Attempt: e.Seq, Class: FailTransient, Now: now})
		if retry == 3 {
			if _, ok := e.State.(CtxBlocked); !ok {
				t.Fatal(e)
			}
			break
		}
		waiting := e.State.(CtxWaiting)
		if waiting.At != now.Add(ContextBackoff(retry)) {
			t.Fatal(waiting)
		}
		early, _ := ContextStep(e, ContextWake{Attempt: e.Seq, Now: now})
		if !reflect.DeepEqual(e, early) {
			t.Fatal("early wake")
		}
		e, _ = ContextStep(e, ContextWake{Attempt: e.Seq, Now: waiting.At})
	}
	e, _ = ContextStep(e, ContextRequested{Key: key, Scope: ScopeTracked, Now: now})
	e, _ = ContextStep(e, ContextFetched{Key: key, Attempt: e.Seq, Value: IssueList{}, Now: now})
	cached, c := ContextStep(e, ContextRequested{Key: key, Scope: ScopeTracked, Now: now.Add(time.Second)})
	if !reflect.DeepEqual(e, cached) {
		t.Fatal("cache lost")
	}
	if _, ok := c[0].(CtxReturnCached); !ok {
		t.Fatal(c)
	}
	expired, _ := ContextStep(e, ContextRequested{Key: key, Scope: ScopeTracked, Now: now.Add(ContextCacheTTL)})
	if _, ok := expired.State.(CtxLoading); !ok {
		t.Fatal("cache never expires")
	}
	untracked, _ := ContextStep(e, ContextRequested{Key: key, Scope: ScopeUntracked, Now: now})
	if untracked.State.(CtxBlocked).Reason != ContextUntracked {
		t.Fatal("untracked cache exposed")
	}
}

func contextSequence(seed int64) []ContextInput {
	r := rand.New(rand.NewSource(seed))
	key := ContextKey{Repo: "rep_1", Kind: ContextIssues}
	other := key
	other.Repo = "rep_2"
	var inputs []ContextInput
	for i := 0; i < 100; i++ {
		now := time.Unix(int64(i*5), 0)
		attempt := r.Intn(8)
		k := key
		if r.Intn(5) == 0 {
			k = other
		}
		switch r.Intn(8) {
		case 0:
			scope := ScopeTracked
			if r.Intn(3) == 0 {
				scope = ScopeUntracked
			}
			inputs = append(inputs, ContextRequested{Key: k, Scope: scope, Now: now})
		case 1:
			inputs = append(inputs, ContextFetched{Key: k, Attempt: attempt, Value: IssueList{}, Now: now})
		case 2:
			inputs = append(inputs, ContextFetchFailed{Key: k, Attempt: attempt, Class: FailTransient, Now: now})
		case 3:
			inputs = append(inputs, ContextWake{Attempt: attempt, Now: now})
		case 4:
			inputs = append(inputs, ContextCancelled{Attempt: attempt})
		case 5:
			inputs = append(inputs, ContextScopeChanged{Scope: ScopeUntracked})
		case 6:
			inputs = append(inputs, ContextConfigChanged{Now: now})
		case 7:
			inputs = append(inputs, ContextRequested{Key: k, Scope: ScopeUnsupported, Now: now})
		}
	}
	return inputs
}
func checkContextSequence(ins []ContextInput) error {
	e := ContextEntry{Key: ContextKey{Repo: "rep_1", Kind: ContextIssues}, State: CtxEmpty{}}
	for _, in := range ins {
		next, cmd := ContextStep(e, in)
		if next.Seq < e.Seq || next.Retries > ContextMaxRetries {
			return fmt.Errorf("attempt/retry bounds")
		}
		if v, ok := in.(ContextFetched); ok {
			load, loading := e.State.(CtxLoading)
			if (!loading || load.Attempt != v.Attempt || v.Key != e.Key) && (!reflect.DeepEqual(e, next) || len(cmd) > 0) {
				return fmt.Errorf("late result")
			}
		}
		if v, ok := in.(ContextRequested); ok && v.Scope != ScopeTracked && v.Key == e.Key {
			for _, c := range cmd {
				if _, yes := c.(CtxReturnCached); yes {
					return fmt.Errorf("untracked cache")
				}
				if _, yes := c.(CtxReadContext); yes {
					return fmt.Errorf("untracked read")
				}
			}
		}
		e = next
	}
	return nil
}
func TestPropContextFencingScopeAndRetries(t *testing.T) {
	for seed := int64(1); seed <= 300; seed++ {
		inputs := contextSequence(seed)
		if err := checkContextSequence(inputs); err != nil {
			for i := 0; i < len(inputs); {
				candidate := append(append([]ContextInput(nil), inputs[:i]...), inputs[i+1:]...)
				if checkContextSequence(candidate) != nil {
					inputs = candidate
					i = 0
				} else {
					i++
				}
			}
			t.Fatalf("seed %d: %v; minimized: %#v", seed, err, inputs)
		}
	}
}
func TestContextHeadFenceAndCancellation(t *testing.T) {
	head := strings.Repeat("a", 40)
	key := ContextKey{Repo: "rep_1", Kind: ContextMRDiff, ObjectID: "1", Head: head}
	e, _ := ContextStep(ContextEntry{Key: key, State: CtxEmpty{}}, ContextRequested{Key: key, Scope: ScopeTracked})
	other := MRContext{MR: MergeRequest{Number: 1}, Head: strings.Repeat("b", 40)}
	next, cmd := ContextStep(e, ContextFetched{Key: key, Attempt: e.Seq, Value: other})
	if !reflect.DeepEqual(e, next) || len(cmd) != 0 {
		t.Fatal("wrong head published")
	}
	cancelled, _ := ContextStep(e, ContextCancelled{Attempt: e.Seq})
	restarted, _ := ContextStep(cancelled, ContextRequested{Key: key, Scope: ScopeTracked})
	if restarted.Seq <= e.Seq {
		t.Fatal("attempt reused")
	}
	next, cmd = ContextStep(restarted, ContextFetched{Key: key, Attempt: e.Seq, Value: MRContext{MR: MergeRequest{Number: 1}, Head: head}})
	if !reflect.DeepEqual(restarted, next) || len(cmd) > 0 {
		t.Fatal("cancelled attempt published")
	}
}
func FuzzContextKey(f *testing.F) {
	f.Add("rep_1", "issue", "1", "")
	f.Add("rep_1", "mr_diff", "1", strings.Repeat("a", 40))
	f.Fuzz(func(t *testing.T, repo, kind, id, head string) {
		k := ContextKey{Repo: core.RepoID(repo), Kind: ContextKind(kind), ObjectID: id, Head: head}
		if k.Validate() != nil {
			return
		}
		raw, err := json.Marshal(k)
		if err != nil {
			t.Fatal(err)
		}
		var decoded ContextKey
		if json.Unmarshal(raw, &decoded) != nil || decoded != k {
			t.Fatal("key round trip")
		}
		if k.Repo == "" || k.Kind == ContextIssues && k.ObjectID != "" {
			t.Fatal("invalid accepted")
		}
	})
}

func TestContextRejectsMalformedPayloadBoundaries(t *testing.T) {
	head := strings.Repeat("a", 40)
	key := ContextKey{Repo: "rep_1", Kind: ContextFailedJobs, ObjectID: "1", Head: head}
	for name, payload := range map[string]ContextPayload{
		"bad job id":    JobLogs{RunID: "1", Head: head, Jobs: []JobLog{{JobID: "https://foreign.test/1"}}},
		"missing head":  JobLogs{RunID: "1", Jobs: []JobLog{{JobID: "1"}}},
		"too many jobs": JobLogs{RunID: "1", Head: head, Jobs: make([]JobLog, ContextMaxEntries+1)},
		"oversized log": JobLogs{RunID: "1", Head: head, Jobs: []JobLog{{JobID: "1", Log: strings.Repeat("x", ContextMaxJobLogBytes+1)}}},
	} {
		t.Run(name, func(t *testing.T) {
			e, _ := ContextStep(ContextEntry{Key: key, State: CtxEmpty{}}, ContextRequested{Key: key, Scope: ScopeTracked})
			got, cmd := ContextStep(e, ContextFetched{Key: key, Attempt: e.Seq, Value: payload})
			if !reflect.DeepEqual(got, e) || len(cmd) > 0 {
				t.Fatal("malformed payload published")
			}
		})
	}
	issueKey := ContextKey{Repo: "rep_1", Kind: ContextIssues}
	e, _ := ContextStep(ContextEntry{Key: issueKey, State: CtxEmpty{}}, ContextRequested{Key: issueKey, Scope: ScopeTracked})
	got, cmd := ContextStep(e, ContextFetched{Key: issueKey, Attempt: e.Seq, Value: IssueList{}, Unavailable: []ContextUnavailable{{Source: "log", Reason: "unparsed outside text"}}})
	if !reflect.DeepEqual(got, e) || len(cmd) > 0 {
		t.Fatal("unparsed reason published")
	}
}
