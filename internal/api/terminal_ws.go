package api

import (
	"context"
	"sync"
	"unicode/utf8"

	"github.com/coder/websocket"

	ct "github.com/brendatamaar/yggdrasil/internal/core/terminal"
	"github.com/brendatamaar/yggdrasil/internal/terminal"
)

// maxCloseReason is the most a WebSocket close frame can carry (125 bytes of
// payload, 2 of them the status code).
const maxCloseReason = 123

// wsSocket is terminal.Socket over a WebSocket: binary messages are
// keystrokes and output, text messages are control frames (ct.ParseFrame
// decides what they mean). The terminal service owns the frame rules; this
// adapter only maps message types.
type wsSocket struct {
	conn *websocket.Conn

	once sync.Once
}

var _ terminal.Socket = (*wsSocket)(nil)

func newWSSocket(conn *websocket.Conn) *wsSocket {
	conn.SetReadLimit(ct.MaxFrameBytes)
	return &wsSocket{conn: conn}
}

// Read implements terminal.Socket.
func (s *wsSocket) Read(ctx context.Context) (ct.FrameKind, []byte, error) {
	typ, data, err := s.conn.Read(ctx)
	if err != nil {
		return 0, nil, err
	}
	if typ == websocket.MessageText {
		return ct.TextFrame, data, nil
	}
	return ct.BinaryFrame, data, nil
}

// Write implements terminal.Socket.
func (s *wsSocket) Write(ctx context.Context, data []byte) error {
	return s.conn.Write(ctx, websocket.MessageBinary, data)
}

// Close implements terminal.Socket: the first call closes with its reason,
// later calls do nothing.
func (s *wsSocket) Close(reason string) error {
	var err error
	s.once.Do(func() {
		err = s.conn.Close(websocket.StatusNormalClosure, truncateUTF8(reason, maxCloseReason))
	})
	return err
}

func truncateUTF8(s string, n int) string {
	if len(s) <= n {
		return s
	}
	s = s[:n]
	for len(s) > 0 && !utf8.ValidString(s) {
		s = s[:len(s)-1]
	}
	return s
}
