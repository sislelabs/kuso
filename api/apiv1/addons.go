package v1

// Wire shapes for /api/projects/{project}/addons.

// CreateAddonRequest is the body of POST /api/projects/:p/addons.
// External and UseInstanceAddon are mutually exclusive with the
// default "managed StatefulSet" path; setting either switches the
// addon into the connect-to-existing / shared-server mode.
type CreateAddonRequest struct {
	Name        string `json:"name"`
	Kind        string `json:"kind"`
	Version     string `json:"version,omitempty"`
	Size        string `json:"size,omitempty"`
	HA          bool   `json:"ha,omitempty"`
	StorageSize string `json:"storageSize,omitempty"`
	Database    string `json:"database,omitempty"`
	// External, when set, switches the addon into connect-to-
	// existing mode: no StatefulSet is provisioned; kuso mirrors
	// the user-provided Secret as the addon's <name>-conn so
	// envFromSecrets works the same way as a native addon.
	External *AddonExternalSpec `json:"external,omitempty"`
	// UseInstanceAddon switches the addon into instance-shared
	// mode: kuso creates a per-project database on the shared
	// server (whose admin DSN is registered in the
	// kuso-instance-shared Secret as INSTANCE_ADDON_<UPPER>_DSN_ADMIN)
	// and writes the per-project DSN into <name>-conn.
	UseInstanceAddon string `json:"useInstanceAddon,omitempty"`
	// Pooler enables an opt-in PgBouncer connection pooler in front
	// of a kind=postgres addon. Nil = no pooler.
	Pooler *AddonPoolerSpec `json:"pooler,omitempty"`
	// TLS opts a kind=postgres addon into in-cluster wire TLS.
	// "" / "disable" (default) = plaintext + sslmode=disable.
	// "require" = serve TLS via a self-signed cert + advertise
	// sslmode=require, for apps that mandate encrypted DB
	// connections. Go pgx/libpq accept the self-signed chain;
	// default node-postgres rejects it, so this is opt-in.
	TLS string `json:"tls,omitempty"`
}

// AddonExternalSpec tells the server to skip provisioning and
// mirror an existing kube Secret as the addon's <name>-conn secret.
// SecretKeys is an optional allow-list; empty mirrors every key.
type AddonExternalSpec struct {
	SecretName string   `json:"secretName,omitempty"`
	SecretKeys []string `json:"secretKeys,omitempty"`
}

// UpdateAddonRequest is the partial-update body for PATCH
// /api/projects/{p}/addons/{a}. Pointer fields distinguish
// "leave alone" (nil) from "set to zero".
type UpdateAddonRequest struct {
	Version     *string             `json:"version,omitempty"`
	Size        *string             `json:"size,omitempty"`
	HA          *bool               `json:"ha,omitempty"`
	StorageSize *string             `json:"storageSize,omitempty"`
	Database    *string             `json:"database,omitempty"`
	Backup      *UpdateAddonBackup  `json:"backup,omitempty"`
	// Pooler toggles the opt-in PgBouncer pooler. Nil = leave alone.
	Pooler *AddonPoolerSpec `json:"pooler,omitempty"`
	// TLS flips in-cluster wire TLS on a kind=postgres addon
	// ("disable" | "require"). Live-safe: only the pod template +
	// conn secret re-render; the data PVC is untouched. Subscribed
	// envs must restart to pick up the new sslmode. Nil = leave alone.
	TLS *string `json:"tls,omitempty"`
}

// UpdateAddonBackup carries the per-addon backup schedule +
// retention. Pointer fields so callers can update one knob at a
// time. Setting Schedule = "" disables the cronjob (chart drops
// the resource); it's the canonical way to turn off scheduled
// backups via API.
type UpdateAddonBackup struct {
	Schedule      *string `json:"schedule,omitempty"`
	RetentionDays *int    `json:"retentionDays,omitempty"`
}

// AddonPoolerSpec is the opt-in connection-pooler block. Only
// meaningful for kind=postgres.
//
// Enabled is a plain bool, not a pointer: the optionality lives one
// level up in the *AddonPoolerSpec pointer on Create/UpdateAddonRequest
// (nil = "leave alone / no pooler"). A present block always carries an
// explicit enabled value — on the update path the handler maps it to
// the domain layer's *bool patch, so `{"pooler":{"enabled":false}}`
// deliberately means "turn the pooler off", not "unspecified".
type AddonPoolerSpec struct {
	Enabled bool `json:"enabled"`
}
