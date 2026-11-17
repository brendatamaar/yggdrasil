// Package standup is the imperative shell of the standup summary (AU-02, UI-03,
// docs/design/phase-6-standup.md). Service validates the range at the boundary,
// reads commits, cached merge requests and builds through bounded sources
// (errgroup, a context, at most 20 s), turns a source error into a labelled
// Unavailable entry, runs core/standup.Step and returns the deterministic
// summary. A refresh never starts an agent, never calls the network and never
// calls the Polisher; Polish is the only path to it, on an explicit request
// with a request ID, through the ordinary action path (Ask mode). Only the
// tracked, ready repos the ScopeReader returns are ever read, and the scope is
// checked again after the read.
package standup
