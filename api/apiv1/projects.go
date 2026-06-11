package v1

// CreateProjectRequest is the wire shape for POST /api/projects.
//
// DefaultRepo + GitHub are optional on the server side; the CLI's
// earlier version inlined these as anonymous structs which made
// referencing them in tests awkward — they're named here.
type CreateProjectRequest struct {
	Name        string                 `json:"name"`
	Description string                 `json:"description,omitempty"`
	BaseDomain  string                 `json:"baseDomain,omitempty"`
	Namespace   string                 `json:"namespace,omitempty"`
	DefaultRepo *RepoRef               `json:"defaultRepo,omitempty"`
	GitHub      *GitHubInstallationRef `json:"github,omitempty"`
	Previews    *PreviewsSettings      `json:"previews,omitempty"`
}

// UpdateProjectRequest is PATCH /api/projects/{name}.
//
// Pointer fields = optional; absent = "leave alone", zero-value
// pointer = "set to zero". The server handles the omitempty
// semantics on its side via reflection in the patch decoder.
type UpdateProjectRequest struct {
	Description *string                `json:"description,omitempty"`
	BaseDomain  *string                `json:"baseDomain,omitempty"`
	DefaultRepo *RepoRef               `json:"defaultRepo,omitempty"`
	GitHub      *GitHubInstallationRef `json:"github,omitempty"`
	Previews    *PreviewsPatch         `json:"previews,omitempty"`
	// AlwaysOn=true overrides every per-service sleep config so all
	// services in this project run with scale-to-zero disabled.
	AlwaysOn *bool `json:"alwaysOn,omitempty"`
	// IncidentMonitoring=true opts the project into the incident-
	// response agent. Omitted = leave unchanged.
	IncidentMonitoring *bool `json:"incidentMonitoring,omitempty"`
}

// RepoRef pairs a repo URL with optional branch + path. Used by
// both project-level (defaultRepo) and service-level (repo) specs.
type RepoRef struct {
	URL           string `json:"url"`
	DefaultBranch string `json:"defaultBranch,omitempty"`
	Path          string `json:"path,omitempty"`
}

// GitHubInstallationRef carries the installation id for projects /
// services that authenticate clones via a GitHub App installation
// token.
type GitHubInstallationRef struct {
	InstallationID int64 `json:"installationId,omitempty"`
}

// PreviewsSettings is the create-time shape.
type PreviewsSettings struct {
	Enabled    bool   `json:"enabled"`
	TTLDays    int    `json:"ttlDays,omitempty"`
	BaseDomain string `json:"baseDomain,omitempty"`
}

// PreviewsPatch is the update-time shape — pointer fields so callers
// can flip Enabled without resetting TTLDays.
type PreviewsPatch struct {
	Enabled    *bool   `json:"enabled,omitempty"`
	TTLDays    *int    `json:"ttlDays,omitempty"`
	BaseDomain *string `json:"baseDomain,omitempty"`
}

// Helpers for building pointer literals tersely. Go 1.21+ has
// generic versions in the stdlib (cmp.Or, etc.) but these stay
// here because every caller already used the legacy names.
func BoolPtr(b bool) *bool       { return &b }
func IntPtr(i int) *int          { return &i }
func StringPtr(s string) *string { return &s }
