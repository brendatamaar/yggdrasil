package devservers_test

import (
	"testing"

	"github.com/brendatamaar/yggdrasil/internal/devservers"
)

// A restart of Yggdrasil restarts Seq at 1; the epoch is what tells the two
// runs apart, on the tail and on the live publish alike.
func TestLogLinesCarryTheSinkEpoch(t *testing.T) {
	var pub []devservers.LogLine
	s, err := devservers.NewMemorySink(func(s string) string { return s }, 5, func(l devservers.LogLine) { pub = append(pub, l) }, devservers.WithEpoch(42))
	if err != nil {
		t.Fatal(err)
	}
	s.Write(devservers.StreamStdout, "a")
	tail := s.Tail(-1)
	if len(tail) != 1 || tail[0].Epoch != 42 || tail[0].Seq != 1 {
		t.Fatalf("tail = %+v", tail)
	}
	if len(pub) != 1 || pub[0].Epoch != 42 {
		t.Fatalf("published = %+v", pub)
	}
}
