package compose

import (
	"strings"
)

// datastoreImages maps a normalized image base name (the repository
// without registry host or tag) to the kuso addon kind it should
// become. Only kinds the kusoaddon helm chart ACTUALLY implements are
// listed — postgres, redis, clickhouse. Other datastores (mysql,
// mariadb, mongodb, valkey, kafka, rabbitmq) are reserved-but-not-
// implemented in the chart (they'd render a "pending" ConfigMap, not a
// working addon), so we deliberately DON'T classify them as addons —
// they fall through to a flagged service instead (see maybeDatastore).
var datastoreImages = map[string]string{
	"postgres":                     "postgres",
	"postgresql":                   "postgres",
	"postgis/postgis":              "postgres",
	"bitnami/postgresql":           "postgres",
	"redis":                        "redis",
	"bitnami/redis":                "redis",
	"clickhouse/clickhouse-server": "clickhouse",
	"clickhouse":                   "clickhouse",
	"redpandadata/redpanda":        "redpanda",
	"vectorized/redpanda":          "redpanda",
}

// addonURLKey returns the conn-secret key that holds the canonical
// connection URL for an addon kind — what a ${{ addon.KEY }} reference
// must point at. Each kind's helm chart names this differently
// (postgres→DATABASE_URL, redis→REDIS_URL, clickhouse→CLICKHOUSE_URL).
func addonURLKey(kind string) string {
	switch kind {
	case "postgres":
		return "DATABASE_URL"
	case "redis":
		return "REDIS_URL"
	case "clickhouse":
		return "CLICKHOUSE_URL"
	case "redpanda":
		return "REDPANDA_URL"
	default:
		return ""
	}
}

// reservedDatastores are kinds that look like datastores but the kuso
// addon chart doesn't implement yet. We detect them only to flag
// them, never to create a (broken) addon.
var reservedDatastores = map[string]bool{
	"mysql": true, "mariadb": true, "mongodb": true,
	"valkey": true, "kafka": true, "rabbitmq": true, "memcached": true,
}

// maybeReservedDatastore returns the reserved kind name when an image
// is a recognizable-but-unimplemented datastore, else "".
func maybeReservedDatastore(image string) string {
	repo, _ := imageParts(image)
	switch baseImageName(repo) {
	case "mysql", "bitnami/mysql":
		return "mysql"
	case "mariadb", "bitnami/mariadb":
		return "mariadb"
	case "mongo", "mongodb", "bitnami/mongodb":
		return "mongodb"
	case "valkey/valkey", "bitnami/valkey":
		return "valkey"
	case "rabbitmq", "bitnami/rabbitmq":
		return "rabbitmq"
	case "confluentinc/cp-kafka", "bitnami/kafka", "apache/kafka":
		return "kafka"
	case "memcached":
		return "memcached"
	}
	return ""
}

// imageParts splits a compose image reference into its repository
// (registry host + path, lowercased) and tag. A digest (@sha256:…) is
// dropped from the tag. "ghcr.io/foo/bar:1.2" → ("ghcr.io/foo/bar",
// "1.2"); "postgres:16-alpine" → ("postgres", "16-alpine").
func imageParts(image string) (repo, tag string) {
	image = strings.TrimSpace(image)
	if image == "" {
		return "", ""
	}
	// Strip digest first so the ':' in "@sha256:" isn't mistaken for a
	// tag separator.
	if at := strings.Index(image, "@"); at >= 0 {
		image = image[:at]
	}
	// A ':' is only a tag separator if it appears after the last '/'
	// (otherwise it's a registry port, e.g. localhost:5000/foo).
	slash := strings.LastIndex(image, "/")
	colon := strings.LastIndex(image, ":")
	if colon > slash {
		return strings.ToLower(image[:colon]), image[colon+1:]
	}
	return strings.ToLower(image), ""
}

// baseImageName strips a known registry host from a repository so
// "docker.io/library/postgres" and "postgres" both normalize to
// "postgres", and "ghcr.io/bitnami/redis" → "bitnami/redis".
func baseImageName(repo string) string {
	repo = strings.TrimPrefix(repo, "docker.io/")
	repo = strings.TrimPrefix(repo, "library/")
	// Strip a leading registry host (contains a '.' or ':' in the
	// first path segment) so vendor-prefixed lookups still work.
	if i := strings.Index(repo, "/"); i >= 0 {
		first := repo[:i]
		if strings.ContainsAny(first, ".:") {
			rest := repo[i+1:]
			rest = strings.TrimPrefix(rest, "library/")
			return rest
		}
	}
	return repo
}

// classifyDatastore returns the kuso addon kind for a compose image,
// or "" when the image is not a recognized datastore. version is the
// major version parsed from the tag (e.g. "16" from "16-alpine"), or
// "" when the tag is absent / "latest" / unparseable.
func classifyDatastore(image string) (kind, version string) {
	repo, tag := imageParts(image)
	base := baseImageName(repo)
	kind, ok := datastoreImages[base]
	if !ok {
		return "", ""
	}
	return kind, majorVersion(tag)
}

// majorVersion extracts the leading numeric component of a tag.
// "16-alpine" → "16", "8.0.36" → "8", "latest"/"" → "".
func majorVersion(tag string) string {
	tag = strings.TrimSpace(tag)
	if tag == "" || tag == "latest" {
		return ""
	}
	var b strings.Builder
	for _, r := range tag {
		if r >= '0' && r <= '9' {
			b.WriteRune(r)
			continue
		}
		break
	}
	return b.String()
}
