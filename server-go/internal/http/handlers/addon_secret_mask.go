package handlers

import (
	"net/url"
	"regexp"
	"sort"
	"strings"
)

// connPasswordMask replaces the password in a redacted connection URL.
const connPasswordMask = "••••••••"

// nonSensitiveConnKeyRe names conn-secret keys whose values are connection
// coordinates, not credentials. Anything not matching is masked.
var nonSensitiveConnKeyRe = regexp.MustCompile(`(?i)(^|_)(HOST|HOSTNAME|PORT|DATABASE|DB|USER|USERNAME|REGION|BUCKET|SSLMODE)$`)

// urlUserinfoPasswordRe is the fallback detector for `scheme://user:pass@`
// values url.Parse rejects (e.g. multi-host mongodb URIs).
var urlUserinfoPasswordRe = regexp.MustCompile(`^[A-Za-z][A-Za-z0-9+.-]*://[^/?#@]*:[^@]*@`)

// connQuerySecretRe names query parameters that may carry credentials
// (sslpassword=, token=, …); their values are masked in the display URL.
var connQuerySecretRe = regexp.MustCompile(`(?i)pass|secret|token|key`)

// hasURLPassword reports whether v looks like a URL carrying a password in
// its userinfo.
func hasURLPassword(v string) bool {
	if u, err := url.Parse(v); err == nil && u.User != nil {
		if _, ok := u.User.Password(); ok {
			return true
		}
	}
	return urlUserinfoPasswordRe.MatchString(v)
}

// connKeySensitive reports whether the conn-secret entry must be masked.
func connKeySensitive(key, value string) bool {
	return !nonSensitiveConnKeyRe.MatchString(key) || hasURLPassword(value)
}

// redactConnURL returns v with the userinfo password (and any credential-
// looking query values) replaced by connPasswordMask, or "" when v isn't a
// URL with a password we can redact structurally.
func redactConnURL(v string) string {
	u, err := url.Parse(v)
	if err != nil || u.Scheme == "" || u.Host == "" || u.User == nil {
		return ""
	}
	if _, ok := u.User.Password(); !ok {
		return ""
	}
	if u.RawQuery != "" {
		q := u.Query()
		redacted := false
		for k, vs := range q {
			if connQuerySecretRe.MatchString(k) {
				redacted = true
				for i := range vs {
					vs[i] = connPasswordMask
				}
			}
		}
		if redacted {
			u.RawQuery = strings.ReplaceAll(q.Encode(), url.QueryEscape(connPasswordMask), connPasswordMask)
		}
	}
	user := url.User(u.User.Username()).String()
	u.User = nil
	prefix := u.Scheme + "://"
	return prefix + user + ":" + connPasswordMask + "@" + strings.TrimPrefix(u.String(), prefix)
}

// maskConnValues returns the display map for the masked secret view plus
// the sorted keys whose (non-empty) values were masked. Empty values stay
// "" and are not reported as masked — they are simply unset.
func maskConnValues(values map[string]string) (map[string]string, []string) {
	out := make(map[string]string, len(values))
	masked := []string{}
	for k, v := range values {
		switch {
		case v == "":
			out[k] = ""
		case connKeySensitive(k, v):
			out[k] = redactConnURL(v)
			masked = append(masked, k)
		default:
			out[k] = v
		}
	}
	sort.Strings(masked)
	return out, masked
}
