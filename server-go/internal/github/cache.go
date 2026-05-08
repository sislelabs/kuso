package github

import (
	"context"
	"fmt"
	"strings"

	"kuso/server/internal/db"
)

// ParseGithubRepoURL extracts owner + repo from a GitHub URL. Accepts
// the shapes the user types into the AddService dialog:
//
//   https://github.com/owner/repo
//   https://github.com/owner/repo.git
//   git@github.com:owner/repo.git
//   github.com/owner/repo
//
// Returns ("", "") when the URL is not on github.com or the path
// doesn't have at least two segments. Strips the trailing ".git"
// suffix because the API wants the bare repo name.
func ParseGithubRepoURL(raw string) (owner, repo string) {
	s := strings.TrimSpace(raw)
	if s == "" {
		return "", ""
	}
	// SSH form
	if strings.HasPrefix(s, "git@github.com:") {
		s = strings.TrimPrefix(s, "git@github.com:")
	} else {
		// Strip scheme.
		s = strings.TrimPrefix(s, "https://")
		s = strings.TrimPrefix(s, "http://")
		s = strings.TrimPrefix(s, "git+")
		// Only github.com URLs auto-resolve. Self-hosted Enterprise
		// installs would need their own host suffix here.
		if !strings.HasPrefix(s, "github.com/") && !strings.HasPrefix(s, "www.github.com/") {
			return "", ""
		}
		s = strings.TrimPrefix(s, "www.")
		s = strings.TrimPrefix(s, "github.com/")
	}
	s = strings.TrimSuffix(s, ".git")
	parts := strings.Split(s, "/")
	if len(parts) < 2 || parts[0] == "" || parts[1] == "" {
		return "", ""
	}
	return parts[0], parts[1]
}

// CacheStore lets the dispatcher + admin endpoints persist installation
// + repo metadata. Backed by *db.DB in production; an interface so tests
// can swap a memory impl.
type CacheStore interface {
	Upsert(ctx context.Context, in db.GithubInstallation) error
	SetRepos(ctx context.Context, id int64, repos []db.GithubRepo) error
	List(ctx context.Context) ([]db.GithubInstallation, error)
	Repos(ctx context.Context, id int64) ([]db.GithubRepo, error)
	Delete(ctx context.Context, id int64) error
}

// ResolveInstallationForRepo finds the cached installation that has
// access to <owner>/<name>. Used by the build trigger path to
// auto-bind a service whose project hasn't been pinned to a
// specific installation — without this, multi-org users had to
// manually plumb installation IDs through API calls.
//
// Lookup order:
//
//  1. Exact repo match — an installation that lists "<owner>/<name>"
//     in its cached repos. Wins because it's authoritative: the App
//     can read the repo today.
//  2. Owner match — fall back to any installation whose
//     accountLogin == owner (case-insensitive). Covers the freshly-
//     installed window where the repo cache hasn't been populated
//     yet for that installation.
//
// Returns 0 + nil when no installation matches; the caller falls
// through to unauth clone (which works for public repos).
//
// Best-effort: a DB error from the cache is treated as "no match"
// (returns 0 + the error so the caller can log) — we don't want a
// transient store hiccup to wedge the build path.
func ResolveInstallationForRepo(ctx context.Context, store CacheStore, owner, name string) (int64, error) {
	if store == nil || owner == "" {
		return 0, nil
	}
	insts, err := store.List(ctx)
	if err != nil {
		return 0, err
	}
	want := strings.ToLower(owner + "/" + name)
	wantOwner := strings.ToLower(owner)
	var byOwner int64
	for _, ins := range insts {
		if strings.EqualFold(ins.AccountLogin, owner) && byOwner == 0 {
			byOwner = ins.ID
		}
		if name == "" {
			continue
		}
		repos, rerr := store.Repos(ctx, ins.ID)
		if rerr != nil {
			continue
		}
		for _, r := range repos {
			if strings.ToLower(r.FullName) == want {
				return ins.ID, nil
			}
		}
	}
	if byOwner != 0 {
		return byOwner, nil
	}
	_ = wantOwner // reserved if we add fuzzy matching later
	return 0, nil
}

// dbCache adapts *db.DB to the CacheStore interface.
type dbCache struct{ DB *db.DB }

// NewDBCache returns a CacheStore backed by SQLite.
func NewDBCache(d *db.DB) CacheStore { return &dbCache{DB: d} }

func (c *dbCache) Upsert(ctx context.Context, in db.GithubInstallation) error {
	return c.DB.UpsertGithubInstallation(ctx, in)
}
func (c *dbCache) SetRepos(ctx context.Context, id int64, repos []db.GithubRepo) error {
	return c.DB.SetGithubInstallationRepos(ctx, id, repos)
}
func (c *dbCache) List(ctx context.Context) ([]db.GithubInstallation, error) {
	return c.DB.ListGithubInstallations(ctx)
}
func (c *dbCache) Repos(ctx context.Context, id int64) ([]db.GithubRepo, error) {
	return c.DB.GithubInstallationRepos(ctx, id)
}
func (c *dbCache) Delete(ctx context.Context, id int64) error {
	return c.DB.DeleteGithubInstallation(ctx, id)
}

// RefreshInstallations pulls the App's current installation list and
// every installation's repo list from GitHub, then writes them through
// to the cache. Idempotent.
func (c *Client) RefreshInstallations(ctx context.Context, store CacheStore) error {
	if c == nil || store == nil {
		return nil
	}
	insts, err := c.FetchInstallations(ctx)
	if err != nil {
		return fmt.Errorf("github: refresh installations: %w", err)
	}
	for _, ins := range insts {
		if err := store.Upsert(ctx, db.GithubInstallation{
			ID:           ins.ID,
			AccountLogin: ins.AccountLogin,
			AccountType:  ins.AccountType,
			AccountID:    ins.AccountID,
		}); err != nil {
			return fmt.Errorf("github: cache upsert %d: %w", ins.ID, err)
		}
		if err := c.RefreshInstallationRepos(ctx, store, ins.ID); err != nil {
			return fmt.Errorf("github: cache repos %d: %w", ins.ID, err)
		}
	}
	return nil
}

// RefreshInstallationRepos pulls a single installation's repo list and
// stores it. Used both by RefreshInstallations and the
// installation_repositories webhook handler.
func (c *Client) RefreshInstallationRepos(ctx context.Context, store CacheStore, id int64) error {
	repos, err := c.FetchInstallationRepos(ctx, id)
	if err != nil {
		return err
	}
	dbRepos := make([]db.GithubRepo, len(repos))
	for i, r := range repos {
		dbRepos[i] = db.GithubRepo{
			ID: r.ID, Name: r.Name, FullName: r.FullName, Private: r.Private, DefaultBranch: r.DefaultBranch,
		}
	}
	return store.SetRepos(ctx, id, dbRepos)
}
