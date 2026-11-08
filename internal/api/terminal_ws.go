package api

import (
	"context"
	"net"
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

	frames chan wsFrame
	done   chan struct{} // closed by Close
	pump   sync.Once

	once sync.Once
}

type wsFrame struct {
	kind ct.FrameKind
	data []byte
	err  error
}

var _ terminal.Socket = (*wsSocket)(nil)

func newWSSocket(conn *websocket.Conn) *wsSocket {
	conn.SetReadLimit(ct.MaxFrameBytes)
	return &wsSocket{conn: conn, frames: make(chan wsFrame), done: make(chan struct{})}
}

// readPump reads the connection until it fails or the socket is closed. It
// reads with a context that is never cancelled: the library drops the
// connection without a close frame when a read's context ends, and the
// session must be able to send its plain-language close reason. Read returns
// when its own context ends, and the pump stops when the connection does
// (Close ends it).
func (s *wsSocket) readPump() {
	for {
		typ, data, err := s.conn.Read(context.Background())
		f := wsFrame{kind: ct.BinaryFrame, data: data, err: err}
		if typ == websocket.MessageText {
			f.kind = ct.TextFrame
		}
		select {
		case s.frames <- f:
		case <-s.done:
			return
		}
		if err != nil {
			return
		}
	}
}

// Read implements terminal.Socket.
func (s *wsSocket) Read(ctx context.Context) (ct.FrameKind, []byte, error) {
	if err := ctx.Err(); err != nil {
		return 0, nil, err
	}
	s.pump.Do(func() { go s.readPump() }) // ends with the connection; see readPump
	select {
	case f := <-s.frames:
		return f.kind, f.data, f.err
	case <-s.done:
		return 0, nil, net.ErrClosed
	case <-ctx.Done():
		return 0, nil, ctx.Err()
	}
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
		close(s.done)
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
