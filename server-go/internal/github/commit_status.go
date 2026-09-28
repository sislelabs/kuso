// Commit statuses (write) and CI state (read) for the build pipeline.
//
// Posting needs the App permission `statuses: write`; reading check runs
// needs `checks: read` (combined statuses are covered by statuses).
// Installations created before these were added to the manifest must
// accept the new permissions on GitHub before either works — until then
// GitHub answers 403 and callers log + back off.
package github

import (
	"context"
	"errors"
	"fmt"
	"strings"
	"sync"
	"time"

	gogithub "github.com/google/go-github/v66/github"
)

// kusoContextPrefix mirrors builds.CommitStatusContextPrefix (the github
// package can't import builds). Statuses under it are kuso's own and never
// count toward the CI verdict.
const kusoContextPrefix = "kuso/"

// CI verdict states.
const (
	CIStatePending = "pending"
	CIStateSuccess = "success"
	CIStateFailure = "failure"
)

// CommitStatusInput is one status to post on a commit.
type CommitStatusInput struct {
	State       string // pending | success | failure | error
	Context     string
	Description string
	TargetURL   string // absolute; omitted when empty
}

// CIVerdict aggregates a commit's check runs + commit statuses.
type CIVerdict struct {
	State    string
	Failed   string // first failing check run name / status context
	NoChecks bool   // nothing (besides kuso's own statuses) reported yet
}

// installationAPI returns the installation-scoped go-github client,
// honouring the test base-URL override.
func (c *Client) installationAPI(installationID int64) (*gogithub.Client, error) {
	cli, err := c.Installation(installationID)
	if err != nil {
		return nil, err
	}
	if c.baseURL != "" {
		if ec, err := cli.WithEnterpriseURLs(c.baseURL, c.baseURL); err == nil {
			cli = ec
		}
	}
	return cli, nil
}

// PostCommitStatus creates a commit status on owner/repo@sha.
func (c *Client) PostCommitStatus(ctx context.Context, installationID int64, owner, repo, sha string, in CommitStatusInput) error {
	if c == nil {
		return errors.New("github client not configured")
	}
	gh, err := c.installationAPI(installationID)
	if err != nil {
		return err
	}
	return postCommitStatus(ctx, gh, owner, repo, sha, in)
}

// CommitCIVerdict reads the CI state of owner/repo@sha.
func (c *Client) CommitCIVerdict(ctx context.Context, installationID int64, owner, repo, sha string) (CIVerdict, error) {
	if c == nil {
		return CIVerdict{}, errors.New("github client not configured")
	}
	gh, err := c.installationAPI(installationID)
	if err != nil {
		return CIVerdict{}, err
	}
	return fetchCIVerdict(ctx, gh, owner, repo, sha)
}

func postCommitStatus(ctx context.Context, gh *gogithub.Client, owner, repo, sha string, in CommitStatusInput) error {
	st := &gogithub.RepoStatus{
		State:   gogithub.String(in.State),
		Context: gogithub.String(in.Context),
	}
	if in.Description != "" {
		st.Description = gogithub.String(in.Description)
	}
	if in.TargetURL != "" {
		st.TargetURL = gogithub.String(in.TargetURL)
	}
	if _, _, err := gh.Repositories.CreateStatus(ctx, owner, repo, sha, st); err != nil {
		return fmt.Errorf("github: create status %s on %s/%s@%.7s: %w", in.Context, owner, repo, sha, err)
	}
	return nil
}

// checkRunPages caps check-run pagination; 500 runs on one commit is
// already far past anything real.
const checkRunPages = 5

func fetchCIVerdict(ctx context.Context, gh *gogithub.Client, owner, repo, sha string) (CIVerdict, error) {
	seen := 0
	pending := false
	opts := &gogithub.ListCheckRunsOptions{
		Filter:      gogithub.String("latest"),
		ListOptions: gogithub.ListOptions{PerPage: 100},
	}
	for page := 0; page < checkRunPages; page++ {
		res, resp, err := gh.Checks.ListCheckRunsForRef(ctx, owner, repo, sha, opts)
		if err != nil {
			return CIVerdict{}, fmt.Errorf("github: list check runs for %s/%s@%.7s: %w", owner, repo, sha, err)
		}
		for _, run := range res.CheckRuns {
			seen++
			if run.GetStatus() != "completed" {
				pending = true
				continue
			}
			switch run.GetConclusion() {
			case "success", "neutral", "skipped":
			default: // failure, timed_out, cancelled, action_required, startup_failure, stale
				return CIVerdict{State: CIStateFailure, Failed: run.GetName()}, nil
			}
		}
		if resp == nil || resp.NextPage == 0 {
			break
		}
		opts.Page = resp.NextPage
	}

	combined, _, err := gh.Repositories.GetCombinedStatus(ctx, owner, repo, sha, &gogithub.ListOptions{PerPage: 100})
	if err != nil {
		return CIVerdict{}, fmt.Errorf("github: combined status for %s/%s@%.7s: %w", owner, repo, sha, err)
	}
	// The combined state includes kuso's own statuses, so re-aggregate
	// from the per-context list (latest status per context).
	for _, st := range combined.Statuses {
		if strings.HasPrefix(st.GetContext(), kusoContextPrefix) {
			continue
		}
		seen++
		switch st.GetState() {
		case "success":
		case "pending":
			pending = true
		default: // failure, error
			return CIVerdict{State: CIStateFailure, Failed: st.GetContext()}, nil
		}
	}
	switch {
	case seen == 0:
		return CIVerdict{State: CIStatePending, NoChecks: true}, nil
	case pending:
		return CIVerdict{State: CIStatePending}, nil
	}
	return CIVerdict{State: CIStateSuccess}, nil
}

// CIVerdictSource is what CachedCIChecker wraps (*Client in production).
type CIVerdictSource interface {
	CommitCIVerdict(ctx context.Context, installationID int64, owner, repo, sha string) (CIVerdict, error)
}

// ErrCIBackoff is returned while a commit's CI lookup is backing off
// after an error.
var ErrCIBackoff = errors.New("github: ci lookup backing off")

// CachedCIChecker caches verdicts per commit so every service built from
// one push (monorepo) and every 5s poller tick share a lookup, and backs
// off on errors, honouring GitHub's rate-limit reset.
type CachedCIChecker struct {
	src        CIVerdictSource
	pendingTTL time.Duration
	finalTTL   time.Duration
	now        func() time.Time

	mu      sync.Mutex
	entries map[string]*ciCacheEntry
}

type ciCacheEntry struct {
	verdict   CIVerdict
	fetchedAt time.Time
	lastUsed  time.Time
	retryAt   time.Time
	backoff   time.Duration
	err       error
}

const (
	ciBackoffMin = 15 * time.Second
	ciBackoffMax = 5 * time.Minute
	ciEntryIdle  = time.Hour
)

// NewCachedCIChecker wraps src with a 15s cache for pending verdicts (the
// gate's effective poll interval) and 10min for final ones.
func NewCachedCIChecker(src CIVerdictSource) *CachedCIChecker {
	return &CachedCIChecker{
		src:        src,
		pendingTTL: 15 * time.Second,
		finalTTL:   10 * time.Minute,
		now:        time.Now,
		entries:    map[string]*ciCacheEntry{},
	}
}

// Check returns the (possibly cached) CI verdict for owner/repo@sha.
func (c *CachedCIChecker) Check(ctx context.Context, installationID int64, owner, repo, sha string) (CIVerdict, error) {
	key := strings.ToLower(owner + "/" + repo + "@" + sha)
	now := c.now()
	c.mu.Lock()
	for k, e := range c.entries {
		if now.Sub(e.lastUsed) > ciEntryIdle {
			delete(c.entries, k)
		}
	}
	e := c.entries[key]
	if e == nil {
		e = &ciCacheEntry{}
		c.entries[key] = e
	}
	e.lastUsed = now
	if now.Before(e.retryAt) {
		err := e.err
		c.mu.Unlock()
		return CIVerdict{}, fmt.Errorf("%w: %v", ErrCIBackoff, err)
	}
	if !e.fetchedAt.IsZero() {
		ttl := c.pendingTTL
		if e.verdict.State != CIStatePending {
			ttl = c.finalTTL
		}
		if now.Sub(e.fetchedAt) < ttl {
			v := e.verdict
			c.mu.Unlock()
			return v, nil
		}
	}
	c.mu.Unlock()

	v, err := c.src.CommitCIVerdict(ctx, installationID, owner, repo, sha)

	c.mu.Lock()
	defer c.mu.Unlock()
	if err != nil {
		e.err = err
		e.backoff = min(max(e.backoff*2, ciBackoffMin), ciBackoffMax)
		e.retryAt = now.Add(e.backoff)
		if reset := rateLimitReset(err); reset.After(e.retryAt) {
			e.retryAt = reset
		}
		return CIVerdict{}, err
	}
	e.err, e.backoff, e.retryAt = nil, 0, time.Time{}
	e.verdict, e.fetchedAt = v, now
	return v, nil
}

// rateLimitReset extracts when GitHub will accept requests again.
func rateLimitReset(err error) time.Time {
	var rl *gogithub.RateLimitError
	if errors.As(err, &rl) {
		return rl.Rate.Reset.Time
	}
	var abuse *gogithub.AbuseRateLimitError
	if errors.As(err, &abuse) {
		if abuse.RetryAfter != nil {
			return time.Now().Add(*abuse.RetryAfter)
		}
	}
	return time.Time{}
}
