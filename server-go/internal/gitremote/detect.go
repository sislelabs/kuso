package gitremote

import (
	"context"
	"encoding/json"
	"fmt"
	"io"
	"net/http"
	"net/url"
	"strings"

	"kuso/server/internal/github"
)

// maxDetectFile bounds a Dockerfile / package.json read.
const maxDetectFile = 1 << 20

// DetectRuntime guesses runtime and port for the service at path on
// branch, using GitHub's contents API (anonymous for public repos, the
// token for private ones). Other hosts return ErrUnsupported: the caller
// asks the user instead.
func (in *Inspector) DetectRuntime(ctx context.Context, repoURL, branch, path, token string) (*github.DetectedRuntime, error) {
	owner, repo, ok := githubOwnerRepo(repoURL)
	if !ok {
		return nil, fmt.Errorf("%w: runtime detection needs a github.com repo", ErrUnsupported)
	}
	dir := strings.Trim(path, "/")
	if dir == "." {
		dir = ""
	}
	contents := func(rel string) string {
		p := rel
		if dir != "" {
			p = strings.TrimSuffix(dir+"/"+rel, "/")
		}
		return fmt.Sprintf("%s/repos/%s/%s/contents/%s?ref=%s",
			in.githubAPI(), url.PathEscape(owner), url.PathEscape(repo), escapePath(p), url.QueryEscape(branch))
	}

	body, err := in.githubGet(ctx, contents(""), "application/vnd.github+json", token)
	if err != nil {
		return nil, err
	}
	var entries []struct {
		Name string `json:"name"`
	}
	if err := json.Unmarshal(body, &entries); err != nil {
		return nil, fmt.Errorf("%s is not a directory in %s/%s", path, owner, repo)
	}
	names := make([]string, 0, len(entries))
	for _, e := range entries {
		names = append(names, e.Name)
	}
	return github.DetectFromFiles(names, func(rel string) string {
		raw, err := in.githubGet(ctx, contents(rel), "application/vnd.github.raw+json", token)
		if err != nil {
			return ""
		}
		return string(raw)
	}), nil
}

func (in *Inspector) githubAPI() string {
	if in != nil && in.GitHubAPI != "" {
		return strings.TrimRight(in.GitHubAPI, "/")
	}
	return "https://api.github.com"
}

func (in *Inspector) githubGet(ctx context.Context, u, accept, token string) ([]byte, error) {
	req, err := http.NewRequestWithContext(ctx, http.MethodGet, u, nil)
	if err != nil {
		return nil, err
	}
	req.Header.Set("Accept", accept)
	req.Header.Set("User-Agent", "kuso")
	if token != "" {
		req.Header.Set("Authorization", "Bearer "+token)
	}
	resp, err := in.client().Do(req)
	if err != nil {
		return nil, fmt.Errorf("reach GitHub: %w", err)
	}
	defer resp.Body.Close()
	switch resp.StatusCode {
	case http.StatusOK:
		return io.ReadAll(io.LimitReader(resp.Body, maxDetectFile))
	case http.StatusUnauthorized, http.StatusNotFound:
		return nil, fmt.Errorf("%w (HTTP %d)", ErrNotAccessible, resp.StatusCode)
	case http.StatusForbidden, http.StatusTooManyRequests:
		// Anonymous callers share 60 requests/hour per IP.
		return nil, fmt.Errorf("GitHub API rate limit reached (HTTP %d)", resp.StatusCode)
	default:
		return nil, fmt.Errorf("GitHub API: HTTP %d", resp.StatusCode)
	}
}

// githubOwnerRepo splits https://github.com/<owner>/<repo>[.git].
func githubOwnerRepo(repoURL string) (owner, repo string, ok bool) {
	u, err := url.Parse(strings.TrimSpace(repoURL))
	if err != nil || !strings.EqualFold(u.Hostname(), "github.com") {
		return "", "", false
	}
	parts := strings.Split(strings.Trim(u.Path, "/"), "/")
	if len(parts) != 2 || parts[0] == "" || parts[1] == "" {
		return "", "", false
	}
	return parts[0], strings.TrimSuffix(parts[1], ".git"), true
}

func escapePath(p string) string {
	segs := strings.Split(p, "/")
	for i, s := range segs {
		segs[i] = url.PathEscape(s)
	}
	return strings.Join(segs, "/")
}
