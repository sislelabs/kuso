// Package imagecheck asks a container registry whether an image tag
// exists, before kuso rolls it out. Anonymous only: it speaks the
// registry v2 API with the Docker token handshake public registries use
// (Docker Hub, ghcr.io, quay.io …). A tag that needs credentials, or a
// registry that can't be reached, reads as Unknown, never as missing.
package imagecheck

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"net"
	"net/http"
	"net/url"
	"regexp"
	"strings"
	"syscall"
	"time"
)

// Status values (wire strings).
const (
	Found    = "found"
	NotFound = "not_found"
	Unknown  = "unknown"
)

type Result struct {
	Status string `json:"status"`
	Detail string `json:"detail,omitempty"`
}

var ErrInvalid = errors.New("image check: invalid reference")

// manifestAccept covers single-arch and multi-arch manifests in both the
// Docker and OCI media types; a registry 404s a HEAD whose Accept lists
// none of what it stores.
var manifestAccept = strings.Join([]string{
	"application/vnd.oci.image.index.v1+json",
	"application/vnd.docker.distribution.manifest.list.v2+json",
	"application/vnd.oci.image.manifest.v1+json",
	"application/vnd.docker.distribution.manifest.v2+json",
}, ", ")

// Checker holds the HTTP client. The zero value refuses private and
// loopback addresses, so a user-typed registry host can't be used to
// probe the cluster network.
type Checker struct {
	HTTP *http.Client
}

func (c *Checker) client() *http.Client {
	if c.HTTP != nil {
		return c.HTTP
	}
	return &http.Client{
		Timeout: 8 * time.Second,
		Transport: &http.Transport{
			DialContext:         (&net.Dialer{Timeout: 5 * time.Second, Control: publicOnly}).DialContext,
			TLSHandshakeTimeout: 5 * time.Second,
		},
		CheckRedirect: func(req *http.Request, via []*http.Request) error {
			if len(via) >= 5 {
				return errors.New("too many redirects")
			}
			return nil
		},
	}
}

// publicOnly rejects connections to non-public addresses after DNS
// resolution (so a hostname pointing inward is caught too).
func publicOnly(_, address string, _ syscall.RawConn) error {
	host, _, err := net.SplitHostPort(address)
	if err != nil {
		return err
	}
	ip := net.ParseIP(host)
	if ip == nil || ip.IsLoopback() || ip.IsPrivate() || ip.IsLinkLocalUnicast() || ip.IsLinkLocalMulticast() ||
		ip.IsUnspecified() || ip.IsMulticast() {
		return fmt.Errorf("refusing non-public address %s", host)
	}
	return nil
}

// Ref is a parsed image reference.
type Ref struct {
	Host string // registry API host, e.g. registry-1.docker.io
	Repo string // e.g. library/nginx
	Tag  string
}

var (
	repoRE = regexp.MustCompile(`^[a-z0-9]+(?:(?:[._]|__|-+)[a-z0-9]+)*(?:/[a-z0-9]+(?:(?:[._]|__|-+)[a-z0-9]+)*)*$`)
	tagRE  = regexp.MustCompile(`^[\w][\w.-]{0,127}$`)
)

// ParseRef splits repository + tag the way docker does: the first path
// segment is a registry host when it has a dot or a port or is
// "localhost"; Docker Hub single names live under library/.
func ParseRef(repository, tag string) (Ref, error) {
	repository = strings.TrimSpace(repository)
	if tag = strings.TrimSpace(tag); tag == "" {
		tag = "latest"
	}
	if !tagRE.MatchString(tag) {
		return Ref{}, fmt.Errorf("%w: tag %q", ErrInvalid, tag)
	}
	host, path := "docker.io", repository
	if i := strings.IndexByte(repository, '/'); i > 0 {
		first := repository[:i]
		if strings.ContainsAny(first, ".:") || first == "localhost" {
			host, path = first, repository[i+1:]
		}
	}
	if host == "docker.io" || host == "index.docker.io" {
		host = "registry-1.docker.io"
		if !strings.Contains(path, "/") {
			path = "library/" + path
		}
	}
	if !repoRE.MatchString(path) {
		return Ref{}, fmt.Errorf("%w: repository %q", ErrInvalid, repository)
	}
	return Ref{Host: host, Repo: path, Tag: tag}, nil
}

// Check HEADs the tag's manifest, doing the bearer-token handshake once
// when the registry asks for it.
func (c *Checker) Check(ctx context.Context, ref Ref) Result {
	u := url.URL{Scheme: "https", Host: ref.Host, Path: "/v2/" + ref.Repo + "/manifests/" + ref.Tag}
	res, err := c.head(ctx, u.String(), "")
	if err != nil {
		return Result{Status: Unknown, Detail: "couldn't reach " + ref.Host}
	}
	if res.StatusCode == http.StatusUnauthorized {
		token, terr := c.token(ctx, res.Header.Get("WWW-Authenticate"))
		if terr != nil {
			return Result{Status: Unknown, Detail: ref.Host + " needs credentials to check this image"}
		}
		if res, err = c.head(ctx, u.String(), token); err != nil {
			return Result{Status: Unknown, Detail: "couldn't reach " + ref.Host}
		}
	}
	switch res.StatusCode {
	case http.StatusOK:
		return Result{Status: Found}
	case http.StatusNotFound:
		return Result{Status: NotFound, Detail: fmt.Sprintf("%s has no tag %q for %s", ref.Host, ref.Tag, ref.Repo)}
	case http.StatusUnauthorized, http.StatusForbidden:
		// Docker Hub answers a missing repository with 401 after an
		// anonymous token; private and missing look the same from here.
		return Result{Status: Unknown, Detail: "the registry wouldn't say: the image is private or doesn't exist"}
	default:
		return Result{Status: Unknown, Detail: fmt.Sprintf("%s answered %d", ref.Host, res.StatusCode)}
	}
}

func (c *Checker) head(ctx context.Context, u, token string) (*http.Response, error) {
	req, err := http.NewRequestWithContext(ctx, http.MethodHead, u, nil)
	if err != nil {
		return nil, err
	}
	req.Header.Set("Accept", manifestAccept)
	if token != "" {
		req.Header.Set("Authorization", "Bearer "+token)
	}
	res, err := c.client().Do(req)
	if err != nil {
		return nil, err
	}
	_ = res.Body.Close()
	return res, nil
}

var challengeParam = regexp.MustCompile(`(\w+)="([^"]*)"`)

// token fetches an anonymous pull token from the challenge's realm.
func (c *Checker) token(ctx context.Context, challenge string) (string, error) {
	if !strings.HasPrefix(strings.ToLower(challenge), "bearer ") {
		return "", errors.New("no bearer challenge")
	}
	params := map[string]string{}
	for _, m := range challengeParam.FindAllStringSubmatch(challenge, -1) {
		params[strings.ToLower(m[1])] = m[2]
	}
	realm, err := url.Parse(params["realm"])
	if err != nil || realm.Scheme != "https" || realm.Host == "" {
		return "", errors.New("bad realm")
	}
	q := realm.Query()
	if s := params["service"]; s != "" {
		q.Set("service", s)
	}
	if s := params["scope"]; s != "" {
		q.Set("scope", s)
	}
	realm.RawQuery = q.Encode()
	req, err := http.NewRequestWithContext(ctx, http.MethodGet, realm.String(), nil)
	if err != nil {
		return "", err
	}
	res, err := c.client().Do(req)
	if err != nil {
		return "", err
	}
	defer res.Body.Close()
	if res.StatusCode != http.StatusOK {
		return "", fmt.Errorf("token endpoint answered %d", res.StatusCode)
	}
	var body struct {
		Token       string `json:"token"`
		AccessToken string `json:"access_token"`
	}
	if err := json.NewDecoder(io.LimitReader(res.Body, 1<<20)).Decode(&body); err != nil {
		return "", err
	}
	if body.Token != "" {
		return body.Token, nil
	}
	if body.AccessToken != "" {
		return body.AccessToken, nil
	}
	return "", errors.New("empty token")
}
