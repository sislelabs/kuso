// Coolify v4 resource shapes. Only the fields kuso actually consumes
// are typed — the rest stay as raw json.RawMessage / interface{} so
// upstream Coolify can add fields without breaking our decode.

package coolify

import "time"

// Project is a Coolify project — the top-level grouping that holds
// environments, applications, services, databases.
type Project struct {
	ID          int    `json:"id"`
	UUID        string `json:"uuid"`
	Name        string `json:"name"`
	Description string `json:"description"`
	// Environments populates only on the per-project endpoint, not
	// on the list endpoint. We use it for project→env mapping.
	Environments []Environment `json:"environments,omitempty"`
}

type Environment struct {
	ID          int    `json:"id"`
	UUID        string `json:"uuid"`
	Name        string `json:"name"`
	ProjectID   int    `json:"project_id"`
	Description string `json:"description,omitempty"`
}

// Application covers Coolify's "Application" resource — git-backed
// builds in nixpacks / dockerfile / dockercompose modes. Many
// Coolify-specific fields elided; we only need the bits that map
// onto a kuso service.
type Application struct {
	ID            int    `json:"id"`
	UUID          string `json:"uuid"`
	Name          string `json:"name"`
	Description   string `json:"description"`
	BuildPack     string `json:"build_pack"` // nixpacks | dockerfile | dockercompose | static
	GitRepository string `json:"git_repository"`
	GitBranch     string `json:"git_branch"`
	GitFullURL    string `json:"git_full_url"`
	GitCommitSHA  string `json:"git_commit_sha,omitempty"`
	BaseDirectory string `json:"base_directory,omitempty"`
	// Comma-separated list of "https://host[,…]" — Coolify packs
	// multiple domains into one string, we split.
	FQDN string `json:"fqdn"`
	// Build/runtime knobs.
	BuildCommand        string `json:"build_command,omitempty"`
	InstallCommand      string `json:"install_command,omitempty"`
	StartCommand        string `json:"start_command,omitempty"`
	DockerfileLocation  string `json:"dockerfile_location,omitempty"`
	DockerfileTarget    string `json:"dockerfile_target_build,omitempty"`
	PortsExposes        string `json:"ports_exposes,omitempty"` // comma-separated
	PortsMappings       string `json:"ports_mappings,omitempty"`
	HealthCheckEnabled  bool   `json:"health_check_enabled,omitempty"`
	HealthCheckPath     string `json:"health_check_path,omitempty"`
	HealthCheckPort     string `json:"health_check_port,omitempty"`
	EnvironmentID       int    `json:"environment_id"`
	CreatedAt           string `json:"created_at"`
	UpdatedAt           string `json:"updated_at"`
}

// Service is Coolify's pre-baked one-click stack (a docker-compose
// under the hood). We classify these as compose and skip — the user
// asked for compose to be skipped entirely.
type Service struct {
	UUID          string `json:"uuid"`
	Name          string `json:"name"`
	ServiceType   string `json:"service_type,omitempty"`
	EnvironmentID int    `json:"environment_id"`
	Description   string `json:"description"`
}

// Database covers Coolify's standalone database resources (postgres,
// redis, mongodb, mysql, mariadb, clickhouse, dragonfly, keydb).
// The redis variant returns "standalone-redis" — we strip the prefix
// when mapping onto kuso addon kinds.
type Database struct {
	UUID            string `json:"uuid"`
	Name            string `json:"name"`
	DatabaseType    string `json:"database_type"`
	Image           string `json:"image,omitempty"`
	EnvironmentID   int    `json:"environment_id"`
	IsPublic        bool   `json:"is_public,omitempty"`
	PublicPort      int    `json:"public_port,omitempty"`
	InternalDBURL   string `json:"internal_db_url,omitempty"`
	ExternalDBURL   string `json:"external_db_url,omitempty"`
	IsLogDrain      bool   `json:"is_log_drain_enabled,omitempty"`
	CreatedAt       string `json:"created_at"`
	UpdatedAt       string `json:"updated_at"`
	// Postgres-specific (omitempty silently noop on other kinds):
	PostgresUser     string `json:"postgres_user,omitempty"`
	PostgresPassword string `json:"postgres_password,omitempty"`
	PostgresDB       string `json:"postgres_db,omitempty"`
}

// EnvVar is one application/service env var. With a write-scope
// token both `value` and `real_value` come back populated; we prefer
// real_value (it's the resolved value, after Coolify's variable-
// reference expansion).
type EnvVar struct {
	UUID        string `json:"uuid"`
	Key         string `json:"key"`
	Value       string `json:"value,omitempty"`
	RealValue   string `json:"real_value,omitempty"`
	IsBuildtime bool   `json:"is_buildtime,omitempty"`
	IsRuntime   bool   `json:"is_runtime,omitempty"`
	IsLiteral   bool   `json:"is_literal,omitempty"`
	IsMultiline bool   `json:"is_multiline,omitempty"`
	IsShared    bool   `json:"is_shared,omitempty"`
	IsPreview   bool   `json:"is_preview,omitempty"`
	IsCoolify   bool   `json:"is_coolify,omitempty"` // Coolify-managed, skip
	Order       int    `json:"order,omitempty"`
}

// EffectiveValue picks real_value over value because value can be a
// reference like "{{ ANOTHER_VAR }}" that Coolify hasn't resolved yet
// — real_value is what the running container actually sees.
func (e EnvVar) EffectiveValue() string {
	if e.RealValue != "" {
		return e.RealValue
	}
	return e.Value
}

// ParsedAt parses the Coolify ISO-8601 timestamp. Returns zero on
// parse failure — none of our migration paths actually need exact
// times, just rough chronology in reports.
func ParsedAt(s string) time.Time {
	t, _ := time.Parse("2006-01-02T15:04:05.000000Z", s)
	return t
}
