// Package backup holds datastore-backup primitives shared across the
// HTTP handler and (later) the producer registry. This file defines the
// manifest written next to every backup artifact so a restore can verify
// integrity before applying.
package backup

import (
	"encoding/json"
	"fmt"
)

// SchemaVersion is the current manifest schema. A manifest with a higher
// version than this binary understands is rejected rather than
// mis-parsed.
const SchemaVersion = 1

// Artifact is one backed-up object plus its integrity metadata. No
// secret values are ever recorded here.
type Artifact struct {
	Key         string `json:"key"`
	SHA256      string `json:"sha256"`
	Bytes       int64  `json:"bytes"`
	PayloadKind string `json:"payloadKind"`
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

// ManifestKey returns the S3 key of the manifest stored beside an
// artifact.
func ManifestKey(artifactKey string) string {
	return artifactKey + ".manifest.json"
}

// Parse unmarshals a manifest and rejects a schema newer than this
// binary supports.
func Parse(b []byte) (*Manifest, error) {
	var m Manifest
	if err := json.Unmarshal(b, &m); err != nil {
		return nil, fmt.Errorf("parse manifest: %w", err)
	}
	if m.SchemaVersion > SchemaVersion {
		return nil, fmt.Errorf("manifest schemaVersion %d newer than supported %d — upgrade kuso", m.SchemaVersion, SchemaVersion)
	}
	return &m, nil
}

// ArtifactFor finds the artifact entry for an S3 key.
func (m *Manifest) ArtifactFor(key string) (*Artifact, bool) {
	for i := range m.Artifacts {
		if m.Artifacts[i].Key == key {
			return &m.Artifacts[i], true
		}
	}
	return nil, false
}
