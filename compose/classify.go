package compose

import (
	"strings"
)

// datastoreImages maps a normalized image base name (the repository
// without registry host or tag) to the kuso addon kind it should
// become. Only kinds the kusoaddon helm chart ACTUALLY implements are
// listed. Other datastores (mariadb, kafka, memcached) are reserved-but-
// not-implemented in the chart (they'd render a "pending" ConfigMap, not
// a working addon), so we deliberately DON'T classify them as addons —
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
	"mysql":                        "mysql",
	"bitnami/mysql":                "mysql",
	"mongo":                        "mongodb",
	"mongodb":                      "mongodb",
	"bitnami/mongodb":              "mongodb",
	"valkey/valkey":                "valkey",
	"bitnami/valkey":               "valkey",
	"rabbitmq":                     "rabbitmq",
	"bitnami/rabbitmq":             "rabbitmq",
}

// maybeReservedDatastore returns the reserved kind name when an image
// is a recognizable-but-unimplemented datastore, else "".
func maybeReservedDatastore(image string) string {
	repo, _ := imageParts(image)
	switch baseImageName(repo) {
	case "mariadb", "bitnami/mariadb":
		return "mariadb"
	case "confluentinc/cp-kafka", "bitnami/kafka", "apache/kafka":
		return "kafka"
	case "memcached":
		return "memcached"
	}
	return ""
}

// datastoreLookalikes are datastore builds that bundle extensions or
// modules the stock managed addon lacks, and whose tags aren't engine
// versions. They stay plain image services so they keep working, and
// are flagged so the user knows no addon was made.
var datastoreLookalikes = map[string]struct{ kind, what string }{
	"pgvector/pgvector":        {"postgres", "the pgvector extension"},
	"timescale/timescaledb":    {"postgres", "the TimescaleDB extension"},
	"timescale/timescaledb-ha": {"postgres", "the TimescaleDB extension"},
	"redis/redis-stack":        {"redis", "the Redis Stack modules (search, JSON, …)"},
	"redis/redis-stack-server": {"redis", "the Redis Stack modules (search, JSON, …)"},
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

// imageDigest returns the "@sha256:…" digest suffix of an image
// reference, or "" when the image isn't digest-pinned. kuso's image
// spec is repository:tag only (the env chart renders exactly that), so
// a digest can never be carried through — callers use this to flag the
// drop instead of silently converting a pinned image to a mutable tag.
func imageDigest(image string) string {
	image = strings.TrimSpace(image)
	if at := strings.Index(image, "@"); at >= 0 {
		return image[at:]
	}
	return ""
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
			// Docker Hub mirrors (public.ecr.aws/docker/library/…).
			rest = strings.TrimPrefix(rest, "docker/library/")
			rest = strings.TrimPrefix(rest, "library/")
			return rest
		}
	}
	return repo
}

// classifyDatastore returns the kuso addon kind for a compose image,
// or "" when the image is not a recognized datastore. version is what
// the addon chart builds its image tag from, "" when the tag is
// absent / "latest" / not numeric. Its precision follows how each
// engine is tagged: mysql keeps major.minor (a bare "8" is 8.4, not
// 8.0), clickhouse and redpanda keep the full tag, the rest the major
// ("16-alpine" → "16").
func classifyDatastore(image string) (kind, version string) {
	repo, tag := imageParts(image)
	base := baseImageName(repo)
	kind, ok := datastoreImages[base]
	if !ok {
		return "", ""
	}
	tag = strings.TrimSpace(tag)
	switch {
	case tag == "" || tag == "latest":
		return kind, ""
	// The redpanda chart takes the full v-prefixed tag (default v24.1.1).
	case kind == "redpanda":
		return kind, tag
	case tag[0] < '0' || tag[0] > '9':
		return kind, ""
	case kind == "clickhouse":
		return kind, tag
	case kind == "mysql":
		return kind, leadingVersion(tag, 2)
	}
	return kind, leadingVersion(tag, 1)
}

// leadingVersion returns the first n dot-separated numeric components
// of tag: leadingVersion("8.0.36-debian", 2) = "8.0".
func leadingVersion(tag string, n int) string {
	end := 0
	for parts := 0; end < len(tag); end++ {
		c := tag[end]
		if c == '.' {
			parts++
			if parts == n {
				break
			}
			continue
		}
		if c < '0' || c > '9' {
			break
		}
	}
	return strings.TrimSuffix(tag[:end], ".")
}
