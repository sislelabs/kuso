package builds

import (
	"context"
	"encoding/json"
	"fmt"
	"io"
	"net/http"
	"net/url"
	"strings"
	"time"
)

// manifestAnyAccept asks for whatever manifest type the tag actually
// stores. buildkit's registry cache export (the :buildcache tag) is an
// OCI index, which a docker-v2-only Accept answers with 404 — the
// orphan sweep would then think the tag is gone and leave its (large)
// cache blobs pinned forever.
const manifestAnyAccept = "application/vnd.docker.distribution.manifest.v2+json, " +
	"application/vnd.docker.distribution.manifest.list.v2+json, " +
	"application/vnd.oci.image.manifest.v1+json, " +
	"application/vnd.oci.image.index.v1+json"

// RegistryInventory is the registry surface the orphan-repository sweep
// needs: enumerate repos + tags, resolve and delete manifests by digest.
type RegistryInventory interface {
	ListRepositories(ctx context.Context) ([]string, error)
	// ListTags returns the repo's tags; empty (nil error) for a repo
	// whose tags are all deleted or that no longer exists.
	ListTags(ctx context.Context, repo string) ([]string, error)
	// TagDigest resolves a tag of any manifest type to its digest; ""
	// (nil error) when the tag doesn't exist.
	TagDigest(ctx context.Context, repo, tag string) (string, error)
	// ImageCreated returns the image config's "created" time, or the
	// zero time when the manifest has no image config (a build cache).
	ImageCreated(ctx context.Context, repo, digest string) (time.Time, error)
	DeleteManifest(ctx context.Context, repo, digest string) error
}

// NewInClusterRegistryInventory returns the RegistryInventory for the
// default in-cluster registry, or nil when host is empty.
func NewInClusterRegistryInventory(host string) RegistryInventory {
	if host == "" {
		return nil
	}
	return newRegistryClient(host)
}

func (c *registryClient) url(path string) string {
	return fmt.Sprintf("%s://%s%s", c.scheme, c.host, path)
}

// getJSON GETs path and decodes the body into out. A 404 returns the
// response with out untouched so callers can tell "absent" from error.
func (c *registryClient) getJSON(ctx context.Context, path, accept string, out any) (*http.Response, error) {
	req, err := http.NewRequestWithContext(ctx, http.MethodGet, c.url(path), nil)
	if err != nil {
		return nil, fmt.Errorf("registry get request: %w", err)
	}
	if accept != "" {
		req.Header.Set("Accept", accept)
	}
	resp, err := c.http.Do(req)
	if err != nil {
		return nil, fmt.Errorf("registry get %s: %w", path, err)
	}
	defer resp.Body.Close()
	if resp.StatusCode == http.StatusNotFound {
		return resp, nil
	}
	if resp.StatusCode != http.StatusOK {
		return nil, fmt.Errorf("registry get %s: status %d", path, resp.StatusCode)
	}
	if err := json.NewDecoder(io.LimitReader(resp.Body, 8<<20)).Decode(out); err != nil {
		return nil, fmt.Errorf("registry decode %s: %w", path, err)
	}
	return resp, nil
}

// nextLink returns the path+query of a `Link: <…>; rel="next"`
// pagination header, or "" on the last page.
func nextLink(h http.Header) string {
	l := h.Get("Link")
	start, end := strings.Index(l, "<"), strings.Index(l, ">")
	if start < 0 || end <= start || !strings.Contains(l[end:], `rel="next"`) {
		return ""
	}
	u, err := url.Parse(l[start+1 : end])
	if err != nil {
		return ""
	}
	return u.RequestURI()
}

// listPaged walks a paginated catalog/tags endpoint. pick extracts the
// page's names. ok404 makes a 404 an empty result instead of an error.
func (c *registryClient) listPaged(ctx context.Context, path string, ok404 bool, pick func([]byte) ([]string, error)) ([]string, error) {
	var out []string
	for pages := 0; path != ""; pages++ {
		if pages >= 100 {
			return nil, fmt.Errorf("registry list %s: more than %d pages", path, pages)
		}
		var raw json.RawMessage
		resp, err := c.getJSON(ctx, path, "", &raw)
		if err != nil {
			return nil, err
		}
		if resp.StatusCode == http.StatusNotFound {
			if ok404 {
				return out, nil
			}
			return nil, fmt.Errorf("registry list %s: not found", path)
		}
		names, err := pick(raw)
		if err != nil {
			return nil, fmt.Errorf("registry decode %s: %w", path, err)
		}
		out = append(out, names...)
		path = nextLink(resp.Header)
	}
	return out, nil
}

func (c *registryClient) ListRepositories(ctx context.Context) ([]string, error) {
	return c.listPaged(ctx, "/v2/_catalog?n=1000", false, func(b []byte) ([]string, error) {
		var body struct {
			Repositories []string `json:"repositories"`
		}
		err := json.Unmarshal(b, &body)
		return body.Repositories, err
	})
}

func (c *registryClient) ListTags(ctx context.Context, repo string) ([]string, error) {
	return c.listPaged(ctx, "/v2/"+repo+"/tags/list?n=1000", true, func(b []byte) ([]string, error) {
		var body struct {
			Tags []string `json:"tags"` // null once every tag is deleted
		}
		err := json.Unmarshal(b, &body)
		return body.Tags, err
	})
}

func (c *registryClient) TagDigest(ctx context.Context, repo, tag string) (string, error) {
	req, err := http.NewRequestWithContext(ctx, http.MethodHead, c.url("/v2/"+repo+"/manifests/"+tag), nil)
	if err != nil {
		return "", fmt.Errorf("registry head request: %w", err)
	}
	req.Header.Set("Accept", manifestAnyAccept)
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

type registryManifest struct {
	Config struct {
		MediaType string `json:"mediaType"`
		Digest    string `json:"digest"`
	} `json:"config"`
	Manifests []struct {
		MediaType string `json:"mediaType"`
		Digest    string `json:"digest"`
	} `json:"manifests"`
}

func (c *registryClient) ImageCreated(ctx context.Context, repo, digest string) (time.Time, error) {
	var m registryManifest
	if _, err := c.getJSON(ctx, "/v2/"+repo+"/manifests/"+digest, manifestAnyAccept, &m); err != nil {
		return time.Time{}, err
	}
	// An index is dated by its first image manifest. A buildkit cache
	// index lists layer blobs instead, so it stays undated.
	for _, child := range m.Manifests {
		if child.MediaType == "application/vnd.oci.image.manifest.v1+json" ||
			child.MediaType == "application/vnd.docker.distribution.manifest.v2+json" {
			m = registryManifest{}
			if _, err := c.getJSON(ctx, "/v2/"+repo+"/manifests/"+child.Digest, manifestAnyAccept, &m); err != nil {
				return time.Time{}, err
			}
			break
		}
	}
	switch m.Config.MediaType {
	case "application/vnd.docker.container.image.v1+json", "application/vnd.oci.image.config.v1+json":
	default:
		return time.Time{}, nil
	}
	var cfg struct {
		Created time.Time `json:"created"`
	}
	if _, err := c.getJSON(ctx, "/v2/"+repo+"/blobs/"+m.Config.Digest, "", &cfg); err != nil {
		return time.Time{}, err
	}
	return cfg.Created, nil
}

func (c *registryClient) DeleteManifest(ctx context.Context, repo, digest string) error {
	req, err := http.NewRequestWithContext(ctx, http.MethodDelete, c.url("/v2/"+repo+"/manifests/"+digest), nil)
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
		return nil
	default:
		return fmt.Errorf("registry delete %s@%s: status %d", repo, digest, resp.StatusCode)
	}
}
