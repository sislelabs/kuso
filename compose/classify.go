package compose

import (
	"strings"
)

// datastoreImages maps a normalized image base name (the repository
// without registry host or tag) to the kuso addon kind it should
// become. kuso supports these kinds via the kusoaddon helm chart:
// postgres, mysql, mariadb, redis, valkey, mongodb, clickhouse,
// kafka, rabbitmq. We map the common official-image names (and a few
// vendor-prefixed variants like bitnami/*) onto them.
var datastoreImages = map[string]string{
	"postgres":            "postgres",
	"postgresql":          "postgres",
	"postgis/postgis":     "postgres",
	"bitnami/postgresql":  "postgres",
	"mysql":               "mysql",
	"bitnami/mysql":       "mysql",
	"mariadb":             "mariadb",
	"bitnami/mariadb":     "mariadb",
	"redis":               "redis",
	"bitnami/redis":       "redis",
	"valkey/valkey":       "valkey",
	"bitnami/valkey":      "valkey",
	"mongo":               "mongodb",
	"mongodb":             "mongodb",
	"bitnami/mongodb":     "mongodb",
	"clickhouse/clickhouse-server": "clickhouse",
	"clickhouse":          "clickhouse",
	"rabbitmq":            "rabbitmq",
	"bitnami/rabbitmq":    "rabbitmq",
	"confluentinc/cp-kafka": "kafka",
	"bitnami/kafka":       "kafka",
	"apache/kafka":        "kafka",
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
