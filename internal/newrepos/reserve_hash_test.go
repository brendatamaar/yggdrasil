package newrepos

import (
	"errors"
	"strings"
	"testing"
)

// Invariant 2: a reservation is made only for the spec the user previewed.
func TestReserveWithAnotherHashIsRefused(t *testing.T) {
	w := newWorld(t)
	pv, err := w.svc.PreviewReserve(w.ctx, w.project.ID, w.reserveReq("shop-new"))
	if err != nil {
		t.Fatal(err)
	}
	if _, err := w.svc.Reserve(w.ctx, w.project.ID, w.reserveReq("shop-new"), strings.Repeat("0", 64)); !errors.Is(err, ErrHashMismatch) {
		t.Fatalf("err = %v, want ErrHashMismatch", err)
	}
	if recs, _ := w.svc.List(w.ctx, w.project.ID); len(recs) != 0 {
		t.Fatalf("a reservation was stored for a hash nobody previewed: %d", len(recs))
	}
	if _, err := w.svc.Reserve(w.ctx, w.project.ID, w.reserveReq("shop-new"), pv.Hash); err != nil {
		t.Fatalf("the previewed hash must work: %v", err)
	}
}
