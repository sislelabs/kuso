package github

import (
	"context"
	"errors"
	"fmt"
	"net/http"
	"sync"
	"time"

	"github.com/bradleyfalzon/ghinstallation/v2"
	gogithub "github.com/google/go-github/v66/github"
)

// Client owns the App-level transport (signed by the kuso App's PEM)
// and a memoised set of per-installation transports.
//
// The installation transports cache their own tokens (1h TTL refreshed
// transparently by ghinstallation), so callers should NOT build a new
// transport per request. We keep them in transports keyed by
// installationID and never evict — installations are O(10) for any
// realistic kuso instance, and ghinstallation is the right layer to
// invalidate from anyway.
type Client struct {
	cfg *Config

	appTransport http.RoundTripper

	mu         sync.Mutex
	transports map[int64]http.RoundTripper
}

// NewClient returns a *Client for the given config. Returns (nil, nil)
// when cfg is not configured — callers should check.
func NewClient(cfg *Config) (*Client, error) {
	if !cfg.IsConfigured() {
		return nil, nil
	}
	app, err := ghinstallation.NewAppsTransport(http.DefaultTransport, cfg.AppID, cfg.PrivateKey)
	if err != nil {
		return nil, fmt.Errorf("github: app transport: %w", err)
	}
	return &Client{
		cfg:          cfg,
		appTransport: app,
		transports:   map[int64]http.RoundTripper{},
	}, nil
}

// App returns a go-github client signed with the App JWT. Use for
// App-level calls only (e.g. ListInstallations).
func (c *Client) App() *gogithub.Client {
	return gogithub.NewClient(&http.Client{Transport: c.appTransport})
}

// Installation returns a go-github client whose transport carries an
// installation token for installationID (refreshed automatically).
func (c *Client) Installation(installationID int64) (*gogithub.Client, error) {
	c.mu.Lock()
	defer c.mu.Unlock()
	if t, ok := c.transports[installationID]; ok {
		return gogithub.NewClient(&http.Client{Transport: t}), nil
	}
	itr, err := ghinstallation.New(http.DefaultTransport, c.cfg.AppID, installationID, c.cfg.PrivateKey)
	if err != nil {
		return nil, fmt.Errorf("github: installation transport: %w", err)
	}
	c.transports[installationID] = itr
	return gogithub.NewClient(&http.Client{Transport: itr}), nil
}

// MintInstallationToken returns a fresh installation token for the given
// id. Used by the build path to seed the kaniko clone Secret.
func (c *Client) MintInstallationToken(ctx context.Context, installationID int64) (string, error) {
	itr, err := c.installationTransport(installationID)
	if err != nil {
		return "", err
	}
	tok, err := itr.Token(ctx)
	if err != nil {
		return "", fmt.Errorf("github: mint token: %w", err)
	}
	return tok, nil
}

// installationTransport returns the cached *ghinstallation.Transport for
// installationID (constructing it on first use).
func (c *Client) installationTransport(installationID int64) (*ghinstallation.Transport, error) {
	c.mu.Lock()
	defer c.mu.Unlock()
	if t, ok := c.transports[installationID]; ok {
		if itr, ok := t.(*ghinstallation.Transport); ok {
			return itr, nil
		}
		return nil, errors.New("github: cached transport is not *ghinstallation.Transport")
	}
	itr, err := ghinstallation.New(http.DefaultTransport, c.cfg.AppID, installationID, c.cfg.PrivateKey)
	if err != nil {
		return nil, fmt.Errorf("github: installation transport: %w", err)
	}
	c.transports[installationID] = itr
	return itr, nil
}

// ResolveBranchSHA returns the head commit SHA for owner/repo at the
// given branch via the installation client. Empty string + no error
// means the branch was not found.
func (c *Client) ResolveBranchSHA(ctx context.Context, installationID int64, owner, repo, branch string) (string, error) {
	cli, err := c.Installation(installationID)
	if err != nil {
		return "", err
	}
	br, _, err := cli.Repositories.GetBranch(ctx, owner, repo, branch, 1)
	if err != nil {
		return "", fmt.Errorf("github: get branch: %w", err)
	}
	if br == nil || br.Commit == nil || br.Commit.SHA == nil {
		return "", nil
	}
	return *br.Commit.SHA, nil
}

// CachedInstallation is the wire shape returned to /api/github/installations.
type CachedInstallation struct {
	ID           int64     `json:"id"`
	AccountLogin string    `json:"accountLogin"`
	AccountType  string    `json:"accountType"`
	AccountID    int64     `json:"accountId"`
	UpdatedAt    time.Time `json:"updatedAt"`
}

// CachedRepo is the wire shape stored in GithubInstallation.repositoriesJson.
type CachedRepo struct {
	ID            int64  `json:"id"`
	Name          string `json:"name"`
	FullName      string `json:"fullName"`
	Private       bool   `json:"private"`
	DefaultBranch string `json:"defaultBranch"`
}

// FetchInstallations enumerates every installation visible to the App.
// Used by the install-callback flow + a manual "refresh" admin action.
func (c *Client) FetchInstallations(ctx context.Context) ([]CachedInstallation, error) {
	cli := c.App()
	out := []CachedInstallation{}
	page := 1
	for {
		insts, resp, err := cli.Apps.ListInstallations(ctx, &gogithub.ListOptions{Page: page, PerPage: 100})
		if err != nil {
			return nil, fmt.Errorf("github: list installations: %w", err)
		}
		for _, ins := range insts {
			if ins.GetID() == 0 {
				continue
			}
			out = append(out, CachedInstallation{
				ID:           ins.GetID(),
				AccountLogin: ins.GetAccount().GetLogin(),
				AccountType:  ins.GetAccount().GetType(),
				AccountID:    ins.GetAccount().GetID(),
				UpdatedAt:    time.Now(),
			})
		}
		if resp.NextPage == 0 {
			return out, nil
		}
		page = resp.NextPage
	}
}

// FetchInstallationRepos pages through every repo accessible to the
// installation.
func (c *Client) FetchInstallationRepos(ctx context.Context, installationID int64) ([]CachedRepo, error) {
	cli, err := c.Installation(installationID)
	if err != nil {
		return nil, err
	}
	out := []CachedRepo{}
	page := 1
	for {
		repos, resp, err := cli.Apps.ListRepos(ctx, &gogithub.ListOptions{Page: page, PerPage: 100})
		if err != nil {
			return nil, fmt.Errorf("github: list repos: %w", err)
		}
		for _, r := range repos.Repositories {
			out = append(out, CachedRepo{
				ID:            r.GetID(),
				Name:          r.GetName(),
				FullName:      r.GetFullName(),
				Private:       r.GetPrivate(),
				DefaultBranch: r.GetDefaultBranch(),
			})
		}
		if resp.NextPage == 0 {
			return out, nil
		}
		page = resp.NextPage
	}
}
