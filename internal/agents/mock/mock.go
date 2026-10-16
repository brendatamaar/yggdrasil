// Package mock is an agents.Adapter that replays JSONL fixtures. It starts no
// process and is what every test uses instead of a real agent CLI.
package mock

import (
	"bufio"
	"bytes"
	"context"
	"embed"
	"encoding/json"
	"errors"
	"fmt"
	"time"

	"github.com/brendatamaar/yggdrasil/internal/agents"
)

//go:embed testdata/*.jsonl
var fixtures embed.FS

// Options select and pace a replay.
type Options struct {
	// Fixture is the name of a file in testdata without ".jsonl". Default "plain".
	Fixture string
	// Delay is the pause before each event. Zero by default, so tests are fast.
	Delay time.Duration
}

// Adapter replays fixtures.
type Adapter struct{ opts Options }

// New returns a mock adapter.
func New(opts Options) *Adapter {
	if opts.Fixture == "" {
		opts.Fixture = "plain"
	}
	return &Adapter{opts: opts}
}

func (*Adapter) Name() string { return "mock" }

func (*Adapter) Capabilities() agents.Capabilities {
	// Only what the mock really does: it replays nested events, tags a resumed
	// session and reports cost. It enforces nothing, so claiming hooks or
	// sub-agent limits would let later safety tests pass against a false claim.
	return agents.Capabilities{
		Resume:        true,
		CostReporting: true,
		Enforcement:   agents.EnforcementLimited,
		Subagents:     agents.SubagentsNested,
	}
}

func (*Adapter) Check(context.Context) error { return nil }

// Load parses one embedded fixture. Unknown JSON fields are an error.
func Load(name string) ([]agents.Event, error) {
	data, err := fixtures.ReadFile("testdata/" + name + ".jsonl")
	if err != nil {
		return nil, fmt.Errorf("mock fixture %q not found: %w", name, err)
	}
	var events []agents.Event
	sc := bufio.NewScanner(bytes.NewReader(data))
	sc.Buffer(nil, 1<<20)
	for n := 1; sc.Scan(); n++ {
		line := bytes.TrimSpace(sc.Bytes())
		if len(line) == 0 {
			continue
		}
		var e agents.Event
		dec := json.NewDecoder(bytes.NewReader(line))
		dec.DisallowUnknownFields()
		if err := dec.Decode(&e); err != nil {
			return nil, fmt.Errorf("mock fixture %q line %d: %w", name, n, err)
		}
		events = append(events, e)
	}
	return events, sc.Err()
}

// Start loads the fixture and replays it in one goroutine that ends when the
// replay ends or ctx is cancelled.
func (a *Adapter) Start(ctx context.Context, opts agents.StartOptions) (agents.Run, error) {
	events, err := Load(a.opts.Fixture)
	if err != nil {
		return nil, err
	}
	session := "mock-" + a.opts.Fixture
	if opts.ResumeSessionID != "" {
		session = "mock-resumed-" + opts.ResumeSessionID
	}
	r := &run{
		// Room for every event plus the final cancelled one: the goroutine
		// never blocks on a consumer that has gone away.
		events:  make(chan agents.Event, len(events)+1),
		done:    make(chan struct{}),
		session: session,
	}
	go r.replay(ctx, events, a.opts.Delay)
	return r, nil
}

type run struct {
	events  chan agents.Event
	done    chan struct{}
	session string
	err     error
}

func (r *run) Events() <-chan agents.Event { return r.events }
func (r *run) SessionID() string           { return r.session }

func (r *run) Wait() error {
	<-r.done
	return r.err
}

func (r *run) replay(ctx context.Context, events []agents.Event, delay time.Duration) {
	defer close(r.done)
	defer close(r.events)
	for _, e := range events {
		if delay > 0 {
			t := time.NewTimer(delay)
			select {
			case <-t.C:
			case <-ctx.Done():
				t.Stop()
			}
		}
		if ctx.Err() != nil {
			r.events <- agents.Event{Kind: agents.EventStatus, Status: agents.RunCancelled, At: time.Now()}
			return
		}
		e.At = time.Now()
		r.events <- e
		if e.Kind == agents.EventStatus && e.Status == agents.RunFailed {
			r.err = errors.New("mock run failed")
		}
	}
}
