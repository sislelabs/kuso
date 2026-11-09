package vulnscan

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"net/http"
	"net/url"
	"strings"
	"time"
)

// PublicDigests resolves a tag on a public registry (Docker Hub, ghcr,
// quay, …) to its current manifest digest, anonymously. It speaks the
// registry v2 bearer-token challenge and uses HEAD, which Docker Hub
// doesn't count against its pull rate limit.
type PublicDigests struct{ HTTP *http.Client }

func (p PublicDigests) client() *http.Client {
	if p.HTTP != nil {
		return p.HTTP
	}
	return &http.Client{Timeout: 15 * time.Second}
}

// SplitTagRef splits "postgres:16" into the v2 API host, repository
// path and tag: ("registry-1.docker.io", "library/postgres", "16").
// A ref without a tag, or pinned by digest, has nothing to resolve.
func SplitTagRef(ref string) (apiHost, repo, tag string, err error) {
	if strings.Contains(ref, "@") {
		return "", "", "", fmt.Errorf("%w: %q is pinned by digest", ErrInvalid, ref)
	}
	name := ImageRepo(ref)
	tag = strings.TrimPrefix(ref, name)
	tag = strings.TrimPrefix(tag, ":")
	if tag == "" {
		return "", "", "", fmt.Errorf("%w: %q has no tag", ErrInvalid, ref)
	}
	host := ImageHost(ref)
	repo = name
	if strings.HasPrefix(name, host+"/") {
		repo = strings.TrimPrefix(name, host+"/")
	}
	apiHost = host
	if host == "docker.io" {
		apiHost = "registry-1.docker.io"
		if !strings.Contains(repo, "/") {
			repo = "library/" + repo
		}
	}
	return apiHost, repo, tag, nil
}

// LatestDigest returns the digest the tag points at now.
func (p PublicDigests) LatestDigest(ctx context.Context, ref string) (string, error) {
	apiHost, repo, tag, err := SplitTagRef(ref)
	if err != nil {
		return "", err
	}
	u := "https://" + apiHost + "/v2/" + repo + "/manifests/" + tag
	resp, err := p.head(ctx, u, "")
	if err != nil {
		return "", err
	}
	if resp.StatusCode == http.StatusUnauthorized {
		challenge := resp.Header.Get("WWW-Authenticate")
		resp.Body.Close()
		token, err := p.token(ctx, challenge, repo)
		if err != nil {
			return "", fmt.Errorf("resolve %s: %w", ref, err)
		}
		if resp, err = p.head(ctx, u, token); err != nil {
			return "", err
		}
	}
	defer resp.Body.Close()
	if resp.StatusCode != http.StatusOK {
		return "", fmt.Errorf("resolve %s: registry status %d", ref, resp.StatusCode)
	}
	d := resp.Header.Get("Docker-Content-Digest")
	if digestOf(d) == "" {
		return "", fmt.Errorf("resolve %s: no usable Docker-Content-Digest", ref)
	}
	return d, nil
}

func (p PublicDigests) head(ctx context.Context, u, token string) (*http.Response, error) {
	req, err := http.NewRequestWithContext(ctx, http.MethodHead, u, nil)
	if err != nil {
		return nil, err
	}
	req.Header.Set("Accept", manifestAccept)
	if token != "" {
		req.Header.Set("Authorization", "Bearer "+token)
	}
	resp, err := p.client().Do(req)
	if err != nil {
		return nil, fmt.Errorf("registry HEAD %s: %w", u, err)
	}
	return resp, nil
}

// token fetches an anonymous pull token from the challenge's realm.
func (p PublicDigests) token(ctx context.Context, challenge, repo string) (string, error) {
	params := parseBearerChallenge(challenge)
	realm := params["realm"]
	if realm == "" {
		return "", errors.New("registry wants auth but sent no bearer realm")
	}
	ru, err := url.Parse(realm)
	if err != nil || ru.Scheme != "https" {
		return "", fmt.Errorf("unusable token realm %q", realm)
	}
	q := ru.Query()
	if s := params["service"]; s != "" {
		q.Set("service", s)
	}
	scope := params["scope"]
	if scope == "" {
		scope = "repository:" + repo + ":pull"
	}
	q.Set("scope", scope)
	ru.RawQuery = q.Encode()
	req, err := http.NewRequestWithContext(ctx, http.MethodGet, ru.String(), nil)
	if err != nil {
		return "", err
	}
	resp, err := p.client().Do(req)
	if err != nil {
		return "", fmt.Errorf("token request: %w", err)
	}
	defer resp.Body.Close()
	if resp.StatusCode != http.StatusOK {
		return "", fmt.Errorf("token request: status %d", resp.StatusCode)
	}
	var body struct {
		Token       string `json:"token"`
		AccessToken string `json:"access_token"`
	}
	if err := json.NewDecoder(io.LimitReader(resp.Body, 1<<20)).Decode(&body); err != nil {
		return "", fmt.Errorf("token response: %w", err)
	}
	if body.Token != "" {
		return body.Token, nil
	}
	if body.AccessToken != "" {
		return body.AccessToken, nil
	}
	return "", errors.New("token response has no token")
}

// parseBearerChallenge reads `Bearer realm="…",service="…",scope="…"`.
func parseBearerChallenge(h string) map[string]string {
	out := map[string]string{}
	rest, ok := strings.CutPrefix(strings.TrimSpace(h), "Bearer ")
	if !ok {
		return out
	}
	for rest != "" {
		rest = strings.TrimLeft(rest, " ,")
		k, v, found := strings.Cut(rest, "=")
		if !found {
			break
		}
		k = strings.TrimSpace(k)
		if strings.HasPrefix(v, `"`) {
			end := strings.Index(v[1:], `"`)
			if end < 0 {
				out[k] = v[1:]
				break
			}
			out[k] = v[1 : end+1]
			rest = v[end+2:]
		} else {
			val, after, _ := strings.Cut(v, ",")
			out[k] = val
			rest = after
		}
	}
	return out
}
