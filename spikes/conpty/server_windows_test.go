//go:build windows

package main

import (
	"context"
	"encoding/json"
	"errors"
	"regexp"
	"strings"
	"testing"
	"time"

	"github.com/coder/websocket"
)

// wsClient is a test client for the /ws protocol.
type wsClient struct {
	t      *testing.T
	c      *websocket.Conn
	ctx    context.Context
	screen strings.Builder
	pid    int
}

func dialShell(t *testing.T, shell string) *wsClient {
	t.Helper()
	srv := newTestServer(t, startGoPTY)
	ctx, cancel := context.WithTimeout(context.Background(), 30*time.Second)
	t.Cleanup(cancel)
	c, _, err := websocket.Dial(ctx, wsURL(srv, "shell="+shell+"&cols=90&rows=30"), nil)
	if err != nil {
		t.Fatalf("dial: %v", err)
	}
	t.Cleanup(func() { _ = c.CloseNow() })
	w := &wsClient{t: t, c: c, ctx: ctx}
	m := w.nextControl()
	if m.Type != "started" || m.Pid == 0 {
		t.Fatalf("first message = %+v, want started with a pid", m)
	}
	w.pid = m.Pid
	t.Cleanup(func() {
		if !processGone(w.pid, 5*time.Second) {
			killPid(w.pid)
			t.Errorf("shell %d still running at the end of the test", w.pid)
		}
	})
	return w
}

func (w *wsClient) send(s string) {
	w.t.Helper()
	if err := w.c.Write(w.ctx, websocket.MessageBinary, []byte(s)); err != nil {
		w.t.Fatalf("write: %v", err)
	}
}

// next reads one frame; data frames are added to the screen.
func (w *wsClient) next() (message, bool, error) {
	typ, data, err := w.c.Read(w.ctx)
	if err != nil {
		return message{}, false, err
	}
	if typ == websocket.MessageBinary {
		w.screen.Write(data)
		return message{}, false, nil
	}
	var m message
	if err := json.Unmarshal(data, &m); err != nil {
		w.t.Fatalf("bad control message %q: %v", data, err)
	}
	return m, true, nil
}

func (w *wsClient) nextControl() message {
	w.t.Helper()
	for {
		m, ok, err := w.next()
		if err != nil {
			w.t.Fatalf("read: %v; screen:\n%s", err, screenText(w.screen.String()))
		}
		if ok {
			return m
		}
	}
}

func (w *wsClient) waitFor(pattern string) {
	w.t.Helper()
	re := regexp.MustCompile(pattern)
	for !re.MatchString(screenText(w.screen.String())) {
		if m, ok, err := w.next(); err != nil {
			w.t.Fatalf("read: %v; screen:\n%s", err, screenText(w.screen.String()))
		} else if ok {
			w.t.Fatalf("unexpected control message %+v", m)
		}
	}
}

// Closing the socket kills the PTY's process.
func TestWebSocketRoundTrip(t *testing.T) {
	w := dialShell(t, "cmd")
	w.send("echo ygg-ws-ok\r")
	w.waitFor(`(?m)^ygg-ws-ok`)
	if err := w.c.Write(w.ctx, websocket.MessageText, []byte(`{"type":"resize","cols":100,"rows":40}`)); err != nil {
		t.Fatal(err)
	}
	w.send("mode con\r")
	w.waitFor(`Columns:\s+100\b`)
	_ = w.c.Close(websocket.StatusNormalClosure, "tab closed")
	if !processGone(w.pid, 5*time.Second) {
		t.Fatalf("shell %d still running after the socket closed", w.pid)
	}
}

// The process exiting closes the socket and reports the exit code.
func TestWebSocketExitCode(t *testing.T) {
	w := dialShell(t, "powershell")
	w.send("exit 3\r")
	m := w.nextControl()
	if m.Type != "exit" || m.Code == nil || *m.Code != 3 {
		t.Fatalf("got %+v, want exit with code 3", m)
	}
	_, _, err := w.next()
	var ce websocket.CloseError
	if !errors.As(err, &ce) || ce.Code != websocket.StatusNormalClosure || ce.Reason != "exit code 3" {
		t.Fatalf("read after exit: %v, want normal close with reason \"exit code 3\"", err)
	}
}
