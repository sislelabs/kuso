// GitHub PR-comment poster used by the reviewer flow to write the
// magic reviewer URL on PR open + post the decision back on submit.
//
// We don't try to be a general-purpose GH client — just the two
// endpoints the preview flow needs:
//
//   POST /repos/:owner/:repo/issues/:number/comments    (create)
//
// Implementation deliberately small + no caching: PR comments are
// rare (one on open, one on decision) so the simplest possible
// fetch-token → POST flow is correct.

package github

import (
	"bytes"
	"context"
	"encoding/json"
	"fmt"
	"net/http"
	"strings"
	"time"
)

// PostPRComment writes a comment on the named PR using a freshly
// minted installation token. owner/repo come from the PR's
// full_name; installationID is what the project's GH spec carries.
//
// Errors are returned wrapped — callers should log them and proceed
// (a failed comment is not a reason to fail the webhook handler;
// the preview env is already up).
func (c *Client) PostPRComment(ctx context.Context, installationID int64, repoFullName string, prNumber int, body string) error {
	if c == nil {
		return fmt.Errorf("github client not configured")
	}
	if !strings.Contains(repoFullName, "/") {
		return fmt.Errorf("invalid repo full_name %q", repoFullName)
	}
	token, err := c.MintInstallationToken(ctx, installationID)
	if err != nil {
		return fmt.Errorf("mint installation token: %w", err)
	}
	url := fmt.Sprintf("https://api.github.com/repos/%s/issues/%d/comments", repoFullName, prNumber)
	payload, _ := json.Marshal(map[string]string{"body": body})
	req, err := http.NewRequestWithContext(ctx, "POST", url, bytes.NewReader(payload))
	if err != nil {
		return err
	}
	req.Header.Set("Authorization", "Bearer "+token)
	req.Header.Set("Accept", "application/vnd.github+json")
	req.Header.Set("X-GitHub-Api-Version", "2022-11-28")
	req.Header.Set("Content-Type", "application/json")

	httpClient := &http.Client{Timeout: 10 * time.Second}
	resp, err := httpClient.Do(req)
	if err != nil {
		return fmt.Errorf("post comment: %w", err)
	}
	defer resp.Body.Close()
	if resp.StatusCode >= 300 {
		return fmt.Errorf("post comment: status %d", resp.StatusCode)
	}
	return nil
}
