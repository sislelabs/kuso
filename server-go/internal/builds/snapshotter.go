package builds

import (
	"context"
	"fmt"
	"strings"

	"kuso/server/internal/kube"
)

// addonNamesFromEnvFromSecrets extracts addon names from an env's
// EnvFromSecrets list, whose entries are "<addon>-conn" secret names.
func addonNamesFromEnvFromSecrets(env *kube.KusoEnvironment) []string {
	var out []string
	for _, s := range env.Spec.EnvFromSecrets {
		if name, ok := strings.CutSuffix(s, "-conn"); ok && name != "" {
			out = append(out, name)
		}
	}
	return out
}

// AddonKindLister reports an addon's kind. Wired to the addons service in
// main.go; kept as a narrow interface so the snapshotter stays testable
// and the builds package doesn't import the addons/handlers packages.
type AddonKindLister interface {
	AddonKind(ctx context.Context, project, addon string) (string, error)
}

// SnapshotJobCreator creates a backup Job for one addon and returns the S3
// key the backup will write. Wired to the backup Job builder in main.go.
type SnapshotJobCreator interface {
	CreateSnapshotJob(ctx context.Context, project, addon, trigger, buildRef string) (key string, err error)
}

// PredeploySnapshotter is the concrete Snapshotter wired in main.go. It
// snapshots an env's subscribed postgres addons before a release hook runs.
type PredeploySnapshotter struct {
	Kinds AddonKindLister
	Jobs  SnapshotJobCreator
}

// Snapshot backs up every subscribed postgres addon and returns the S3
// keys. A non-nil error means the caller must NOT proceed into the
// migration (the promised safety net could not be taken).
func (s *PredeploySnapshotter) Snapshot(ctx context.Context, ns string, env *kube.KusoEnvironment) ([]string, error) {
	project := env.Spec.Project
	var keys []string
	for _, addon := range addonNamesFromEnvFromSecrets(env) {
		kind, err := s.Kinds.AddonKind(ctx, project, addon)
		if err != nil {
			return nil, fmt.Errorf("resolve addon %q kind: %w", addon, err)
		}
		if kind != "postgres" {
			continue // postgres-only per design
		}
		key, err := s.Jobs.CreateSnapshotJob(ctx, project, addon, "pre_deploy", env.Name)
		if err != nil {
			return nil, fmt.Errorf("snapshot addon %q: %w", addon, err)
		}
		keys = append(keys, key)
	}
	return keys, nil
}
