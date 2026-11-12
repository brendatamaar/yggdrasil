// Package standup is the imperative shell of the standup summary (AU-02, UI-03,
// docs/design/phase-6-standup.md). It will validate the range, read commits,
// cached merge requests and builds through bounded sources, run
// core/standup.Step and Format, and return a deterministic summary with labelled
// gaps. A refresh never starts an agent; polishing is an explicit submission
// through the ordinary action path. This foundation declares only the ports
// (ports.go); the standup task (S) implements the service.
package standup
