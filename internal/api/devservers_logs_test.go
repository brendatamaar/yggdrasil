package api

import (
	"encoding/json"
	"slices"
	"testing"
	"time"

	"github.com/brendatamaar/yggdrasil/internal/devservers"
)

// The client rule for logs: fetch the tail, subscribe, put both into one set
// keyed by seq (a later copy replaces an earlier one), sort by seq. With one
// source of numbers (the service's log), the merged list has every line once
// and in order, however the live messages overlap the tail.
func TestDevServerLogTailAndLiveLinesMergeBySeq(t *testing.T) {
	tail := []devservers.LogLine{
		{Seq: 5, Stream: devservers.StreamStdout, Line: "five"},
		{Seq: 6, Stream: devservers.StreamStderr, Line: "six"},
		{Seq: 7, Stream: devservers.StreamStdout, Line: "seven"},
	}
	f := &fakeDev{logs: tail}
	v := f.view(devservers.Target{Repo: "repo_a"}, "dev", "running")
	f.views = []devservers.View{v}
	e := devEnv(t, f)
	c := e.dial(t)
	subscribe(t, c, TopicDevServerPrefix+v.ID)
	time.Sleep(150 * time.Millisecond) // subscriptions have no ack

	// Live lines 7 (also in the tail) to 10, published by the service's log.
	for _, l := range []devservers.LogLine{
		{Seq: 7, Stream: devservers.StreamStdout, Line: "seven"},
		{Seq: 8, Stream: devservers.StreamStdout, Line: "eight"},
		{Seq: 9, Stream: devservers.StreamStderr, Line: "nine"},
		{Seq: 10, Stream: devservers.StreamStdout, Line: "ten"},
	} {
		e.srv.PublishLog(v.ID, l)
	}
	merged := map[uint64]DevServerLogMessage{}
	resp, data := e.do(t, "GET", "/api/devservers/"+v.ID+"/log?tail=50", nil)
	var got DevServerLogResponse
	if resp.StatusCode != 200 || json.Unmarshal(data, &got) != nil {
		t.Fatalf("tail = %d %s", resp.StatusCode, data)
	}
	for _, l := range got.Lines {
		merged[l.Seq] = l
	}
	for i := 0; i < 4; i++ {
		m, err := readMsg(t, c, 3*time.Second)
		if err != nil {
			t.Fatal(err)
		}
		var l DevServerLogMessage
		if json.Unmarshal(m.Data, &l) != nil {
			t.Fatalf("message %s", m.Data)
		}
		merged[l.Seq] = l
	}
	seqs := make([]uint64, 0, len(merged))
	for s := range merged {
		seqs = append(seqs, s)
	}
	slices.Sort(seqs)
	if !slices.Equal(seqs, []uint64{5, 6, 7, 8, 9, 10}) {
		t.Fatalf("merged seqs = %v, want 5..10 once each", seqs)
	}
	if merged[6].Stream != "stderr" || merged[10].Line != "ten" || merged[7].Line != "seven" {
		t.Errorf("merged lines = %+v", merged)
	}
}
