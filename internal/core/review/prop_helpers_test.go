package review

import (
	"fmt"
	"math/rand"
	"reflect"
	"strings"
	"testing"
	"time"
)

const (
	propRuns  = 300
	propSteps = 50
)

var epoch = time.Date(2026, 1, 1, 0, 0, 0, 0, time.UTC)

func hash(c string) string { return strings.Repeat(c, 64) }

func forSeeds(t *testing.T, f func(t *testing.T, r *rand.Rand)) {
	t.Helper()
	for seed := int64(1); seed <= propRuns; seed++ {
		if !t.Run(fmt.Sprintf("seed%d", seed), func(t *testing.T) { f(t, rand.New(rand.NewSource(seed))) }) {
			return
		}
	}
}

var paths = []RelPath{"a.go", "src/b.go", "dir with space/c.txt"}

func validNote(id string) Note {
	return Note{ID: NoteID(id), Task: "t1", Repo: "r1", Anchor: Anchor{Path: "a.go", Line: 3, Side: SideNew},
		Text: "rename this", DiffHash: hash("a"), Created: epoch, State: NoteOpen{}}
}

// ngen produces inputs for one note slot, mixing legal, stale, duplicate and
// invalid ones.
type ngen struct {
	r       *rand.Rand
	now     time.Time
	batches []BatchID
	last    Input
}

func (g *ngen) tick() time.Time {
	g.now = g.now.Add(time.Duration(g.r.Intn(200)) * time.Second)
	return g.now
}

func (g *ngen) batch() BatchID {
	if len(g.batches) > 0 && g.r.Intn(3) > 0 {
		return g.batches[g.r.Intn(len(g.batches))]
	}
	b := BatchID(fmt.Sprintf("b%d", len(g.batches)+1))
	g.batches = append(g.batches, b)
	return b
}

func (g *ngen) text(n Note) string {
	switch g.r.Intn(6) {
	case 0:
		return "" // invalid
	case 1:
		return strings.Repeat("x", MaxMessageBytes+1)
	case 2:
		return "unrelated"
	}
	return "batch text " + string(n.ID)
}

func (g *ngen) next(n Note) Input {
	if g.last != nil && g.r.Intn(8) == 0 {
		return g.last
	}
	var in Input
	switch k := g.r.Intn(14); {
	case k <= 1:
		an := validNote("n1")
		switch g.r.Intn(5) {
		case 0:
			an.Anchor.Line = 0
		case 1:
			an.Text = ""
		case 2:
			an.State = NoteSent{Turn: "x"}
		}
		in = AddNote{Note: an, OpenCount: g.r.Intn(MaxNotesPerTask + 2), Now: g.tick()}
	case k == 2:
		t := []string{"new text", "", "rename this", "a\x00b"}[g.r.Intn(4)]
		in = EditNote{Text: t, Now: g.tick()}
	case k == 3:
		in = DeleteNote{Now: g.tick()}
	case k <= 6:
		in = SendNotes{Batch: g.batch(), Text: g.text(n), Now: g.tick()}
	case k == 7 || k == 8:
		in = SendAccepted{Batch: g.batch(), Turn: TurnID([]string{"t1", "t 2"}[g.r.Intn(2)]), Now: g.tick()}
	case k == 9:
		in = SendRefused{Batch: g.batch(), Reason: "busy", Now: g.tick()}
	case k <= 12:
		h := map[RelPath]string{}
		for _, p := range paths {
			if g.r.Intn(3) > 0 {
				h[p] = []string{hash("a"), hash("b")}[g.r.Intn(2)]
			}
		}
		in = DiffObserved{Hashes: h, Now: g.tick()}
	default:
		in = Recover{Startup: g.r.Intn(3) == 0, Now: g.tick()}
	}
	g.last = in
	return in
}

type checkFn = func(t *testing.T, before Note, in Input, after Note, cmds []Command)

// run drives one note slot (starting empty) through random inputs and calls
// check after every step.
func run(t *testing.T, r *rand.Rand, check checkFn) {
	t.Helper()
	g := &ngen{r: r, now: epoch}
	n := Note{}
	for i := 0; i < propSteps; i++ {
		in := g.next(n)
		after, cmds := StepNote(n, in)
		check(t, n, in, after, cmds)
		if t.Failed() {
			t.Fatalf("step %d: %T failed the invariant (state %T -> %T)", i, in, n.State, after.State)
		}
		n = after
	}
}

func prop(t *testing.T, check checkFn) {
	t.Helper()
	forSeeds(t, func(t *testing.T, r *rand.Rand) { run(t, r, check) })
}

func changedNote(a, b Note) bool { return !reflect.DeepEqual(a, b) }

func count[T Command](cmds []Command) (n int) {
	for _, c := range cmds {
		if _, ok := c.(T); ok {
			n++
		}
	}
	return n
}
