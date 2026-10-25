package search

import (
	"strings"
	"testing"
)

func TestSinkStopsOnTotalBytesOfUnparseableLines(t *testing.T) {
	stopped := 0
	s := &lineSink{limit: 100, stop: func() { stopped++ }}
	chunk := []byte(strings.Repeat("not a grep line\n", 4096)) // 64 KiB, none parse
	for i := 0; i < 200 && !s.full; i++ {
		_, _ = s.Write(chunk)
	}
	if !s.full || stopped != 1 {
		t.Fatalf("full=%v stopped=%d, want the sink to stop the search once", s.full, stopped)
	}
	if s.total > maxTotalBytes+len(chunk) {
		t.Fatalf("read %d bytes, cap is %d", s.total, maxTotalBytes)
	}
	_, _ = s.Write(chunk)
	if stopped != 1 {
		t.Fatalf("stop called %d times", stopped)
	}
}
