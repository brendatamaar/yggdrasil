package api

// Go-only wiring (excluded from tygo): the Phase 5 services, which are bound
// after the server exists because they publish to its hub (PublishServer,
// PublishLog, PublishWorktreeEnv).
//
//	srv, _ := api.New(deps)
//	devSvc, _ := devservers.New(ctx, devservers.Deps{Hub: srv, ...})
//	srv.SetPhase5Services(api.Phase5Services{DevServers: adapter(devSvc), ...})
//
// A nil field answers 501 for its endpoints.

// Phase5Services are the services of the Phase 5 areas.
type Phase5Services struct {
	DevServers  DevServerService
	WorktreeEnv WorktreeEnvService
	Terminal    TerminalService
}

// SetPhase5Services binds (or replaces) the Phase 5 services. Safe to call
// while the server runs.
func (s *Server) SetPhase5Services(p Phase5Services) { s.p5.Store(&p) }

func (s *Server) phase5() *Phase5Services {
	if p := s.p5.Load(); p != nil {
		return p
	}
	return &Phase5Services{}
}
