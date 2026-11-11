package handlers

import (
	"context"
	"fmt"
	"net/url"
	"strings"

	"kuso/server/internal/github"
)

// GithubRepoFiles adapts the GitHub App client to crons.RepoFileFetcher.
type GithubRepoFiles struct {
	Client *github.Client
}

func (g GithubRepoFiles) GetRepoFile(ctx context.Context, installationID int64, repoURL, ref, filePath string) ([]byte, bool, error) {
	owner, repo := github.ParseGithubRepoURL(repoURL)
	if owner == "" || repo == "" {
		return nil, false, fmt.Errorf("not a github.com repo: %q", repoURL)
	}
	return g.Client.GetFile(ctx, installationID, owner, repo, ref, filePath)
}

func (g GithubRepoFiles) BlobURL(repoURL, ref, filePath string) string {
	owner, repo := github.ParseGithubRepoURL(repoURL)
	if owner == "" || repo == "" {
		return ""
	}
	segs := strings.Split(filePath, "/")
	for i := range segs {
		segs[i] = url.PathEscape(segs[i])
	}
	return fmt.Sprintf("https://github.com/%s/%s/blob/%s/%s", owner, repo, url.PathEscape(ref), strings.Join(segs, "/"))
}
