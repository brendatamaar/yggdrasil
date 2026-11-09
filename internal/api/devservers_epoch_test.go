package api

import (
	"encoding/json"
	"testing"
	"time"

	"github.com/brendatamaar/yggdrasil/internal/devservers"
)

// The tail and the live messages carry the log's epoch, so a client can tell
// the lines of a run before a restart from those after it.
func TestDevServerLogMessagesCarryTheEpoch(t *testing.T) {
	f := &fakeDev{logs: []devservers.LogLine{{Epoch: 1700000000000, Seq: 1, Stream: devservers.StreamStdout, Line: "a"}}}
	v := f.view(devservers.Target{Repo: "repo_a"}, "dev", "running")
	f.views = []devservers.View{v}
	e := devEnv(t, f)
	resp, data := e.do(t, "GET", "/api/devservers/"+v.ID+"/log?tail=5", nil)
	var got DevServerLogResponse
	if resp.StatusCode != 200 || json.Unmarshal(data, &got) != nil || len(got.Lines) != 1 || got.Lines[0].Epoch != 1700000000000 {
		t.Fatalf("tail = %d %s", resp.StatusCode, data)
	}
	c := e.dial(t)
	subscribe(t, c, TopicDevServerPrefix+v.ID)
	time.Sleep(150 * time.Millisecond)
	e.srv.PublishLog(v.ID, devservers.LogLine{Epoch: 1700000009999, Seq: 1, Stream: devservers.StreamStdout, Line: "b"})
	m, err := readMsg(t, c, 3*time.Second)
	if err != nil {
		t.Fatal(err)
	}
	var l DevServerLogMessage
	if json.Unmarshal(m.Data, &l) != nil || l.Epoch != 1700000009999 {
		t.Fatalf("live = %s", m.Data)
	}
}
