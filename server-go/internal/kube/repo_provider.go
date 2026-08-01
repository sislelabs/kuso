package kube

import (
	"net/url"
	"strings"
)

// RepoProvider identifies the VCS host a service's repo lives on.
type RepoProvider string

const (
	// ProviderGitHub — repos on github.com (or a GitHub Enterprise host).
	// Authenticates via the kuso GitHub App installation.
	ProviderGitHub RepoProvider = "github"
	// ProviderGitLab — repos on gitlab.com or a self-hosted GitLab.
	// Authenticates via a per-service stored token (KusoRepoRef.TokenSecret).
	ProviderGitLab RepoProvider = "gitlab"
)

// RepoTokenSecretKey is the key under which a private-repo clone token is
// stored in the Secret named by KusoRepoRef.TokenSecret.
const RepoTokenSecretKey = "TOKEN"

// RepoProviderForRef resolves the provider for a repo ref: an explicit
// spec.provider wins; otherwise it's inferred from the URL host. Defaults
// to GitHub when nothing indicates otherwise, so existing GitHub services
// (which never set provider) are unaffected.
func RepoProviderForRef(ref *KusoRepoRef) RepoProvider {
	if ref == nil {
		return ProviderGitHub
	}
	if p := RepoProvider(strings.ToLower(strings.TrimSpace(ref.Provider))); p == ProviderGitHub || p == ProviderGitLab {
		return p
	}
	return RepoProviderForURL(ref.URL)
}

// RepoProviderForURL infers the provider from a repo URL's host. Recognises
// github.com and gitlab.com (incl. subdomains). A host containing "gitlab"
// (self-hosted, e.g. gitlab.acme.com) is treated as GitLab. Everything else
// — including unknown self-hosted GitHub Enterprise hosts — defaults to
// GitHub, preserving today's behaviour for repos that don't set a provider.
func RepoProviderForURL(raw string) RepoProvider {
	host := repoHost(raw)
	switch {
	case host == "gitlab.com" || strings.HasSuffix(host, ".gitlab.com") || strings.Contains(host, "gitlab"):
		return ProviderGitLab
	default:
		return ProviderGitHub
	}
}

// repoHost extracts the lowercase host from a repo URL. Tolerates missing
// scheme (git@host:… and bare host/owner/repo) by best-effort parsing.
func repoHost(raw string) string {
	raw = strings.TrimSpace(raw)
	if raw == "" {
		return ""
	}
	// scp-like git@host:owner/repo(.git)
	if strings.HasPrefix(raw, "git@") {
		rest := strings.TrimPrefix(raw, "git@")
		if i := strings.IndexByte(rest, ':'); i > 0 {
			return strings.ToLower(rest[:i])
		}
	}
	if !strings.Contains(raw, "://") {
		raw = "https://" + raw
	}
	if u, err := url.Parse(raw); err == nil {
		return strings.ToLower(u.Hostname())
	}
	return ""
}
