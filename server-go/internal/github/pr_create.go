// GitHub PR-creation methods used by the incident-agent implement flow.
//
// The agent pushes its fix commits to a branch itself (git clone with the
// short-lived installation token), then asks the server to open the PR. So
// the methods here are deliberately the minimal branch+PR+merge surface —
// no blob/tree/commit plumbing — wrapping go-github's higher-level
// services against the per-installation client.
//
//	CreateBranch → POST /repos/:owner/:repo/git/refs
//	OpenPR       → POST /repos/:owner/:repo/pulls
//	MergePR      → PUT  /repos/:owner/:repo/pulls/:number/merge
//
// Unlike pr_comments.go (raw HTTP + freshly minted token) these go through
// the cached installation client (c.Installation), matching the rest of
// client.go.

package github

import (
	"context"
	"fmt"
	"strings"

	gogithub "github.com/google/go-github/v66/github"
)

// CreateBranch creates refs/heads/<newBranch> pointing at the head commit
// of fromBranch, returning the resolved SHA the new branch was cut from.
//
// fromBranch is resolved via ResolveBranchSHA (reused from client.go); an
// empty/unknown fromBranch is an error here — we will not create a ref off
// a SHA we couldn't resolve.
func (c *Client) CreateBranch(ctx context.Context, installationID int64, owner, repo, fromBranch, newBranch string) (string, error) {
	if c == nil {
		return "", fmt.Errorf("github client not configured")
	}
	if owner == "" || repo == "" {
		return "", fmt.Errorf("github: create branch: owner and repo required")
	}
	if newBranch == "" {
		return "", fmt.Errorf("github: create branch: new branch name required")
	}
	sha, err := c.ResolveBranchSHA(ctx, installationID, owner, repo, fromBranch)
	if err != nil {
		return "", fmt.Errorf("github: resolve %s: %w", fromBranch, err)
	}
	if sha == "" {
		return "", fmt.Errorf("github: source branch %q not found", fromBranch)
	}
	cli, err := c.Installation(installationID)
	if err != nil {
		return "", err
	}
	ref := &gogithub.Reference{
		Ref:    gogithub.String("refs/heads/" + newBranch),
		Object: &gogithub.GitObject{SHA: gogithub.String(sha)},
	}
	if _, _, err := cli.Git.CreateRef(ctx, owner, repo, ref); err != nil {
		return "", fmt.Errorf("github: create ref refs/heads/%s: %w", newBranch, err)
	}
	return sha, nil
}

// OpenPR opens a pull request from head into base and returns the PR's
// html_url + number. head/base are branch names on the same repo (no
// cross-fork support — the agent always works in-repo).
func (c *Client) OpenPR(ctx context.Context, installationID int64, owner, repo, head, base, title, body string) (string, int, error) {
	if c == nil {
		return "", 0, fmt.Errorf("github client not configured")
	}
	if owner == "" || repo == "" {
		return "", 0, fmt.Errorf("github: open pr: owner and repo required")
	}
	if head == "" || base == "" {
		return "", 0, fmt.Errorf("github: open pr: head and base required")
	}
	cli, err := c.Installation(installationID)
	if err != nil {
		return "", 0, err
	}
	pr, _, err := cli.PullRequests.Create(ctx, owner, repo, &gogithub.NewPullRequest{
		Title: gogithub.String(title),
		Head:  gogithub.String(head),
		Base:  gogithub.String(base),
		Body:  gogithub.String(body),
	})
	if err != nil {
		return "", 0, fmt.Errorf("github: create pr %s→%s: %w", head, base, err)
	}
	return pr.GetHTMLURL(), pr.GetNumber(), nil
}

// MergePR merges PR number using the given method ("merge" | "squash" |
// "rebase"); an empty/unknown method defaults to "squash" (kuso's house
// style — one commit per fix on the default branch).
func (c *Client) MergePR(ctx context.Context, installationID int64, owner, repo string, number int, method string) error {
	if c == nil {
		return fmt.Errorf("github client not configured")
	}
	if owner == "" || repo == "" {
		return fmt.Errorf("github: merge pr: owner and repo required")
	}
	if number <= 0 {
		return fmt.Errorf("github: merge pr: invalid number %d", number)
	}
	cli, err := c.Installation(installationID)
	if err != nil {
		return err
	}
	res, _, err := cli.PullRequests.Merge(ctx, owner, repo, number, "", &gogithub.PullRequestOptions{
		MergeMethod: normalizeMergeMethod(method),
	})
	if err != nil {
		return fmt.Errorf("github: merge pr #%d: %w", number, err)
	}
	if !res.GetMerged() {
		// GitHub returns 200 with merged:false for "not mergeable" cases
		// (conflicts, required checks pending) — surface the message.
		return fmt.Errorf("github: merge pr #%d not merged: %s", number, res.GetMessage())
	}
	return nil
}

// normalizeMergeMethod maps a caller-supplied method to one GitHub accepts,
// defaulting to "squash".
func normalizeMergeMethod(method string) string {
	switch strings.ToLower(strings.TrimSpace(method)) {
	case "merge":
		return "merge"
	case "rebase":
		return "rebase"
	case "squash", "":
		return "squash"
	default:
		return "squash"
	}
}

// parseRepoFullName splits an "owner/repo" string into its parts. It
// tolerates a leading/trailing slash but rejects anything that isn't
// exactly two non-empty segments (e.g. "owner", "owner/repo/extra", "/",
// "owner/"). The full_name carried on PR/repo payloads is always the
// two-segment form, so callers that already have owner+repo split don't
// need this — it exists for the handler path that receives a single
// repoFullName string (mirrors PostPRComment's input shape).
func parseRepoFullName(fullName string) (owner, repo string, err error) {
	trimmed := strings.Trim(strings.TrimSpace(fullName), "/")
	parts := strings.Split(trimmed, "/")
	if len(parts) != 2 || parts[0] == "" || parts[1] == "" {
		return "", "", fmt.Errorf("github: invalid repo full_name %q", fullName)
	}
	return parts[0], parts[1], nil
}
