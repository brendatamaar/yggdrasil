package api

import (
	"fmt"
	"strings"

	"github.com/brendatamaar/yggdrasil/internal/core"
)

// The types here are unexported and use trailing comments only, so tygo (which
// reads the whole package) emits nothing for them: topics never cross the wire
// as types, only as the strings String returns.
//
// wsTopic is a WebSocket subscription topic, a closed set (tasksTopic,
// taskTopic). Outside data becomes a wsTopic only through parseTopic, so a
// value is always valid.

type wsTopic interface {
	String() string
	isTopic()
}

type tasksTopic struct{} // a status message for every task

type taskTopic struct{ ID core.TaskID } // the events of one task

func (tasksTopic) isTopic() {}
func (taskTopic) isTopic()  {}

func (tasksTopic) String() string { return TopicTasks }
func (t taskTopic) String() string {
	return TopicTaskPrefix + string(t.ID)
}

// parseTopic accepts "tasks" and "task:<id>" with a valid task ID.
func parseTopic(s string) (wsTopic, error) {
	if s == TopicTasks {
		return tasksTopic{}, nil
	}
	if id, ok := strings.CutPrefix(s, TopicTaskPrefix); ok {
		tid := core.TaskID(id)
		if err := tid.Validate(); err != nil {
			return nil, fmt.Errorf("parse topic %q: %w", s, err)
		}
		return taskTopic{ID: tid}, nil
	}
	return nil, fmt.Errorf("parse topic %q: unknown topic", s)
}
