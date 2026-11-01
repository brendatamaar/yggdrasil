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
// taskTopic, reposTopic, repoTopic, buildsTopic, buildTopic, hostsTopic,
// proposalsTopic, boardTopic, verificationsTopic, changeSetsTopic).
// Outside data becomes a wsTopic only through parseTopic, so a value is
// always valid.

type wsTopic interface {
	String() string
	isTopic()
}

type tasksTopic struct{} // a status message for every task

type taskTopic struct{ ID core.TaskID } // the events of one task

type reposTopic struct{} // the repo list and its counts changed

type repoTopic struct{ ID core.RepoID } // the status of one repo

type buildsTopic struct{} // a build changed (any build)

type buildTopic struct{ ID core.BuildID } // one build: changes and log lines

type hostsTopic struct{} // host instances or their MR/pipeline data changed

type proposalsTopic struct{} // a proposal was created or changed state

type boardTopic struct{} // a task moved between board columns, or maxAgents changed

type verificationsTopic struct{} // a verification of any task changed state

type changeSetsTopic struct{} // a change set or the review notes of a task changed

func (verificationsTopic) isTopic() {}
func (changeSetsTopic) isTopic()    {}

func (verificationsTopic) String() string { return TopicVerifications }
func (changeSetsTopic) String() string    { return TopicChangeSets }

func (proposalsTopic) isTopic() {}
func (boardTopic) isTopic()     {}

func (proposalsTopic) String() string { return TopicProposals }
func (boardTopic) String() string     { return TopicBoard }

func (tasksTopic) isTopic()  {}
func (taskTopic) isTopic()   {}
func (reposTopic) isTopic()  {}
func (repoTopic) isTopic()   {}
func (buildsTopic) isTopic() {}
func (buildTopic) isTopic()  {}
func (hostsTopic) isTopic()  {}

func (tasksTopic) String() string  { return TopicTasks }
func (reposTopic) String() string  { return TopicRepos }
func (buildsTopic) String() string { return TopicBuilds }
func (hostsTopic) String() string  { return TopicHosts }
func (t taskTopic) String() string { return TopicTaskPrefix + string(t.ID) }
func (t repoTopic) String() string { return TopicRepoPrefix + string(t.ID) }
func (t buildTopic) String() string {
	return TopicBuildPrefix + string(t.ID)
}

// parseTopic accepts the fixed names "tasks", "repos", "builds", "hosts",
// "proposals", "board", "verifications" and "changesets",
// and "task:<id>", "repo:<id>" and "build:<id>" with a valid ID.
func parseTopic(s string) (wsTopic, error) {
	switch s {
	case TopicTasks:
		return tasksTopic{}, nil
	case TopicRepos:
		return reposTopic{}, nil
	case TopicBuilds:
		return buildsTopic{}, nil
	case TopicHosts:
		return hostsTopic{}, nil
	case TopicProposals:
		return proposalsTopic{}, nil
	case TopicVerifications:
		return verificationsTopic{}, nil
	case TopicChangeSets:
		return changeSetsTopic{}, nil
	case TopicBoard:
		return boardTopic{}, nil
	}
	if id, ok := strings.CutPrefix(s, TopicTaskPrefix); ok {
		tid := core.TaskID(id)
		if err := tid.Validate(); err != nil {
			return nil, fmt.Errorf("parse topic %q: %w", s, err)
		}
		return taskTopic{ID: tid}, nil
	}
	if id, ok := strings.CutPrefix(s, TopicRepoPrefix); ok {
		rid := core.RepoID(id)
		if err := rid.Validate(); err != nil {
			return nil, fmt.Errorf("parse topic %q: %w", s, err)
		}
		return repoTopic{ID: rid}, nil
	}
	if id, ok := strings.CutPrefix(s, TopicBuildPrefix); ok {
		bid := core.BuildID(id)
		if err := bid.Validate(); err != nil {
			return nil, fmt.Errorf("parse topic %q: %w", s, err)
		}
		return buildTopic{ID: bid}, nil
	}
	return nil, fmt.Errorf("parse topic %q: unknown topic", s)
}
