package github

import (
	"context"
	"errors"
	"fmt"
	"net/http"
	"regexp"
	"strings"

	gogithub "github.com/google/go-github/v66/github"
)

// RepoTreeEntry is the wire shape for /api/github/installations/.../tree.
type RepoTreeEntry struct {
	Path string `json:"path"`
	Type string `json:"type"` // "blob" or "tree"
	Size int    `json:"size"`
}

// ListRepoTree walks a repo's git tree at HEAD of the given branch.
// Optionally filters by path prefix and strips it from returned paths
// (mirrors the TS behaviour).
func (c *Client) ListRepoTree(ctx context.Context, installationID int64, owner, repo, branch, pathPrefix string) ([]RepoTreeEntry, error) {
	cli, err := c.Installation(installationID)
	if err != nil {
		return nil, err
	}
	return listRepoTree(ctx, cli, owner, repo, branch, pathPrefix)
}

func listRepoTree(ctx context.Context, cli *gogithub.Client, owner, repo, branch, pathPrefix string) ([]RepoTreeEntry, error) {
	br, resp, err := cli.Repositories.GetBranch(ctx, owner, repo, branch, 1)
	if err != nil {
		return nil, classifyAPIError("get branch", resp, err)
	}
	if br == nil || br.Commit == nil || br.Commit.Commit == nil || br.Commit.Commit.Tree == nil || br.Commit.Commit.Tree.SHA == nil {
		return nil, nil
	}
	tree, resp, err := cli.Git.GetTree(ctx, owner, repo, *br.Commit.Commit.Tree.SHA, true)
	if err != nil {
		return nil, classifyAPIError("get tree", resp, err)
	}
	out := make([]RepoTreeEntry, 0, len(tree.Entries))
	for _, e := range tree.Entries {
		typ := "blob"
		if e.GetType() == "tree" {
			typ = "tree"
		}
		out = append(out, RepoTreeEntry{Path: e.GetPath(), Type: typ, Size: e.GetSize()})
	}
	if pathPrefix == "" {
		return out, nil
	}
	prefix := strings.Trim(pathPrefix, "/") + "/"
	filtered := make([]RepoTreeEntry, 0, len(out))
	for _, e := range out {
		if !strings.HasPrefix(e.Path, prefix) {
			continue
		}
		filtered = append(filtered, RepoTreeEntry{Path: e.Path[len(prefix):], Type: e.Type, Size: e.Size})
	}
	return filtered, nil
}

// ReadFile fetches a single file's contents at HEAD of branch. Empty
// string + nil error on 404 — matches the TS readFile so the runtime
// detector can peek at Dockerfile/EXPOSE without exploding.
func (c *Client) ReadFile(ctx context.Context, installationID int64, owner, repo, branch, path string) (string, error) {
	cli, err := c.Installation(installationID)
	if err != nil {
		return "", err
	}
	file, _, _, err := cli.Repositories.GetContents(ctx, owner, repo, path, &gogithub.RepositoryContentGetOptions{Ref: branch})
	if err != nil {
		// go-github returns *github.ErrorResponse with Response.StatusCode for 404.
		var er *gogithub.ErrorResponse
		if asErrorResponse(err, &er) && er.Response != nil && er.Response.StatusCode == 404 {
			return "", nil
		}
		return "", fmt.Errorf("get contents: %w", err)
	}
	if file == nil {
		return "", nil
	}
	content, err := file.GetContent()
	if err != nil {
		return "", fmt.Errorf("decode content: %w", err)
	}
	return content, nil
}

// ErrRateLimited is GitHub's primary or secondary API rate limit.
var ErrRateLimited = errors.New("github: api rate limit exceeded")

// classifyAPIError tags a go-github error with the sentinel for GitHub's
// answer (ErrRepoNotFound, ErrRepoForbidden, ErrRateLimited) so handlers
// can return the real status via errors.Is instead of a bare 500.
// GetBranch (redirect-following) reports a non-200 as a plain error, so
// the status comes from resp when there's no typed go-github error.
func classifyAPIError(op string, resp *gogithub.Response, err error) error {
	var rl *gogithub.RateLimitError
	var abuse *gogithub.AbuseRateLimitError
	if errors.As(err, &rl) || errors.As(err, &abuse) {
		return fmt.Errorf("%w: %s: %w", ErrRateLimited, op, err)
	}
	status, msg := 0, ""
	var er *gogithub.ErrorResponse
	if errors.As(err, &er) && er.Response != nil {
		status, msg = er.Response.StatusCode, er.Message
	} else if resp != nil && resp.Response != nil {
		status = resp.StatusCode
	}
	switch status {
	case http.StatusNotFound:
		return fmt.Errorf("%w: %s: %w", ErrRepoNotFound, op, err)
	case http.StatusTooManyRequests:
		return fmt.Errorf("%w: %s: %w", ErrRateLimited, op, err)
	case http.StatusForbidden:
		if strings.Contains(strings.ToLower(msg), "rate limit") || rateLimitHeaders(resp) {
			return fmt.Errorf("%w: %s: %w", ErrRateLimited, op, err)
		}
		return fmt.Errorf("%w: %s: %w", ErrRepoForbidden, op, err)
	}
	return fmt.Errorf("%s: %w", op, err)
}

// rateLimitHeaders: GitHub marks a rate-limit 403 with an exhausted
// X-RateLimit-Remaining (primary) or a Retry-After (secondary).
func rateLimitHeaders(resp *gogithub.Response) bool {
	if resp == nil || resp.Response == nil {
		return false
	}
	return resp.Header.Get("X-RateLimit-Remaining") == "0" || resp.Header.Get("Retry-After") != ""
}

// asErrorResponse is a small wrapper around errors.As to keep the
// imports tidy in the hot path.
func asErrorResponse(err error, target **gogithub.ErrorResponse) bool {
	for cur := err; cur != nil; cur = unwrap(cur) {
		if er, ok := cur.(*gogithub.ErrorResponse); ok {
			*target = er
			return true
		}
	}
	return false
}

func unwrap(err error) error {
	type wrapper interface{ Unwrap() error }
	if w, ok := err.(wrapper); ok {
		return w.Unwrap()
	}
	return nil
}

// DetectedRuntime is the wire shape returned by /api/github/detect-runtime.
type DetectedRuntime struct {
	Runtime string `json:"runtime"`
	Port    int    `json:"port"`
	Reason  string `json:"reason"`
}

// DetectRuntime applies the auto-detect rules:
//
//   1. Dockerfile present → runtime=dockerfile, port=parseExpose() ?? 8080
//   2. index.html only (no package.json/go.mod/...) → runtime=static, port=80
//   3. *.csproj → runtime=buildpacks, port=8080 (Paketo binds ASPNETCORE_URLS to $PORT)
//   4. package.json → runtime=nixpacks, port=guessNodePort
//   5. go.mod / Cargo.toml / requirements.txt / pyproject.toml → nixpacks, port=8080
//   6. fallback → nixpacks, port=8080
func (c *Client) DetectRuntime(ctx context.Context, installationID int64, owner, repo, branch, pathPrefix string) (*DetectedRuntime, error) {
	entries, err := c.ListRepoTree(ctx, installationID, owner, repo, branch, pathPrefix)
	if err != nil {
		return nil, err
	}
	names := make([]string, 0, len(entries))
	for _, e := range entries {
		names = append(names, e.Path)
	}
	prefixed := func(rel string) string {
		if pathPrefix == "" {
			return rel
		}
		return strings.TrimRight(pathPrefix, "/") + "/" + rel
	}
	return DetectFromFiles(names, func(rel string) string {
		content, _ := c.ReadFile(ctx, installationID, owner, repo, branch, prefixed(rel))
		return content
	}), nil
}

// DetectFromFiles applies the auto-detect rules to a directory listing.
// names are the entries of the service's directory; read returns a file's
// contents ("" when unreadable). Shared by the GitHub App path and the
// no-App path (internal/gitremote).
func DetectFromFiles(names []string, read func(rel string) string) *DetectedRuntime {
	has := func(name string) bool {
		for _, n := range names {
			if n == name {
				return true
			}
		}
		return false
	}
	if has("Dockerfile") {
		port := parseExposePort(read("Dockerfile"))
		if port == 0 {
			port = 8080
		}
		return &DetectedRuntime{Runtime: "dockerfile", Port: port, Reason: "Dockerfile detected"}
	}
	staticOnly := has("index.html") && !has("package.json") && !has("go.mod") && !has("Cargo.toml") &&
		!has("requirements.txt") && !has("pyproject.toml")
	if staticOnly {
		return &DetectedRuntime{Runtime: "static", Port: 80, Reason: "index.html only"}
	}
	// Checked before package.json: ASP.NET templates often carry one for
	// front-end tooling. Not nixpacks: its C# provider defaults to the
	// .NET 6 SDK and hardcodes port 3000.
	var nestedCsproj string
	for _, n := range names {
		if !strings.HasSuffix(n, ".csproj") {
			continue
		}
		if !strings.Contains(n, "/") {
			return &DetectedRuntime{Runtime: "buildpacks", Port: 8080, Reason: ".NET project detected"}
		}
		if nestedCsproj == "" {
			nestedCsproj = n
		}
	}
	if has("package.json") {
		return &DetectedRuntime{Runtime: "nixpacks", Port: guessNodePort(read("package.json")), Reason: "package.json detected"}
	}
	if has("go.mod") {
		return &DetectedRuntime{Runtime: "nixpacks", Port: 8080, Reason: "go.mod detected"}
	}
	if has("Cargo.toml") {
		return &DetectedRuntime{Runtime: "nixpacks", Port: 8080, Reason: "Cargo.toml detected"}
	}
	if has("requirements.txt") || has("pyproject.toml") {
		return &DetectedRuntime{Runtime: "nixpacks", Port: 8080, Reason: "Python project detected"}
	}
	if nestedCsproj != "" {
		dir := nestedCsproj[:strings.LastIndex(nestedCsproj, "/")]
		return &DetectedRuntime{Runtime: "nixpacks", Port: 8080, Reason: ".NET project found in " + dir + "/ — re-run with --path " + dir}
	}
	return &DetectedRuntime{Runtime: "nixpacks", Port: 8080, Reason: "fallback"}
}

var exposeRE = regexp.MustCompile(`(?im)^\s*EXPOSE\s+(\d+)`)

// parseExposePort scans a Dockerfile for the first EXPOSE directive.
func parseExposePort(dockerfile string) int {
	m := exposeRE.FindStringSubmatch(dockerfile)
	if len(m) < 2 {
		return 0
	}
	var n int
	for _, c := range m[1] {
		if c < '0' || c > '9' {
			return 0
		}
		n = n*10 + int(c-'0')
		if n > 65535 {
			return 0
		}
	}
	return n
}

// guessNodePort returns 3000 by default for Node packages — same fallback
// the TS server uses.
func guessNodePort(pkgJSON string) int {
	// PORT references in package.json scripts are common; we don't try
	// to be clever and parse them. The TS code has the same heuristic.
	if pkgJSON == "" {
		return 3000
	}
	return 3000
}
