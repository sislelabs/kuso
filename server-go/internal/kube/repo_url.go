package kube

import "regexp"

// repoUserinfoRe matches an optional scheme followed by a userinfo@
// segment: https://user:token@host/… or bare user:token@host/….
var repoUserinfoRe = regexp.MustCompile(`^([a-z][a-z0-9+.-]*://)?([^/@]+)@(.+)$`)

// StripRepoURLCredentials removes embedded credentials (userinfo) from
// a git clone URL. Users store deploy-token URLs like
//
//	https://kuso-deploy:gldt-xxxx@gitlab.com/org/repo.git
//
// so the builder can clone private repos — but the userinfo is a
// working credential and must never reach a caller without
// secrets:read. scp-style SSH (git@host:org/repo — no scheme, no ":"
// in the userinfo) is left alone: its userinfo is a username, not a
// secret. Mirrors web/src/lib/format.ts stripRepoCredentials.
func StripRepoURLCredentials(raw string) string {
	if raw == "" {
		return ""
	}
	m := repoUserinfoRe.FindStringSubmatch(raw)
	if m == nil {
		return raw
	}
	if m[1] == "" && !containsRune(m[2], ':') {
		return raw // scp-style git@host:path — username only, keep
	}
	return m[1] + m[3]
}

// RepoURLHasCredentials reports whether the URL embeds credentials
// that StripRepoURLCredentials would remove.
func RepoURLHasCredentials(raw string) bool {
	return StripRepoURLCredentials(raw) != raw
}

func containsRune(s string, r byte) bool {
	for i := 0; i < len(s); i++ {
		if s[i] == r {
			return true
		}
	}
	return false
}
