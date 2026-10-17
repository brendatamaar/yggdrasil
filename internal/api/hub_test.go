package api

import (
	"encoding/json"
	"io"
	"log/slog"
	"sync"
	"testing"
)

// subscribedClient registers a client with no connection on the hub. Its send
// buffer is large enough that no test publisher is dropped as slow.
func subscribedClient(h *hub, buf int, topics ...wsTopic) *client {
	c := &client{send: make(chan []byte, buf), topics: map[string]struct{}{}}
	for _, t := range topics {
		c.topics[t.String()] = struct{}{}
	}
	h.mu.Lock()
	h.clients[c] = struct{}{}
	h.mu.Unlock()
	return c
}

func drainSeqs(t *testing.T, c *client) []int64 {
	t.Helper()
	var seqs []int64
	for {
		select {
		case raw := <-c.send:
			var m WSMessage
			if err := json.Unmarshal(raw, &m); err != nil {
				t.Fatalf("decode: %v", err)
			}
			seqs = append(seqs, m.Seq)
		default:
			return seqs
		}
	}
}

func assertStrictlyIncreasingNoGaps(t *testing.T, seqs []int64, want int) {
	t.Helper()
	if len(seqs) != want {
		t.Fatalf("got %d messages, want %d", len(seqs), want)
	}
	for i, s := range seqs {
		if s != int64(i+1) {
			t.Fatalf("message %d has seq %d, want %d (out of order or gap)", i, s, i+1)
		}
	}
}

func TestPublishNextDeliversSeqInOrder(t *testing.T) {
	const publishers, each = 32, 200
	h := newHub(slog.New(slog.NewTextHandler(io.Discard, nil)))
	c := subscribedClient(h, publishers*each, tasksTopic{})

	var wg sync.WaitGroup
	for range publishers {
		wg.Add(1)
		go func() {
			defer wg.Done()
			for range each {
				h.publishNext(tasksTopic{}, map[string]string{"k": "v"})
			}
		}()
	}
	wg.Wait()
	assertStrictlyIncreasingNoGaps(t, drainSeqs(t, c), publishers*each)
}

func TestPublishSeqKeepsCallerSeqAndTopics(t *testing.T) {
	h := newHub(slog.New(slog.NewTextHandler(io.Discard, nil)))
	a := subscribedClient(h, 8, taskTopic{ID: "a"})
	b := subscribedClient(h, 8, taskTopic{ID: "b"})

	h.publishSeq(taskTopic{ID: "a"}, 7, "x")
	if got := drainSeqs(t, a); len(got) != 1 || got[0] != 7 {
		t.Fatalf("a got %v, want [7]", got)
	}
	if got := drainSeqs(t, b); len(got) != 0 {
		t.Fatalf("b got %v, want none", got)
	}
}
