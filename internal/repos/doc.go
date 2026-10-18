// Package repos is the shell of repo tracking (WS-01) and repo status
// (WS-02 to WS-04). The decisions are pure functions in internal/core/repos;
// this package parses outside data into core types (parse.go), reads the
// disk (scan.go, browse.go), persists a decision and its audit entries in
// one transaction and runs what is left (service.go).
//
// Tracking never creates, changes or removes a file or folder under a
// tracked path. Untracking is a flag on the database row.
//
// Wiring (cmd/ygg/app.go), for the lead:
//
//	tracking, err := repos.NewTrackingService(repos.Deps{
//		Store:    store,          // *db.Store
//		Redactor: redactor,       // safety.Redactor, for audit entries
//		Status:   statusService,  // StatusRefresher; nil is fine
//		Log:      log,            // optional
//	})
//	api.Deps{Tracking: tracking, ...}
//
// Scanner, Dirs, Clock, IDs and Limits default to the live implementations.
package repos
