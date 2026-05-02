package github

import (
	"context"
	"fmt"
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
	br, _, err := cli.Repositories.GetBranch(ctx, owner, repo, branch, 1)
	if err != nil {
		return nil, fmt.Errorf("get branch: %w", err)
	}
	if br == nil || br.Commit == nil || br.Commit.Commit == nil || br.Commit.Commit.Tree == nil || br.Commit.Commit.Tree.SHA == nil {
		return nil, nil
	}
	tree, _, err := cli.Git.GetTree(ctx, owner, repo, *br.Commit.Commit.Tree.SHA, true)
	if err != nil {
		return nil, fmt.Errorf("get tree: %w", err)
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

// DetectRuntime applies the auto-detect rules from
// docs/REDESIGN.md "Auto-detect runtime".
//
//   1. Dockerfile present → runtime=dockerfile, port=parseExpose() ?? 8080
//   2. index.html only (no package.json/go.mod/...) → runtime=static, port=80
//   3. package.json → runtime=nixpacks, port=guessNodePort
//   4. go.mod / Cargo.toml / requirements.txt / pyproject.toml → nixpacks, port=8080
//   5. fallback → nixpacks, port=8080
func (c *Client) DetectRuntime(ctx context.Context, installationID int64, owner, repo, branch, pathPrefix string) (*DetectedRuntime, error) {
	entries, err := c.ListRepoTree(ctx, installationID, owner, repo, branch, pathPrefix)
	if err != nil {
		return nil, err
	}
	has := func(name string) bool {
		for _, e := range entries {
			if e.Path == name {
				return true
			}
		}
		return false
	}
	prefixed := func(rel string) string {
		if pathPrefix == "" {
			return rel
		}
		return strings.TrimRight(pathPrefix, "/") + "/" + rel
	}

	if has("Dockerfile") {
		df, _ := c.ReadFile(ctx, installationID, owner, repo, branch, prefixed("Dockerfile"))
		port := parseExposePort(df)
		if port == 0 {
			port = 8080
		}
		return &DetectedRuntime{Runtime: "dockerfile", Port: port, Reason: "Dockerfile detected"}, nil
	}
	staticOnly := has("index.html") && !has("package.json") && !has("go.mod") && !has("Cargo.toml") &&
		!has("requirements.txt") && !has("pyproject.toml")
	if staticOnly {
		return &DetectedRuntime{Runtime: "static", Port: 80, Reason: "index.html only"}, nil
	}
	if has("package.json") {
		pkg, _ := c.ReadFile(ctx, installationID, owner, repo, branch, prefixed("package.json"))
		return &DetectedRuntime{Runtime: "nixpacks", Port: guessNodePort(pkg), Reason: "package.json detected"}, nil
	}
	if has("go.mod") {
		return &DetectedRuntime{Runtime: "nixpacks", Port: 8080, Reason: "go.mod detected"}, nil
	}
	if has("Cargo.toml") {
		return &DetectedRuntime{Runtime: "nixpacks", Port: 8080, Reason: "Cargo.toml detected"}, nil
	}
	if has("requirements.txt") || has("pyproject.toml") {
		return &DetectedRuntime{Runtime: "nixpacks", Port: 8080, Reason: "Python project detected"}, nil
	}
	return &DetectedRuntime{Runtime: "nixpacks", Port: 8080, Reason: "fallback"}, nil
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
