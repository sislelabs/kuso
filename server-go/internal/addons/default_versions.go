package addons

// newAddonVersions is the version a NEW addon of each kind is created
// with when the request names none. It is stamped onto the CR at create
// time instead of being left to the chart's `| default`, so raising a
// default here never changes an existing addon: CRs created before this
// existed have an empty spec.version and keep rendering the chart
// default they always had (a silent major upgrade of a live database).
//
// Each value is a tag fragment in the shape the kind's template expects
// (redis renders "redis:%s-alpine", clickhouse takes the full tag).
var newAddonVersions = map[string]string{
	"postgres":    "18",
	"redis":       "8",
	"valkey":      "9",
	"mongodb":     "8",
	"mysql":       "8.4",
	"rabbitmq":    "4",
	"nats":        "2.12-alpine",
	"clickhouse":  "25.8-alpine",
	"redpanda":    "v26.2.6",
	"meilisearch": "v1.56",
	"mailpit":     "v1.33",
	"s3":          "RELEASE.2026-08-04T00-00-00Z",
}

// newHAPostgresVersion is the default for kind=postgres with ha=true:
// the CloudNativePG operator decides which majors an HA cluster can run.
const newHAPostgresVersion = "17"

// DefaultAddonVersion returns the version a new addon of kind gets when
// none was requested, or "" when kuso has no opinion (unknown kind).
func DefaultAddonVersion(kind string, ha bool) string {
	if kind == "postgres" && ha {
		return newHAPostgresVersion
	}
	return newAddonVersions[kind]
}
