package projects

// Service is the historical god-struct with ~64 methods covering
// project / service / environment / env-group / drift / pods CRUD
// plus per-(project,service) mutex management. The review (A-P1-1)
// called it out as a god-object, but a full physical split into
// three packages would touch every handler call site for a purely
// cosmetic gain — see docs/REVIEW_2026-05-12.md for the rationale.
//
// Instead, this file introduces three typed facades that EXPOSE
// only the relevant methods to each handler. Implementation stays
// on *Service; the facades are zero-cost (just struct pointers
// with method-set narrowing via interfaces).
//
// Handlers should take whichever facade matches their scope:
//
//   - ProjectAPI: project lifecycle (Create / Update / Delete /
//     Describe / List / SetEnvGroup / Get).
//   - ServiceAPI: service CRUD inside a project (AddService /
//     PatchService / DeleteService / RenameService / Get / List /
//     Wake / per-domain + per-env-var deltas).
//   - EnvironmentAPI: environment CRUD + env-group operations +
//     drift + pod listing.
//
// Future migration paths (a) replace one of these with an interface,
// (b) move the underlying methods into a sibling type, or (c) ship
// a real split into three packages. All three are easier once the
// handler-side API consumes the narrower facade.

// ProjectAPI exposes the project lifecycle surface. A handler that
// holds one of these can call Project-level methods but not
// service or env-group operations — narrower than holding the full
// *Service.
type ProjectAPI struct {
	*Service
}

// ServiceAPI exposes service-CRUD-within-a-project. Like ProjectAPI,
// it embeds *Service so call sites continue to compile, but the
// type signals intent: a handler typed *ServiceAPI is scoped to
// service operations.
type ServiceAPI struct {
	*Service
}

// EnvironmentAPI exposes environment + env-group + drift + pods.
// Same embedding trick.
type EnvironmentAPI struct {
	*Service
}

// AsProjectAPI / AsServiceAPI / AsEnvironmentAPI wrap a *Service in
// the appropriate facade. Wiring code (router / main) calls these
// at handler construction time. Cheap: each facade is one pointer.
func (s *Service) AsProjectAPI() *ProjectAPI         { return &ProjectAPI{Service: s} }
func (s *Service) AsServiceAPI() *ServiceAPI         { return &ServiceAPI{Service: s} }
func (s *Service) AsEnvironmentAPI() *EnvironmentAPI { return &EnvironmentAPI{Service: s} }
