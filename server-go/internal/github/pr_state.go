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
//     limit) → recorded; unless some repo answered "open", the first
//     such error is returned so the caller keeps the env and retries,
//     instead of treating "couldn't ask" as "closed". One repo's 404
//     says nothing about the repo that errored — the PR may live
//     exactly there — so ANY unanswered repo poisons a "closed"
//     verdict, not just all of them failing.
func (c *Client) PROpenInAny(ctx context.Context, store CacheStore, repoURLs []string, number int) (bool, error) {
	if c == nil {
		return false, errors.New("github client not configured")
	}
	if number <= 0 {
		return false, fmt.Errorf("github: invalid pr number %d", number)
	}
	seen := map[string]bool{}
	var firstErr error
	answered, skipped := 0, 0
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
		if instID == 0 {
			// No App installation covers this repo (deploy-token/PAT
			// repo, or the App was uninstalled). Skip it WITHOUT
			// recording an error: previews are only ever created from
			// App-webhook events, so an App-less repo can't be the
			// PR's home — and treating it as an error would defer
			// every preview delete in mixed App+PAT projects to the
			// 14-day grace path. (Installation(0) would fail with a
			// non-404 error and poison firstErr forever.)
			skipped++
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
				answered++
				continue // definitive: this repo has no such PR
			}
			if firstErr == nil {
				firstErr = fmt.Errorf("get pr %s#%d: %w", key, number, err)
			}
			continue
		}
		answered++
		if pr.GetState() == "open" {
			return true, nil
		}
	}
	// No repo said "open". Trust that as a definitive "closed" only
	// when EVERY repo actually answered. A partial sweep (repo A
	// errored, repo B said 404) proves nothing about A — and A may be
	// exactly where the PR lives, so returning (false, nil) here would
	// tear down a live preview on a transient A-side failure. Surface
	// the error instead; the sweep keeps the env and retries next tick.
	if firstErr != nil {
		return false, firstErr
	}
	// EVERY candidate repo was skipped for lacking an App installation
	// and none answered. For a preview that EXISTS (they're only ever
	// created by App webhooks) that isn't proof of "closed" — it's a
	// transiently empty/lost installations cache, or the App was just
	// uninstalled. "Couldn't ask" must not silently become "closed";
	// surface it and let the sweep's grace cap bound the retention.
	if answered == 0 && skipped > 0 {
		return false, fmt.Errorf("github: no App installation covers any of the %d candidate repo(s) for pr #%d — cannot verify PR state", skipped, number)
	}
	return false, nil
}
