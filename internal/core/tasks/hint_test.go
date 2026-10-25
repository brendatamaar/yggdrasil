package tasks

import (
	"math/rand"
	"testing"
)

// K15: RoomHint never suggests above the ceiling and never when a slot is free.
func TestPropRoomHintBounds(t *testing.T) {
	forSeeds(t, func(t *testing.T, r *rand.Rand) {
		cpus, mem, mx := r.Intn(80), r.Intn(100000), r.Intn(20)-1
		active := r.Intn(20)
		h := RoomHint(cpus, mem, mx, active)
		if h.RaiseTo > RoomHintCeiling {
			t.Fatalf("suggested %d", h.RaiseTo)
		}
		if h.RaiseTo != 0 && active < mx {
			t.Fatalf("suggested with a free slot: %d of %d", active, mx)
		}
		if h.RaiseTo != 0 && h.RaiseTo != mx+1 {
			t.Fatalf("suggested %d from %d", h.RaiseTo, mx)
		}
		if h.RaiseTo != 0 && (cpus < 4*(mx+1) || mem < 2048*(mx+1)) {
			t.Fatalf("suggested without room: cpus %d mem %d max %d", cpus, mem, mx)
		}
	})
}
