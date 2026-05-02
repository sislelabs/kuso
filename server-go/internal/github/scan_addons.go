package github

import (
	"context"
	"strings"
)

// AddonSuggestion is one heuristic match emitted by ScanAddons. The
// frontend uses these to pre-check addon checkboxes during the
// project-creation fast path.
type AddonSuggestion struct {
	Kind   string `json:"kind"`
	Reason string `json:"reason"`
}

// envHints maps env-var name patterns onto an addon kind. Only the
// first match per kind wins; the reason field tells the user why we
// suggested it.
var envHints = []struct {
	pattern string
	kind    string
}{
	{"DATABASE_URL", "postgres"},
	{"POSTGRES_URL", "postgres"},
	{"PGHOST", "postgres"},
	{"REDIS_URL", "redis"},
	{"MONGODB_URI", "mongodb"},
	{"MONGO_URL", "mongodb"},
	{"MYSQL_URL", "mysql"},
	{"DATABASE_HOST", "mysql"}, // ambiguous; postgres also matches DATABASE_URL above
	{"AMQP_URL", "rabbitmq"},
	{"RABBITMQ_URL", "rabbitmq"},
	{"MEMCACHED_URL", "memcached"},
	{"CLICKHOUSE_URL", "clickhouse"},
	{"ELASTICSEARCH_URL", "elasticsearch"},
	{"KAFKA_BROKERS", "kafka"},
	{"COCKROACHDB_URL", "cockroachdb"},
	{"COUCHDB_URL", "couchdb"},
}

// candidatePaths are the file paths we sniff for env hints. Order
// doesn't matter; we read each one and merge findings.
var candidatePaths = []string{
	".env.example",
	".env.sample",
	".env.template",
	".env.dev",
	".env.development",
	"docker-compose.yml",
	"docker-compose.yaml",
	"compose.yml",
	"compose.yaml",
}

// ScanAddons reads heuristic-friendly files from the repo at the head
// of branch, scans each line for known env-var prefixes, and returns a
// dedup'd list of (kind, reason) suggestions. pathPrefix narrows the
// scan into a monorepo subtree.
func (c *Client) ScanAddons(ctx context.Context, installationID int64, owner, repo, branch, pathPrefix string) ([]AddonSuggestion, error) {
	prefix := func(p string) string {
		if pathPrefix == "" {
			return p
		}
		return strings.TrimSuffix(pathPrefix, "/") + "/" + p
	}

	seen := map[string]string{} // kind -> reason
	for _, rel := range candidatePaths {
		path := prefix(rel)
		body, err := c.ReadFile(ctx, installationID, owner, repo, branch, path)
		if err != nil || body == "" {
			continue
		}
		body = strings.ToUpper(body)
		for _, h := range envHints {
			if _, exists := seen[h.kind]; exists {
				continue
			}
			if strings.Contains(body, h.pattern) {
				seen[h.kind] = h.pattern + " in " + rel
			}
		}
	}

	out := make([]AddonSuggestion, 0, len(seen))
	for kind, reason := range seen {
		out = append(out, AddonSuggestion{Kind: kind, Reason: reason})
	}
	return out, nil
}
