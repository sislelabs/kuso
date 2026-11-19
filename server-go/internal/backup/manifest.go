// Package backup holds datastore-backup primitives shared across the
// HTTP handler and (later) the producer registry. This file defines the
// manifest written next to every backup artifact so a restore can verify
// integrity before applying.
package backup

// SchemaVersion is the manifest schema the backup CronJobs write.
const SchemaVersion = 1

// Artifact is one backed-up object plus its integrity metadata. No
// secret values are ever recorded here.
type Artifact struct {
	Key         string `json:"key"`
	SHA256      string `json:"sha256"`
	Bytes       int64  `json:"bytes"`
	PayloadKind string `json:"payloadKind"`
	// Encryption is set when the stored object is encrypted. SHA256 is
	// then the digest of the decrypted payload, so the restore verifies
	// what it actually applies; Bytes stays the stored object's size.
	Encryption *ArtifactEncryption `json:"encryption,omitempty"`
}

// ArtifactEncryption names how an artifact was encrypted and which key
// (by KeyID fingerprint, never the key) decrypts it.
type ArtifactEncryption struct {
	Alg   string `json:"alg"`
	KeyID string `json:"keyId"`
}

// Manifest describes one backup run and its artifacts.
type Manifest struct {
	SchemaVersion int        `json:"schemaVersion"`
	CreatedAt     string     `json:"createdAt"`
	Project       string     `json:"project"`
	Addon         string     `json:"addon"`
	AddonKind     string     `json:"addonKind"`
	Producer      string     `json:"producer"`
	Artifacts     []Artifact `json:"artifacts"`
}
