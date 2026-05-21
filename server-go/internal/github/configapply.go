package github

import (
	"context"
	"fmt"
	"strings"

	"kuso/server/internal/kube"
	"kuso/server/internal/spec"
)

// configFileNames are the kuso.yaml file names tried, in order, at the
// repo root. The first one that exists wins.
var configFileNames = []string{"kuso.yaml", "kuso.yml"}

// fetchFunc retrieves a file from a repo at a ref. ok=false means the
// file does not exist (a 404) — the common, non-error case.
type fetchFunc func(ctx context.Context, owner, repo, ref, path string) (content []byte, ok bool, err error)

// applyFunc parses+plans+applies a kuso.yaml body.
type applyFunc func(ctx context.Context, raw []byte) error

// applyConfigFromRepo fetches kuso.yaml (then kuso.yml) from the repo
// at the pushed ref and applies it. A missing file is not an error.
// The file's project must match the resolved project — a mismatch is
// rejected so a webhook can never mutate a different project.
func applyConfigFromRepo(ctx context.Context, fetch fetchFunc, apply applyFunc, owner, repo, ref, project string) error {
	var raw []byte
	var found bool
	for _, name := range configFileNames {
		content, ok, err := fetch(ctx, owner, repo, ref, name)
		if err != nil {
			return fmt.Errorf("fetch %s: %w", name, err)
		}
		if ok {
			raw, found = content, true
			break
		}
	}
	if !found {
		return nil // no config file in the repo — nothing to do
	}
	f, err := spec.Parse(raw)
	if err != nil {
		return fmt.Errorf("parse kuso.yaml: %w", err)
	}
	if f.Project != project {
		return fmt.Errorf("kuso.yaml project %q does not match repo's project %q", f.Project, project)
	}
	return apply(ctx, raw)
}

// splitFullName splits a GitHub "owner/repo" full name into its two
// parts. Returns empty strings when the input is not in that form.
func splitFullName(fullName string) (owner, repo string) {
	parts := strings.SplitN(strings.TrimSpace(fullName), "/", 2)
	if len(parts) != 2 || parts[0] == "" || parts[1] == "" {
		return "", ""
	}
	return parts[0], parts[1]
}

// configAsCodeEnabled reports whether the project opts into the
// kuso.yaml-on-push behaviour. Nil block = default-on.
func configAsCodeEnabled(proj *kube.KusoProject) bool {
	if proj == nil {
		return false
	}
	return proj.Spec.ConfigAsCode == nil || proj.Spec.ConfigAsCode.Enabled
}
