package api

import (
	"strings"
	"testing"

	coretasks "github.com/brendatamaar/yggdrasil/internal/core/tasks"
)

// A client can never send an ID from Yggdrasil's reserved namespace (the
// proposal anchors), on any request that carries a request ID.
func TestParseRequestIDRejectsReservedPrefix(t *testing.T) {
	for _, v := range []string{string(coretasks.AnchorRequest("rep_1")), "anchor-", "ANCHOR-rep_1"} {
		if _, err := parseRequestID(v); err == nil {
			t.Errorf("reserved id %q accepted", v)
		}
	}
	for _, v := range []string{"req_1", "anchor", "anchor_rep", "my-anchor-1"} {
		if id, err := parseRequestID(v); err != nil || string(id) != v {
			t.Errorf("ordinary id %q: %v", v, err)
		}
	}
	if _, err := parseRequestID(strings.Repeat("a", maxRequestIDLen+1)); err == nil {
		t.Error("overlong id accepted")
	}
}
