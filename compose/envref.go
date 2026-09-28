package compose

import (
	"regexp"
	"sort"
	"strings"
)

// Roles an app env var can play against an addon. The role picks the
// conn-secret key; a URL where the app wanted a hostname breaks it.
const (
	roleURL      = "url"
	roleHost     = "host"
	rolePort     = "port"
	roleUser     = "user"
	rolePassword = "password"
	roleDB       = "db"
	roleHostPort = "hostport"
)

// addonKeys is the per-kind conn-secret key table, mirroring the
// kusoaddon chart templates (operator/helm-charts/kusoaddon/templates/).
// An empty key means the kind injects nothing for that role.
type addonKeys struct {
	url string
	// urlAliases are other URL-shaped keys the chart also injects; used
	// when the app's var name is literally one of them.
	urlAliases                     []string
	host, port, user, password, db string
	hostPort                       string
}

var addonKeyTable = map[string]addonKeys{
	"postgres": {url: "DATABASE_URL", urlAliases: []string{"DIRECT_URL"},
		host: "POSTGRES_HOST", port: "POSTGRES_PORT", user: "POSTGRES_USER", password: "POSTGRES_PASSWORD", db: "POSTGRES_DB"},
	"redis": {url: "REDIS_URL", host: "REDIS_HOST", port: "REDIS_PORT", password: "REDIS_PASSWORD"},
	"valkey": {url: "VALKEY_URL", urlAliases: []string{"REDIS_URL"},
		host: "VALKEY_HOST", port: "VALKEY_PORT", password: "REDIS_PASSWORD"},
	"mongodb": {url: "MONGO_URL", urlAliases: []string{"MONGODB_URI", "DATABASE_URL"},
		host: "MONGO_HOST", port: "MONGO_PORT", user: "MONGO_USER", password: "MONGO_PASSWORD", db: "MONGO_DB"},
	"mysql": {url: "MYSQL_URL", urlAliases: []string{"DATABASE_URL"},
		host: "MYSQL_HOST", port: "MYSQL_PORT", user: "MYSQL_USER", password: "MYSQL_PASSWORD", db: "MYSQL_DB"},
	// clickhouse has two ports (HTTP + native) — no single port key.
	"clickhouse": {url: "CLICKHOUSE_URL", urlAliases: []string{"CLICKHOUSE_NATIVE_URL"},
		host: "CLICKHOUSE_HOST", user: "CLICKHOUSE_USER", password: "CLICKHOUSE_PASSWORD", db: "CLICKHOUSE_DATABASE"},
	"redpanda": {url: "REDPANDA_URL", host: "KAFKA_HOST", port: "KAFKA_PORT", hostPort: "KAFKA_BROKERS"},
}

func (k addonKeys) forRole(role string) string {
	switch role {
	case roleURL:
		return k.url
	case roleHost:
		return k.host
	case rolePort:
		return k.port
	case roleUser:
		return k.user
	case rolePassword:
		return k.password
	case roleDB:
		return k.db
	case roleHostPort:
		return k.hostPort
	}
	return ""
}

// credEnv maps a datastore image's own init env vars onto roles, so an
// app var whose value equals e.g. the compose POSTGRES_PASSWORD is known
// to be that addon's password (kuso mints a new one, so the literal
// would be wrong after import).
var credEnv = map[string]map[string]string{
	"postgres": {"POSTGRES_USER": roleUser, "POSTGRES_PASSWORD": rolePassword, "POSTGRES_DB": roleDB,
		"POSTGRESQL_USERNAME": roleUser, "POSTGRESQL_PASSWORD": rolePassword, "POSTGRESQL_DATABASE": roleDB},
	"redis":      {"REDIS_PASSWORD": rolePassword},
	"valkey":     {"VALKEY_PASSWORD": rolePassword, "REDIS_PASSWORD": rolePassword},
	"mongodb":    {"MONGO_INITDB_ROOT_USERNAME": roleUser, "MONGO_INITDB_ROOT_PASSWORD": rolePassword, "MONGO_INITDB_DATABASE": roleDB},
	"mysql":      {"MYSQL_USER": roleUser, "MYSQL_PASSWORD": rolePassword, "MYSQL_DATABASE": roleDB},
	"clickhouse": {"CLICKHOUSE_USER": roleUser, "CLICKHOUSE_PASSWORD": rolePassword, "CLICKHOUSE_DB": roleDB},
}

// datastoreCreds extracts role→literal from a datastore service's env.
func datastoreCreds(kind string, env map[string]*string) map[string]string {
	out := map[string]string{}
	for k, role := range credEnv[kind] {
		if v, ok := env[k]; ok && v != nil && *v != "" {
			out[role] = *v
		}
	}
	return out
}

var roleSuffixes = []struct{ suffix, role string }{
	{"_HOSTNAME", roleHost}, {"_HOST", roleHost},
	{"_PORT", rolePort},
	{"_USERNAME", roleUser}, {"_USER", roleUser},
	{"_PASSWORD", rolePassword}, {"_PASS", rolePassword},
	{"_DATABASE", roleDB}, {"_DB", roleDB}, {"_NAME", roleDB},
	{"_BROKERS", roleHostPort},
	{"_URL", roleURL}, {"_URI", roleURL}, {"_DSN", roleURL},
}

var libpqVars = map[string]string{
	"PGHOST": roleHost, "PGPORT": rolePort, "PGUSER": roleUser, "PGPASSWORD": rolePassword, "PGDATABASE": roleDB,
}

// nameRole returns the role a var name implies and the name with the
// role suffix removed (for the credential prefix check).
func nameRole(name string) (role, prefix string) {
	up := strings.ToUpper(name)
	if r, ok := libpqVars[up]; ok {
		return r, "PG"
	}
	for _, s := range roleSuffixes {
		if strings.HasSuffix(up, s.suffix) {
			return s.role, strings.TrimSuffix(up, s.suffix)
		}
	}
	return "", up
}

var dbWords = map[string]bool{
	"DB": true, "DATABASE": true, "SQL": true, "PG": true, "PGSQL": true, "POSTGRES": true, "POSTGRESQL": true,
	"MYSQL": true, "MARIADB": true, "MONGO": true, "MONGODB": true, "REDIS": true, "CACHE": true, "VALKEY": true,
	"CLICKHOUSE": true, "CH": true, "KAFKA": true, "REDPANDA": true,
}

// credPrefixOK guards the credential match: DB_PASSWORD=secret is the
// addon's password, APP_NAME=app is not the addon's database name even
// when POSTGRES_DB is also "app".
func credPrefixOK(prefix string, a addonRef) bool {
	own := strings.ToUpper(strings.NewReplacer("-", "_", ".", "_").Replace(a.composeName))
	if prefix == "" || prefix == own || strings.HasSuffix(prefix, "_"+own) {
		return true
	}
	for _, tok := range strings.Split(prefix, "_") {
		if dbWords[tok] || tok == own {
			return true
		}
	}
	return false
}

// urlHosts returns the host(s) of a scheme://[userinfo@]host[:port][,host…]/…
// value, or nil when the value isn't URL-shaped.
func urlHosts(v string) []string {
	i := strings.Index(v, "://")
	if i <= 0 {
		return nil
	}
	rest := v[i+3:]
	if j := strings.IndexAny(rest, "/?#"); j >= 0 {
		rest = rest[:j]
	}
	if at := strings.LastIndex(rest, "@"); at >= 0 {
		rest = rest[at+1:]
	}
	var hosts []string
	for _, hp := range strings.Split(rest, ",") {
		if c := strings.LastIndex(hp, ":"); c >= 0 {
			hp = hp[:c]
		}
		hosts = append(hosts, hp)
	}
	return hosts
}

var hostPortRE = regexp.MustCompile(`^([^:/@\s]+):\d{1,5}$`)

type valueShape struct {
	url, host, hostPort bool
	credRole            string
}

func (s valueShape) any() bool { return s.url || s.host || s.hostPort || s.credRole != "" }

func shapeFor(name, val string, a addonRef) valueShape {
	var s valueShape
	for _, h := range urlHosts(val) {
		if strings.EqualFold(h, a.composeName) {
			s.url = true
		}
	}
	s.host = strings.EqualFold(val, a.composeName)
	if m := hostPortRE.FindStringSubmatch(val); m != nil && strings.EqualFold(m[1], a.composeName) {
		s.hostPort = true
	}
	role, prefix := nameRole(name)
	if val != "" && a.creds[role] == val && credPrefixOK(prefix, a) {
		s.credRole = role
	}
	return s
}

// envChoice is the outcome for one app env var. ref=="" keeps the
// literal; flag asks the user to check it; why is the report text.
type envChoice struct {
	addon addonRef
	key   string
	ref   string
	flag  bool
	why   string
}

func (c envChoice) matched() bool { return c.ref != "" || c.flag }

// chooseAddonEnv picks the conn-secret key an app env var should
// reference. The var's NAME decides the role where it names one
// (_HOST, _PORT, _USER, _PASSWORD, _DB…); otherwise the VALUE's shape
// does (a URL/DSN gets the kind's URL key). When name and value
// disagree, or the kind has no key for the role, the literal is kept
// and flagged — never a URL where the app expects a host.
func chooseAddonEnv(name, val string, addons []addonRef) envChoice {
	for _, a := range addons {
		s := shapeFor(name, val, a)
		if !s.any() {
			continue
		}
		keys, known := addonKeyTable[a.kind]
		if !known {
			return envChoice{addon: a, flag: true,
				why: "value points at addon `" + a.slug + "` (kind " + a.kind + ") but kuso has no key table for that kind — literal kept; set a ${{ " + a.slug + ".<KEY> }} reference by hand"}
		}
		role, _ := nameRole(name)
		ok := func(key, reason string) envChoice {
			return envChoice{addon: a, key: key, ref: "${{ " + a.slug + "." + key + " }}", why: reason}
		}
		keep := func(reason string) envChoice {
			return envChoice{addon: a, flag: true, why: reason + " — literal kept; point it at the right `${{ " + a.slug + ".<KEY> }}` by hand"}
		}
		switch role {
		case roleHost, rolePort, roleUser, rolePassword, roleDB:
			compatible := (role == roleHost && s.host) || s.credRole == role
			if !compatible {
				return keep("name ends like a " + role + " but the value (" + shapeDesc(s) + ") doesn't fit")
			}
			key := keys.forRole(role)
			if key == "" {
				return keep("addon kind " + a.kind + " injects no " + role + " key")
			}
			return ok(key, "name ends like a "+role)
		case roleHostPort:
			if s.hostPort && keys.hostPort != "" {
				return ok(keys.hostPort, "host:port bootstrap value")
			}
			return keep("value (" + shapeDesc(s) + ") isn't a host:port this addon kind injects")
		}
		// No decisive name suffix (or _URL/_URI/_DSN): value shape decides.
		switch {
		case s.url:
			up := strings.ToUpper(name)
			for _, alias := range keys.urlAliases {
				if up == alias {
					return ok(alias, "URL value; var name matches the injected key")
				}
			}
			return ok(keys.url, "URL/DSN value → the kind's connection URL")
		case s.host:
			c := ok(keys.host, "value is the bare hostname `"+a.composeName+"`")
			c.flag = true
			c.why = "value is the bare hostname `" + a.composeName + "` but the name doesn't say HOST, so it gets the HOST key (not the URL) — check the app expects a hostname here"
			return c
		case s.hostPort:
			if keys.hostPort != "" {
				return ok(keys.hostPort, "host:port bootstrap value")
			}
			return keep("host:port value has no matching " + a.kind + " key (kuso injects " + keys.host + " and " + orNone(keys.port) + " separately)")
		}
		// Credential match under a name that implies no role — not ours.
	}
	return envChoice{}
}

func shapeDesc(s valueShape) string {
	switch {
	case s.url:
		return "a URL"
	case s.hostPort:
		return "host:port"
	case s.host:
		return "a hostname"
	case s.credRole != "":
		return "a " + s.credRole
	}
	return "unrecognized"
}

func orNone(k string) string {
	if k == "" {
		return "no port key"
	}
	return k
}

// sortedAddons gives chooseAddonEnv a deterministic scan order.
func sortedAddons(m map[string]addonRef) []addonRef {
	out := make([]addonRef, 0, len(m))
	for _, a := range m {
		out = append(out, a)
	}
	sort.Slice(out, func(i, j int) bool { return out[i].composeName < out[j].composeName })
	return out
}
