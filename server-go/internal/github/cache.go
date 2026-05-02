package github

import (
	"context"
	"fmt"

	"kuso/server/internal/db"
)

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
