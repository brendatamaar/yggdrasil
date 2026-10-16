package api

import (
	"context"
	"encoding/json"
	"log/slog"
	"net/http"
	"strings"
	"sync"
	"time"

	"github.com/coder/websocket"

	"github.com/brendatamaar/yggdrasil/internal/core"
)

const (
	clientBuffer = 256
	writeTimeout = 10 * time.Second
	pingEvery    = 30 * time.Second
	maxWSMessage = 64 << 10
)

// hub fans messages out to WebSocket clients by topic. Publishing never
// blocks: a client whose buffer is full is dropped, because it would
// otherwise hold up every task event (and the run that produces them).
type hub struct {
	log *slog.Logger

	mu      sync.Mutex
	clients map[*client]struct{}
	seq     map[string]int64
	closed  bool

	wg sync.WaitGroup // one per connection handler
}

type client struct {
	conn   *websocket.Conn
	send   chan []byte
	topics map[string]struct{} // guarded by hub.mu
	drop   sync.Once
}

func newHub(log *slog.Logger) *hub {
	return &hub{log: log, clients: map[*client]struct{}{}, seq: map[string]int64{}}
}

// nextSeq returns the next sequence number of a topic that has no sequence of
// its own (events carry theirs).
func (h *hub) nextSeq(topic string) int64 {
	h.mu.Lock()
	defer h.mu.Unlock()
	h.seq[topic]++
	return h.seq[topic]
}

// publish sends data on topic with the given sequence number.
func (h *hub) publish(topic string, seq int64, data any) {
	raw, err := json.Marshal(data)
	if err != nil {
		h.log.Error("encode ws data", "topic", topic, "err", err)
		return
	}
	msg, err := json.Marshal(WSMessage{Type: MsgEvent, Topic: topic, Seq: seq, Data: raw})
	if err != nil {
		h.log.Error("encode ws message", "topic", topic, "err", err)
		return
	}
	h.mu.Lock()
	var slow []*client
	for c := range h.clients {
		if _, ok := c.topics[topic]; !ok {
			continue
		}
		select {
		case c.send <- msg:
		default:
			slow = append(slow, c)
		}
	}
	h.mu.Unlock()
	for _, c := range slow {
		h.log.Warn("dropping a slow websocket client", "topic", topic)
		h.dropClient(c, websocket.StatusPolicyViolation, "too slow")
	}
}

// dropClient removes c and closes its connection once.
func (h *hub) dropClient(c *client, code websocket.StatusCode, reason string) {
	c.drop.Do(func() {
		h.mu.Lock()
		delete(h.clients, c)
		h.mu.Unlock()
		// Close from its own goroutine: the handshake can take a moment and
		// publish must not wait for it.
		h.wg.Add(1)
		go func() {
			defer h.wg.Done()
			_ = c.conn.Close(code, reason)
		}()
	})
}

// closeAll refuses new clients and closes the existing ones.
func (h *hub) closeAll() {
	h.mu.Lock()
	h.closed = true
	all := make([]*client, 0, len(h.clients))
	for c := range h.clients {
		all = append(all, c)
	}
	h.mu.Unlock()
	for _, c := range all {
		h.dropClient(c, websocket.StatusGoingAway, "server stopping")
	}
}

func (h *hub) wait() { h.wg.Wait() }

// validTopic accepts "tasks" and "task:<id>".
func validTopic(topic string) bool {
	if topic == TopicTasks {
		return true
	}
	id, ok := strings.CutPrefix(topic, TopicTaskPrefix)
	return ok && core.TaskID(id).Validate() == nil
}

// serveWS upgrades the request and serves one client until it disconnects or
// the server stops. Host and Origin were checked by the guard middleware
// before this runs, so the library's own origin check is switched off.
func (s *Server) serveWS(w http.ResponseWriter, r *http.Request) {
	h := s.hub
	h.mu.Lock()
	closed := h.closed
	h.mu.Unlock()
	if closed {
		writeError(w, http.StatusServiceUnavailable, ErrUnavailable, "Yggdrasil is shutting down.", "Reload the page in a moment.")
		return
	}
	conn, err := websocket.Accept(w, r, &websocket.AcceptOptions{InsecureSkipVerify: true}) //nolint:gosec // origin already checked by guard
	if err != nil {
		return // Accept has written the response
	}
	conn.SetReadLimit(maxWSMessage)
	c := &client{conn: conn, send: make(chan []byte, clientBuffer), topics: map[string]struct{}{}}

	h.mu.Lock()
	if h.closed {
		h.mu.Unlock()
		_ = conn.Close(websocket.StatusGoingAway, "server stopping")
		return
	}
	h.clients[c] = struct{}{}
	h.wg.Add(1)
	h.mu.Unlock()
	defer h.wg.Done()

	ctx, cancel := context.WithCancel(r.Context())
	defer cancel()
	defer h.dropClient(c, websocket.StatusNormalClosure, "bye")

	h.wg.Add(1)
	go func() { // writer: owned by this handler, ends with ctx
		defer h.wg.Done()
		defer cancel()
		t := time.NewTicker(pingEvery)
		defer t.Stop()
		for {
			select {
			case <-ctx.Done():
				return
			case msg := <-c.send:
				wctx, wcancel := context.WithTimeout(ctx, writeTimeout)
				err := conn.Write(wctx, websocket.MessageText, msg)
				wcancel()
				if err != nil {
					return
				}
			case <-t.C:
				pctx, pcancel := context.WithTimeout(ctx, writeTimeout)
				err := conn.Ping(pctx)
				pcancel()
				if err != nil {
					return
				}
			}
		}
	}()

	for { // reader
		_, data, err := conn.Read(ctx)
		if err != nil {
			return
		}
		var msg WSMessage
		if err := json.Unmarshal(data, &msg); err != nil {
			h.reply(c, WSMessage{Type: MsgError, Data: errData("That message isn't valid JSON.")})
			continue
		}
		switch msg.Type {
		case MsgSubscribe, MsgUnsubscribe:
			if !validTopic(msg.Topic) {
				h.reply(c, WSMessage{Type: MsgError, Topic: msg.Topic, Data: errData("Unknown topic.")})
				continue
			}
			h.mu.Lock()
			if msg.Type == MsgSubscribe {
				c.topics[msg.Topic] = struct{}{}
			} else {
				delete(c.topics, msg.Topic)
			}
			h.mu.Unlock()
		default:
			h.reply(c, WSMessage{Type: MsgError, Data: errData("Unknown message type.")})
		}
	}
}

// reply queues a message for one client without blocking.
func (h *hub) reply(c *client, m WSMessage) {
	raw, err := json.Marshal(m)
	if err != nil {
		return
	}
	select {
	case c.send <- raw:
	default:
		h.dropClient(c, websocket.StatusPolicyViolation, "too slow")
	}
}

func errData(message string) json.RawMessage {
	b, _ := json.Marshal(ErrorBody{Code: ErrBadRequest, Message: message})
	return b
}
