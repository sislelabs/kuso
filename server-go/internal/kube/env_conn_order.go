package kube

import "strings"

// IsEnvScopedConn reports whether secretName is the conn secret of an addon
// clone minted for envScope. Clones follow "<base>-<scope>-conn" for named
// envs (staging, qa) and "<base>-pr-<N>-conn" for previews, whose scope is
// "preview-pr-<N>" — the name and the scope deliberately differ there.
func IsEnvScopedConn(secretName, envScope string) bool {
	if envScope == "" || envScope == "production" || !strings.HasSuffix(secretName, "-conn") {
		return false
	}
	if strings.HasSuffix(secretName, "-"+envScope+"-conn") {
		return true
	}
	if short := strings.TrimPrefix(envScope, "preview-"); short != envScope {
		return strings.HasSuffix(secretName, "-"+short+"-conn")
	}
	return false
}

// CloneConnsLast reorders an env's envFromSecrets so the env's OWN addon
// clones come after everything else, preserving relative order within each
// group.
//
// envFrom is last-source-wins on duplicate keys, and every addon of a kind
// publishes the same keys (DATABASE_URL, DIRECT_URL, POSTGRES_*). An env that
// mounts both its clone (tickero-db-staging-conn) and a project-level addon
// (tickero-psdb-conn) therefore talks to whichever is listed LAST — and the
// list used to be alphabetical, so "psdb" beat "db-staging" by spelling.
// tickero-api-staging ran its migration release hook against production's
// DIRECT_URL that way. The clone was minted for this env; it wins.
func CloneConnsLast(envFromSecrets []string, envScope string) []string {
	if len(envFromSecrets) < 2 {
		return envFromSecrets
	}
	rest := make([]string, 0, len(envFromSecrets))
	clones := make([]string, 0, 2)
	for _, s := range envFromSecrets {
		if IsEnvScopedConn(s, envScope) {
			clones = append(clones, s)
		} else {
			rest = append(rest, s)
		}
	}
	return append(rest, clones...)
}
