// PR-state lookup used by the preview TTL sweep. The sweep must not
// delete a TTL-expired preview whose PR is still open (the TTL only
// proves "no pushes for N days", not abandonment — see the tickero
// PR 46 incident of 2026-08-06), so before teardown it asks: is this
// PR open on ANY of the project's repos?

package github

import (
	"context"
	"errors"
	"fmt"
	"strings"
)

// PROpenInAny reports whether PR #number is open on any of repoURLs.
// Multi-repo projects preview per-service repos, and the caller does
// not know which repo the PR belongs to — so every distinct repo is
// consulted and "open anywhere" wins.
//
// Semantics per repo:
//   - PR found, state=open → (true, nil) immediately.
//   - PR found, closed/merged → definitive no for this repo, continue.
//   - 404 (no such PR in this repo) → definitive no, continue.
//   - any other failure (installation unresolvable, network, rate
//     limit) → recorded; if NO repo answered "open", the first such
//     error is returned so the caller keeps the env and retries,
//     instead of treating "couldn't ask" as "closed".
func (c *Client) PROpenInAny(ctx context.Context, store CacheStore, repoURLs []string, number int) (bool, error) {
	if c == nil {
		return false, errors.New("github client not configured")
	}
	if number <= 0 {
		return false, fmt.Errorf("github: invalid pr number %d", number)
	}
	seen := map[string]bool{}
	var firstErr error
	checked := 0
	for _, raw := range repoURLs {
		owner, repo := ParseGithubRepoURL(raw)
		if owner == "" || repo == "" {
			continue
		}
		key := strings.ToLower(owner + "/" + repo)
		if seen[key] {
			continue
		}
		seen[key] = true
		instID, err := ResolveInstallationForRepo(ctx, store, owner, repo)
		if err != nil {
			if firstErr == nil {
				firstErr = fmt.Errorf("resolve installation for %s: %w", key, err)
			}
			continue
		}
		cli, err := c.Installation(instID)
		if err != nil {
			if firstErr == nil {
				firstErr = fmt.Errorf("installation client %d: %w", instID, err)
			}
			continue
		}
		pr, resp, err := cli.PullRequests.Get(ctx, owner, repo, number)
		if err != nil {
			if resp != nil && resp.StatusCode == 404 {
				checked++ // definitive: this repo has no such PR
				continue
			}
			if firstErr == nil {
				firstErr = fmt.Errorf("get pr %s#%d: %w", key, number, err)
			}
			continue
		}
		checked++
		if pr.GetState() == "open" {
			return true, nil
		}
	}
	// No repo said "open". Only trust that as a definitive "closed"
	// when at least one repo actually answered; otherwise surface the
	// failure so the sweep keeps the env and retries next tick.
	if checked == 0 && firstErr != nil {
		return false, firstErr
	}
	return false, nil
}
