// Minimal client for the in-cluster registry's v2 delete API. Used by
// the image-retention sweep to untag build images that have aged out of
// the rollback window. Once a tag is removed, the weekly registry GC
// CronJob (operator/helm-charts/kuso/templates/registry-gc.yaml) reclaims
// the now-unreferenced blobs — we only do the untag here, not blob GC.
//
// The default in-cluster registry (kuso-registry, registry:2) is
// anonymous and runs with REGISTRY_STORAGE_DELETE_ENABLED=true, so a
// plain HTTP DELETE works with no auth. External registries (when
// RegistryAuthSecret is set) are NOT pruned here — their lifecycle is
// the operator's responsibility — so DeleteImageTag is a no-op-with-warn
// for non-default hosts.

package builds

import (
	"context"
	"fmt"
	"net/http"
	"time"
)

// manifestV2Accept is the media type that makes the registry return the
// v2 schema-2 manifest, whose Docker-Content-Digest header we need to
// address the DELETE. Requesting without it can yield a schema-1
// manifest with a DIFFERENT digest, and deleting that digest leaves the
// schema-2 blob dangling.
const manifestV2Accept = "application/vnd.docker.distribution.manifest.v2+json"

// ImageDeleter unties an image tag from a registry. Exported so main.go
// can wire it into the Poller; an interface so the sweep is testable
// without a live registry.
type ImageDeleter interface {
	DeleteImageTag(ctx context.Context, repo, tag string) error
}

// NewInClusterImageDeleter returns an ImageDeleter for the default
// in-cluster registry (RegistryHost). Returns nil when host is empty
// (external-registry clusters where we don't own image lifecycle) so
// the sweep self-disables — assigning a nil ImageDeleter to the Poller
// skips the image-retention sweep.
func NewInClusterImageDeleter(host string) ImageDeleter {
	if host == "" {
		return nil
	}
	return newRegistryClient(host)
}

// registryClient talks to one registry host over plain HTTP. host is
// "kuso-registry.kuso.svc.cluster.local:5000" (no scheme).
type registryClient struct {
	host   string
	http   *http.Client
	scheme string
}

func newRegistryClient(host string) *registryClient {
	return &registryClient{
		host:   host,
		scheme: "http", // in-cluster registry is plaintext
		http:   &http.Client{Timeout: 15 * time.Second},
	}
}

// DeleteImageTag resolves the tag's manifest digest, then deletes the
// manifest by digest (the registry v2 API can only DELETE by digest,
// not by tag). Returns nil if the tag is already gone (404) — the sweep
// is idempotent. repo is "<project>/<service>".
func (c *registryClient) DeleteImageTag(ctx context.Context, repo, tag string) error {
	digest, err := c.manifestDigest(ctx, repo, tag)
	if err != nil {
		return err
	}
	if digest == "" {
		return nil // already absent
	}
	url := fmt.Sprintf("%s://%s/v2/%s/manifests/%s", c.scheme, c.host, repo, digest)
	req, err := http.NewRequestWithContext(ctx, http.MethodDelete, url, nil)
	if err != nil {
		return fmt.Errorf("registry delete request: %w", err)
	}
	resp, err := c.http.Do(req)
	if err != nil {
		return fmt.Errorf("registry delete: %w", err)
	}
	defer resp.Body.Close()
	switch resp.StatusCode {
	case http.StatusAccepted, http.StatusOK, http.StatusNotFound:
		return nil // 404 = already deleted; idempotent
	default:
		return fmt.Errorf("registry delete %s@%s: status %d", repo, digest, resp.StatusCode)
	}
}

// manifestDigest does a HEAD on the tagged manifest and returns the
// Docker-Content-Digest header. Empty string (nil error) when the tag
// doesn't exist (404).
func (c *registryClient) manifestDigest(ctx context.Context, repo, tag string) (string, error) {
	url := fmt.Sprintf("%s://%s/v2/%s/manifests/%s", c.scheme, c.host, repo, tag)
	req, err := http.NewRequestWithContext(ctx, http.MethodHead, url, nil)
	if err != nil {
		return "", fmt.Errorf("registry head request: %w", err)
	}
	req.Header.Set("Accept", manifestV2Accept)
	resp, err := c.http.Do(req)
	if err != nil {
		return "", fmt.Errorf("registry head: %w", err)
	}
	defer resp.Body.Close()
	if resp.StatusCode == http.StatusNotFound {
		return "", nil
	}
	if resp.StatusCode != http.StatusOK {
		return "", fmt.Errorf("registry head %s:%s: status %d", repo, tag, resp.StatusCode)
	}
	digest := resp.Header.Get("Docker-Content-Digest")
	if digest == "" {
		return "", fmt.Errorf("registry head %s:%s: no Docker-Content-Digest header", repo, tag)
	}
	return digest, nil
}
