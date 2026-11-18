package hosts

import (
	"math/rand"
	"reflect"
	"testing"
	"time"
)

// Previous gives the data the key last held in every phase: Idle is its own
// previous data, the other phases keep theirs in Prev, and a missing phase has
// none. It never invents data and never loses any.
func TestPreviousIsTheLastDataOfEveryPhase(t *testing.T) {
	r := rand.New(rand.NewSource(3))
	for i := 0; i < 200; i++ {
		prev := Idle{FetchedAt: time.Unix(int64(r.Intn(1e6)), 0).UTC(), ETag: string(rune('a' + r.Intn(26)))}
		switch r.Intn(3) {
		case 0:
			prev.Data = RepoState{MR: &MergeRequest{Number: r.Intn(100), Head: "abc"}}
		case 1:
			prev.Data = Inbox{Items: []InboxItem{{}}}
		}
		phases := []Phase{
			prev,
			Fetching{Attempt: r.Intn(5), Prev: prev},
			Waiting{Until: time.Unix(5, 0), Failures: r.Intn(5), Prev: prev, Why: WaitTransient},
			Blocked{Why: BlockUnauthorized, Since: time.Unix(7, 0), Prev: prev},
		}
		for _, ph := range phases {
			if got := Previous(Entry{Attempt: r.Intn(9), Failures: r.Intn(9), Phase: ph}); !reflect.DeepEqual(got, prev) {
				t.Fatalf("Previous(%T) = %+v, want %+v", ph, got, prev)
			}
		}
	}
	if got := Previous(Entry{}); !reflect.DeepEqual(got, Idle{}) {
		t.Fatalf("Previous of an entry with no phase = %+v, want the empty Idle", got)
	}
}
