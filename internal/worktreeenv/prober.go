package worktreeenv

import (
	"context"
	"net"
	"strconv"
)

// LoopbackProber finds busy ports by trying to listen on them on the loopback
// address. A port that cannot be bound is busy. The listeners are closed at
// once; nothing is served.
type LoopbackProber struct{}

// Busy implements PortProber.
func (LoopbackProber) Busy(ctx context.Context, lo, hi int) ([]int, error) {
	var busy []int
	var lc net.ListenConfig
	for p := lo; p <= hi; p++ {
		if err := ctx.Err(); err != nil {
			return nil, err
		}
		l, err := lc.Listen(ctx, "tcp", net.JoinHostPort("127.0.0.1", strconv.Itoa(p)))
		if err != nil {
			busy = append(busy, p)
			continue
		}
		_ = l.Close()
	}
	return busy, nil
}
