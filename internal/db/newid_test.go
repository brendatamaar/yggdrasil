package db

import (
	"strings"
	"testing"
)

// IDs must stay unique however fast they are generated: they do not read the
// clock, so a coarse clock (about 15 ms on Windows) cannot make two collide.
func TestNewIDIsUniqueWhenGeneratedInABurst(t *testing.T) {
	seen := make(map[string]struct{}, 200000)
	for i := 0; i < 200000; i++ {
		id := NewID("aud")
		if !strings.HasPrefix(id, "aud_") {
			t.Fatalf("id %q has no prefix", id)
		}
		if _, dup := seen[id]; dup {
			t.Fatalf("duplicate id %q after %d ids", id, i)
		}
		seen[id] = struct{}{}
	}
}
