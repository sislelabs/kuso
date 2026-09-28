package main

import (
	"context"
	"errors"
	"sync/atomic"

	"kuso/server/internal/builds"
	ghpkg "kuso/server/internal/github"
	"kuso/server/internal/notify"
)

// githubBuildBridge satisfies builds.CommitStatusReporter and
// builds.CIChecker over the GitHub App client, keeping builds/ free of a
// github import (same layering as notifyAdapter).
//
// Late-bound: startSingletons (and so the build poller) can start before
// the GitHub config block runs, so the client sits in an atomic pointer
// and the bridge reports unavailable until set is called.
type githubBuildBridge struct {
	cli atomic.Pointer[ghpkg.Client]
	ci  atomic.Pointer[ghpkg.CachedCIChecker]
}

func (g *githubBuildBridge) set(c *ghpkg.Client) {
	if c == nil {
		return
	}
	g.ci.Store(ghpkg.NewCachedCIChecker(c))
	g.cli.Store(c)
}

func (g *githubBuildBridge) Available() bool { return g.cli.Load() != nil }

// PostCommitStatus is a no-op without a GitHub App: reporting success
// lets the poller record the state instead of retrying forever.
func (g *githubBuildBridge) PostCommitStatus(ctx context.Context, s builds.CommitStatus) error {
	c := g.cli.Load()
	if c == nil {
		return nil
	}
	return c.PostCommitStatus(ctx, s.InstallationID, s.Owner, s.Repo, s.SHA, ghpkg.CommitStatusInput{
		State:       s.State,
		Context:     s.Context,
		Description: s.Description,
		TargetURL:   notify.AbsoluteURL(s.TargetPath),
	})
}

func (g *githubBuildBridge) CheckCI(ctx context.Context, installationID int64, owner, repo, sha string) (builds.CIVerdict, error) {
	ci := g.ci.Load()
	if ci == nil {
		return builds.CIVerdict{}, errors.New("github app not configured")
	}
	v, err := ci.Check(ctx, installationID, owner, repo, sha)
	if err != nil {
		return builds.CIVerdict{}, err
	}
	return builds.CIVerdict{State: builds.CIState(v.State), Failed: v.Failed, NoChecks: v.NoChecks}, nil
}
