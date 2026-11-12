package kube

import (
	"regexp"
	"strings"
)

// repoUserinfoRe matches an optional scheme followed by a userinfo@
// segment: https://user:token@host/… or bare user:token@host/….
// (?i): git accepts case-insensitive schemes, and a case-SENSITIVE
// match let HTTPS://user:tok@… sail through every "redacted" read
// path verbatim. Userinfo is [^/]+ (not [^/@]+) so a password
// containing @ (user:p@ss@host) strips to the LAST @ before the path
// instead of leaving the credential tail in place.
var repoUserinfoRe = regexp.MustCompile(`(?i)^([a-z][a-z0-9+.-]*://)?([^/]+)@(.+)$`)

// scpPathRe recognises the rest-after-@ of scp-style SSH
// (git@host:org/repo): a host with a colon before any slash.
var scpPathRe = regexp.MustCompile(`^[^/:]+:`)

// StripRepoURLCredentials removes embedded credentials (userinfo) from
// a git clone URL. Users store deploy-token URLs like
//
//	https://kuso-deploy:gldt-xxxx@gitlab.com/org/repo.git
//
// so the builder can clone private repos — but the userinfo is a
// working credential and must never reach a caller without
// secrets:read.
//
// Colon-free userinfo is kept only under the SSH family — ssh://
// schemes and scp-style git@host:path — where it's a plain username.
// Under http(s) (or a schemeless slash-path form) a colon-free
// userinfo is a bare token (https://TOKEN@github.com/… is GitHub's
// documented PAT clone shape), so it strips like any other credential.
// Mirrors web/src/lib/format.ts stripRepoCredentials.
func StripRepoURLCredentials(raw string) string {
	if raw == "" {
		return ""
	}
	// Stray whitespace from a paste must not defeat the anchored
	// match — " https://user:tok@host/…" is a working clone URL to
	// git but was invisible to the redactor.
	m := repoUserinfoRe.FindStringSubmatch(strings.TrimSpace(raw))
	if m == nil {
		return raw
	}
	scheme, userinfo, rest := m[1], m[2], m[3]
	if !strings.Contains(userinfo, ":") {
		switch strings.ToLower(scheme) {
		case "ssh://", "git+ssh://", "git://":
			return raw // username, not a secret
		case "":
			if scpPathRe.MatchString(rest) {
				return raw // scp-style git@host:path — username only
			}
		}
	}
	return scheme + rest
}

// RepoURLHasCredentials reports whether the URL embeds credentials
// that StripRepoURLCredentials would remove.
func RepoURLHasCredentials(raw string) bool {
	return StripRepoURLCredentials(raw) != raw
}

// RepoURLEchoesRedacted reports whether incoming is a redacted echo of
// stored: equal to stored minus its userinfo, under EITHER the current
// redaction (StripRepoURLCredentials) or the blanket strip-any-userinfo
// form older releases produced (they also stripped SSH usernames).
// Round-trip preservation must accept both — a client still holding a
// pre-upgrade redacted URL (React Query cache, older CLI) that saves
// it back would otherwise clobber the stored userinfo, e.g. rewriting
// ssh://git@host/r to the uncloneable ssh://host/r. Not for display —
// display always uses StripRepoURLCredentials.
func RepoURLEchoesRedacted(stored, incoming string) bool {
	// Whitespace-tolerant: a redacted echo that picked up (or lost) a
	// stray space is still an echo — clobbering stored creds over
	// invisible whitespace is the failure mode this exists to stop.
	stored, incoming = strings.TrimSpace(stored), strings.TrimSpace(incoming)
	if incoming == "" || incoming == stored {
		return false // nothing to preserve / plain overwrite-with-same
	}
	if StripRepoURLCredentials(stored) == incoming {
		return true
	}
	m := repoUserinfoRe.FindStringSubmatch(stored)
	return m != nil && m[1]+m[3] == incoming
}

// RepoIdentity reduces a git remote to a comparable host/path key
// ("github.com/acme/web"): scheme, credentials, a trailing ".git" or "/"
// and case are dropped, and scp-style git@host:org/repo becomes
// host/org/repo. Two URLs with the same identity clone the same repo.
// Returns "" for an empty URL.
func RepoIdentity(raw string) string {
	s := strings.ToLower(strings.TrimSpace(raw))
	if s == "" {
		return ""
	}
	s = strings.TrimPrefix(s, "git+")
	if i := strings.Index(s, "://"); i >= 0 {
		s = s[i+3:]
	}
	head := s
	if i := strings.IndexByte(s, '/'); i >= 0 {
		head = s[:i]
	}
	if at := strings.LastIndexByte(head, '@'); at >= 0 {
		s = s[at+1:]
		head = head[at+1:]
	}
	// scp-style host:org/repo; host:8443/org/repo is a port and stays.
	if host, rest, ok := strings.Cut(head, ":"); ok && !isAllDigits(rest) {
		s = host + "/" + strings.TrimPrefix(s[len(host)+1:], "/")
	}
	s = strings.TrimPrefix(s, "www.")
	s = strings.TrimRight(s, "/")
	s = strings.TrimSuffix(s, ".git")
	return strings.TrimRight(s, "/")
}

func isAllDigits(s string) bool {
	if s == "" {
		return false
	}
	for _, c := range s {
		if c < '0' || c > '9' {
			return false
		}
	}
	return true
}
