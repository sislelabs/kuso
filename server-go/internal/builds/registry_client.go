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
	"bytes"
	"context"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"fmt"
	"io"
	"net/http"
	"net/url"
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
//
// ResolveTagDigest exists because deletion is DIGEST-scoped while
// protection used to be TAG-scoped: the registry v2 API can only DELETE
// manifests by digest, and deleting a digest removes it for EVERY tag
// that points at it. Two byte-identical builds (same commit rebuilt, a
// retagged rollback) share one digest — so untagging an aged-out,
// unprotected tag could kill the manifest a protected live tag depended
// on → ImagePullBackOff on the next pod restart. The sweep uses
// ResolveTagDigest to protect by digest, not tag string.
type ImageDeleter interface {
	DeleteImageTag(ctx context.Context, repo, tag string) error
	// ResolveTagDigest returns the manifest digest a tag points at, or
	// "" (nil error) when the tag doesn't exist.
	ResolveTagDigest(ctx context.Context, repo, tag string) (string, error)
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

// ResolveTagDigest implements ImageDeleter — the sweep uses it to map
// protected tags (live env + cron images) to their manifest digests so
// it never deletes a manifest a protected tag shares.
func (c *registryClient) ResolveTagDigest(ctx context.Context, repo, tag string) (string, error) {
	return c.manifestDigest(ctx, repo, tag)
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

// ImageCopier copies an image between repositories of the in-cluster
// registry without rebuilding it (build reuse, reuse.go).
type ImageCopier interface {
	CopyImage(ctx context.Context, srcRepo, srcTag, dstRepo, dstTag string) (digest string, err error)
}

// NewInClusterImageCopier returns an ImageCopier for the in-cluster
// registry, or nil when host is empty.
func NewInClusterImageCopier(host string) ImageCopier {
	if host == "" {
		return nil
	}
	return newRegistryClient(host)
}

const copyManifestAccept = "application/vnd.docker.distribution.manifest.v2+json, " +
	"application/vnd.docker.distribution.manifest.list.v2+json, " +
	"application/vnd.oci.image.manifest.v1+json, " +
	"application/vnd.oci.image.index.v1+json"

type rawManifest struct {
	body        []byte
	contentType string
	digest      string
}

// manifestRefs is the subset of a manifest or index CopyImage walks.
type manifestRefs struct {
	Config *struct {
		Digest string `json:"digest"`
	} `json:"config"`
	Layers []struct {
		Digest string `json:"digest"`
	} `json:"layers"`
	Manifests []struct {
		Digest string `json:"digest"`
	} `json:"manifests"`
}

// CopyImage copies srcRepo:srcTag to dstRepo:dstTag by mounting every blob
// across repositories and PUTting the original manifest bytes, so the
// destination has the same digest. Index/list children are copied by
// digest first. A registry that answers a mount with 202 (it would need
// an upload instead) is an error: the caller falls back to building.
func (c *registryClient) CopyImage(ctx context.Context, srcRepo, srcTag, dstRepo, dstTag string) (string, error) {
	top, err := c.getManifest(ctx, srcRepo, srcTag)
	if err != nil {
		return "", err
	}
	var refs manifestRefs
	if err := json.Unmarshal(top.body, &refs); err != nil {
		return "", fmt.Errorf("registry copy: decode %s:%s: %w", srcRepo, srcTag, err)
	}
	if len(refs.Manifests) > 0 {
		for _, child := range refs.Manifests {
			m, err := c.getManifest(ctx, srcRepo, child.Digest)
			if err != nil {
				return "", err
			}
			if err := c.copyManifestBlobs(ctx, srcRepo, dstRepo, m); err != nil {
				return "", err
			}
			if _, err := c.putManifest(ctx, dstRepo, child.Digest, m); err != nil {
				return "", err
			}
		}
	} else if err := c.copyManifestBlobs(ctx, srcRepo, dstRepo, top); err != nil {
		return "", err
	}
	got, err := c.putManifest(ctx, dstRepo, dstTag, top)
	if err != nil {
		return "", err
	}
	if got != top.digest {
		return "", fmt.Errorf("registry copy: %s:%s digest %s differs from source %s", dstRepo, dstTag, got, top.digest)
	}
	return got, nil
}

func (c *registryClient) getManifest(ctx context.Context, repo, ref string) (rawManifest, error) {
	req, err := http.NewRequestWithContext(ctx, http.MethodGet, c.url(fmt.Sprintf("/v2/%s/manifests/%s", repo, ref)), nil)
	if err != nil {
		return rawManifest{}, fmt.Errorf("registry copy request: %w", err)
	}
	req.Header.Set("Accept", copyManifestAccept)
	resp, err := c.http.Do(req)
	if err != nil {
		return rawManifest{}, fmt.Errorf("registry copy: get %s:%s: %w", repo, ref, err)
	}
	defer resp.Body.Close()
	if resp.StatusCode != http.StatusOK {
		return rawManifest{}, fmt.Errorf("registry copy: get %s:%s: status %d", repo, ref, resp.StatusCode)
	}
	body, err := io.ReadAll(io.LimitReader(resp.Body, 4<<20))
	if err != nil {
		return rawManifest{}, fmt.Errorf("registry copy: read %s:%s: %w", repo, ref, err)
	}
	sum := sha256.Sum256(body)
	digest := "sha256:" + hex.EncodeToString(sum[:])
	if h := resp.Header.Get("Docker-Content-Digest"); h != "" && h != digest {
		return rawManifest{}, fmt.Errorf("registry copy: %s:%s body digest %s does not match header %s", repo, ref, digest, h)
	}
	return rawManifest{body: body, contentType: resp.Header.Get("Content-Type"), digest: digest}, nil
}

func (c *registryClient) copyManifestBlobs(ctx context.Context, srcRepo, dstRepo string, m rawManifest) error {
	var refs manifestRefs
	if err := json.Unmarshal(m.body, &refs); err != nil {
		return fmt.Errorf("registry copy: decode manifest: %w", err)
	}
	var blobs []string
	if refs.Config != nil && refs.Config.Digest != "" {
		blobs = append(blobs, refs.Config.Digest)
	}
	for _, l := range refs.Layers {
		blobs = append(blobs, l.Digest)
	}
	for _, dg := range blobs {
		if err := c.mountBlob(ctx, srcRepo, dstRepo, dg); err != nil {
			return err
		}
	}
	return nil
}

func (c *registryClient) mountBlob(ctx context.Context, srcRepo, dstRepo, digest string) error {
	head, err := http.NewRequestWithContext(ctx, http.MethodHead, c.url(fmt.Sprintf("/v2/%s/blobs/%s", dstRepo, digest)), nil)
	if err != nil {
		return fmt.Errorf("registry copy request: %w", err)
	}
	resp, err := c.http.Do(head)
	if err != nil {
		return fmt.Errorf("registry copy: head blob %s: %w", digest, err)
	}
	resp.Body.Close()
	if resp.StatusCode == http.StatusOK {
		return nil
	}
	q := url.Values{"mount": {digest}, "from": {srcRepo}}
	post, err := http.NewRequestWithContext(ctx, http.MethodPost, c.url(fmt.Sprintf("/v2/%s/blobs/uploads/?%s", dstRepo, q.Encode())), nil)
	if err != nil {
		return fmt.Errorf("registry copy request: %w", err)
	}
	resp, err = c.http.Do(post)
	if err != nil {
		return fmt.Errorf("registry copy: mount %s: %w", digest, err)
	}
	resp.Body.Close()
	if resp.StatusCode != http.StatusCreated {
		return fmt.Errorf("registry copy: mount %s from %s into %s: status %d", digest, srcRepo, dstRepo, resp.StatusCode)
	}
	return nil
}

func (c *registryClient) putManifest(ctx context.Context, repo, ref string, m rawManifest) (string, error) {
	req, err := http.NewRequestWithContext(ctx, http.MethodPut, c.url(fmt.Sprintf("/v2/%s/manifests/%s", repo, ref)), bytes.NewReader(m.body))
	if err != nil {
		return "", fmt.Errorf("registry copy request: %w", err)
	}
	req.Header.Set("Content-Type", m.contentType)
	resp, err := c.http.Do(req)
	if err != nil {
		return "", fmt.Errorf("registry copy: put %s:%s: %w", repo, ref, err)
	}
	defer resp.Body.Close()
	if resp.StatusCode != http.StatusCreated && resp.StatusCode != http.StatusOK {
		return "", fmt.Errorf("registry copy: put %s:%s: status %d", repo, ref, resp.StatusCode)
	}
	return resp.Header.Get("Docker-Content-Digest"), nil
}
