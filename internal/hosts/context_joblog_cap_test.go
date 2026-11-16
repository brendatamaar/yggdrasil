package hosts

// Regression tests of the per-job log cap and the aggregate response budget
// (GH-05, TM1-H). The first version of the failed-job read bounded one log by the
// REMAINING 1 MiB aggregate instead of min(remaining, 256 KiB). These tests
// measure what the Getter actually pulled off the wire with their own counting
// transport, so they do not depend on the production accounting (readBytes).

import (
	"context"
	"fmt"
	"io"
	"net/http"
	"strconv"
	"strings"
	"sync"
	"testing"

	corehosts "github.com/brendatamaar/yggdrasil/internal/core/hosts"
)

// byteCounter wraps a transport and counts the body bytes the caller READ per
// request path.
type byteCounter struct {
	next http.RoundTripper
	mu   sync.Mutex
	read map[string]int
	reqs []string
}

func newByteCounter(next http.RoundTripper) *byteCounter {
	return &byteCounter{next: next, read: map[string]int{}}
}

func (c *byteCounter) RoundTrip(r *http.Request) (*http.Response, error) {
	c.mu.Lock()
	c.reqs = append(c.reqs, r.URL.Path)
	c.mu.Unlock()
	res, err := c.next.RoundTrip(r)
	if res != nil && res.Body != nil {
		res.Body = &countedBody{ReadCloser: res.Body, c: c, path: r.URL.Path}
	}
	return res, err
}

type countedBody struct {
	io.ReadCloser
	c    *byteCounter
	path string
}

func (b *countedBody) Read(p []byte) (int, error) {
	n, err := b.ReadCloser.Read(p)
	b.c.mu.Lock()
	b.c.read[b.path] += n
	b.c.mu.Unlock()
	return n, err
}

// total is every body byte read so far; of is the bytes read from paths that end
// with suffix.
func (c *byteCounter) total() int {
	c.mu.Lock()
	defer c.mu.Unlock()
	n := 0
	for _, v := range c.read {
		n += v
	}
	return n
}

func (c *byteCounter) of(suffix string) (n, requests int) {
	c.mu.Lock()
	defer c.mu.Unlock()
	for p, v := range c.read {
		if strings.HasSuffix(p, suffix) {
			n += v
		}
	}
	for _, p := range c.reqs {
		if strings.HasSuffix(p, suffix) {
			requests++
		}
	}
	return n, requests
}

func (c *byteCounter) countLogs(lab bool) (bytes, requests int) {
	suffix := "/logs"
	if lab {
		suffix = "/trace"
	}
	return c.of(suffix)
}

// writeSized streams size bytes in chunks and stops at the first write error
// (the client went away).
func writeSized(w http.ResponseWriter, size int, fill byte) {
	chunk := []byte(strings.Repeat(string([]byte{fill}), 32<<10))
	for size > 0 {
		n := min(size, len(chunk))
		if _, err := w.Write(chunk[:n]); err != nil {
			return
		}
		size -= n
	}
}

// jobsFixture serves one failed-job run of a GitLab or GitHub-shaped host.
type jobsFixture struct {
	lab    bool
	head   string
	jobs   int
	runPad int // unrelated bytes in the run answer, to spend the aggregate budget
	// log says what job id answers: a status and a body size.
	log func(id int) (status, size int)
	// fill is the byte the log bodies are made of ('x' by default). 0xff is
	// invalid UTF-8, so such a log is large on the wire and empty as text.
	fill byte
}

func (f jobsFixture) api(t *testing.T) (ContextAPI, corehosts.Instance, *byteCounter) {
	t.Helper()
	srv, in, _ := contextFixture(t, func(w http.ResponseWriter, r *http.Request) {
		p := r.URL.Path
		switch {
		case strings.HasSuffix(p, "/trace") || strings.HasSuffix(p, "/logs"):
			parts := strings.Split(p, "/")
			id, _ := strconv.Atoi(parts[len(parts)-2])
			status, size := f.log(id)
			w.WriteHeader(status)
			fill := f.fill
			if fill == 0 {
				fill = 'x'
			}
			writeSized(w, size, fill)
		case strings.HasSuffix(p, "/jobs"):
			var items []string
			for i := 1; i <= f.jobs; i++ {
				if f.lab {
					items = append(items, fmt.Sprintf(`{"id":%d,"name":"job-%d","stage":"test","status":"failed"}`, i, i))
				} else {
					items = append(items, fmt.Sprintf(`{"id":%d,"name":"job-%d","conclusion":"failure"}`, i, i))
				}
			}
			if f.lab {
				fmt.Fprintf(w, "[%s]", strings.Join(items, ","))
			} else {
				fmt.Fprintf(w, `{"jobs":[%s]}`, strings.Join(items, ","))
			}
		default:
			fmt.Fprintf(w, `{"id":12,"sha":%q,"head_sha":%q,"pad":%q}`, f.head, f.head, strings.Repeat("p", f.runPad))
		}
	})
	counter := newByteCounter(srv.Client().Transport)
	g := NewGetter(&http.Client{Transport: counter}, nil)
	root := srv.URL
	return ContextAPI{Getter: g, Root: root, GitLab: f.lab}, in, counter
}

func (f jobsFixture) read(t *testing.T) (corehosts.JobLogs, []corehosts.ContextUnavailable, *byteCounter, error) {
	t.Helper()
	api, in, counter := f.api(t)
	res, err := api.FailedJobs(context.Background(), in, "sentinel-secret", "acme/shop", "12", f.head)
	jl, _ := res.Value.(corehosts.JobLogs)
	return jl, res.Unavailable, counter, err
}

// The lead-repair regression: whatever the body of one log, the bytes consumed
// for it are at most min(remaining aggregate, 256 KiB) plus one overflow byte,
// and an overflow is a labelled, explicit partial result.
func TestHostContextPerJobLogCapConsumesAtMostLimitPlusSentinel(t *testing.T) {
	const cap = corehosts.ContextMaxJobLogBytes
	head := strings.Repeat("a", 40)
	cases := []struct {
		name string
		// runPad spends part of the aggregate budget before the log is read.
		runPad   int
		bodySize int
		overflow bool
	}{
		{"body exactly at the per-job limit", 0, cap, false},
		{"body one byte over the limit", 0, cap + 1, true},
		{"body far larger than the limit", 0, 6 << 20, true},
		{"body 1 MiB, remaining aggregate far above the cap", 0, corehosts.ContextMaxResponseBytes, true},
		{"remaining aggregate below the cap, body within it", corehosts.ContextMaxResponseBytes - 150<<10, 10 << 10, false},
		{"remaining aggregate below the cap, body over it", corehosts.ContextMaxResponseBytes - 150<<10, 1 << 20, true},
		{"remaining aggregate below the cap, far larger body", corehosts.ContextMaxResponseBytes - 150<<10, 6 << 20, true},
	}
	for _, lab := range []bool{false, true} {
		for _, c := range cases {
			t.Run(fmt.Sprintf("gitlab=%v/%s", lab, c.name), func(t *testing.T) {
				f := jobsFixture{lab: lab, head: head, jobs: 1, runPad: c.runPad, log: func(int) (int, int) { return 200, c.bodySize }}
				jl, gaps, counter, err := f.read(t)
				if err != nil {
					t.Fatal(err)
				}
				// What was spent before the log request: everything but the log.
				before := counter.total()
				logBytes, logReqs := counter.countLogs(lab)
				before -= logBytes
				remaining := corehosts.ContextMaxResponseBytes - before
				limit := min(remaining, cap)
				if logReqs != 1 {
					t.Fatalf("%d log requests, want 1", logReqs)
				}
				if logBytes > limit+1 {
					t.Fatalf("consumed %d bytes of the log, want at most min(remaining %d, %d)+1 = %d", logBytes, remaining, cap, limit+1)
				}
				if len(jl.Jobs) != 1 {
					t.Fatalf("jobs = %+v", jl.Jobs)
				}
				job := jl.Jobs[0]
				if c.overflow {
					if job.LogUnavailable == "" || job.Log != "" {
						t.Fatalf("overflow is not labelled on the job: log %d bytes, LogUnavailable %q", len(job.Log), job.LogUnavailable)
					}
					if len(gaps) == 0 || gaps[0].Reason != corehosts.ContextTooLarge || !strings.Contains(gaps[0].Source, "job log 1") {
						t.Fatalf("overflow is not a labelled gap: %+v", gaps)
					}
					if !jl.Truncated {
						t.Fatal("overflow did not mark the result truncated")
					}
				} else {
					if job.LogUnavailable != "" {
						t.Fatalf("a log within the limit was labelled unavailable: %q", job.LogUnavailable)
					}
					for _, g := range gaps {
						if g.Reason == corehosts.ContextTooLarge {
							t.Fatalf("a log within the limit produced a too_large gap: %+v", g)
						}
					}
					if job.Log == "" {
						t.Fatal("a log within the limit was lost")
					}
				}
			})
		}
	}
}

// The aggregate budget must stay meaningful once the per-job cap exists: logs
// that are each BELOW the cap stop the read only because the 1 MiB aggregate is
// spent, and the actual bytes consumed never pass the budget by more than the
// one overflow sentinel.
func TestHostContextAggregateBudgetStopsReadsBelowThePerJobCap(t *testing.T) {
	head := strings.Repeat("a", 40)
	const each = 200 << 10 // under the 256 KiB cap
	for _, lab := range []bool{false, true} {
		t.Run(fmt.Sprintf("gitlab=%v", lab), func(t *testing.T) {
			// Logs of invalid UTF-8: heavy on the wire, empty as text, so the 64 KiB
			// prompt bound does not hide how many jobs the read kept.
			f := jobsFixture{lab: lab, head: head, jobs: 12, fill: 0xff, log: func(int) (int, int) { return 200, each }}
			jl, gaps, counter, err := f.read(t)
			if err != nil {
				t.Fatal(err)
			}
			if total := counter.total(); total > corehosts.ContextMaxResponseBytes+1 {
				t.Fatalf("consumed %d bytes, budget is %d (+1 sentinel)", total, corehosts.ContextMaxResponseBytes)
			}
			_, logReqs := counter.countLogs(lab)
			// 5 whole logs fit (1000 KiB); the 6th gets the 24 KiB left and overflows.
			if logReqs != 6 {
				t.Fatalf("%d log requests, want 6 (5 within budget, then the overflowing one)", logReqs)
			}
			if !jl.Truncated || len(jl.Jobs) != 6 {
				t.Fatalf("truncated=%v jobs=%d, want a labelled partial of 6 jobs", jl.Truncated, len(jl.Jobs))
			}
			last := jl.Jobs[5]
			if last.LogUnavailable == "" || last.Log != "" {
				t.Fatalf("the job that overran the budget is not labelled: log %d bytes, LogUnavailable %q", len(last.Log), last.LogUnavailable)
			}
			var found bool
			for _, g := range gaps {
				if g.Source == "job log 6" && g.Reason == corehosts.ContextTooLarge {
					found = true
				}
			}
			if !found {
				t.Fatalf("no too_large gap for the overrunning job: %+v", gaps)
			}
		})
	}
}

// A failed read (here 500) still spends what was read from its body, so a host
// that answers every log with an error and a big body cannot make the reader
// pull more than the aggregate budget.
func TestHostContextFailedLogReadsSpendTheBudget(t *testing.T) {
	head := strings.Repeat("a", 40)
	const each = 100 << 10
	for _, lab := range []bool{false, true} {
		t.Run(fmt.Sprintf("gitlab=%v", lab), func(t *testing.T) {
			f := jobsFixture{lab: lab, head: head, jobs: 30, log: func(int) (int, int) { return 500, each }}
			jl, gaps, counter, err := f.read(t)
			if err != nil {
				t.Fatal(err)
			}
			if total := counter.total(); total > corehosts.ContextMaxResponseBytes+1 {
				t.Fatalf("consumed %d bytes, budget is %d (+1 sentinel)", total, corehosts.ContextMaxResponseBytes)
			}
			_, logReqs := counter.countLogs(lab)
			if logReqs >= 30 || logReqs < 10 {
				t.Fatalf("%d log requests: the budget did not stop the read after about 11", logReqs)
			}
			if !jl.Truncated {
				t.Fatal("a read stopped by the budget is not labelled truncated")
			}
			if len(gaps) != len(jl.Jobs) {
				t.Fatalf("%d jobs but %d labelled gaps: a failed log went unlabelled", len(jl.Jobs), len(gaps))
			}
			for _, j := range jl.Jobs {
				if j.LogUnavailable == "" || j.Log != "" {
					t.Fatalf("a failed log is shown as data: job %s log %d bytes, LogUnavailable %q", j.JobID, len(j.Log), j.LogUnavailable)
				}
			}
		})
	}
}
