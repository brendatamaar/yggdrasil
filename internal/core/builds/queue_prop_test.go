package builds

import (
	"fmt"
	"math/rand"
	"testing"

	"github.com/brendatamaar/yggdrasil/internal/core"
)

// Q1
func TestPropNextFIFO(t *testing.T) {
	forSeeds(t, func(t *testing.T, r *rand.Rand) {
		n := r.Intn(8)
		q := make([]QueuedRef, n)
		for i := range q {
			q[i] = QueuedRef{ID: core.BuildID(fmt.Sprintf("b%d", i)), QueuedAt: epoch.Add(timeSec(r.Intn(4)))}
		}
		running, max := r.Intn(3), r.Intn(3)
		id, ok := Next(q, running, max)
		if running >= max || n == 0 {
			if ok {
				t.Fatalf("admitted with running=%d max=%d queued=%d", running, max, n)
			}
			return
		}
		if !ok {
			t.Fatalf("did not admit: running=%d max=%d queued=%v", running, max, q)
		}
		var pick *QueuedRef
		for i := range q {
			if q[i].ID == id {
				pick = &q[i]
			}
		}
		if pick == nil {
			t.Fatalf("%q is not in queued", id)
		}
		for _, x := range q {
			if x.QueuedAt.Before(pick.QueuedAt) || (x.QueuedAt.Equal(pick.QueuedAt) && x.ID < pick.ID) {
				t.Fatalf("picked %v but %v is older", *pick, x)
			}
		}
	})
}
