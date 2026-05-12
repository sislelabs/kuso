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
