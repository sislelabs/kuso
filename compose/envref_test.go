package compose

import (
	"strings"
	"testing"

	"gopkg.in/yaml.v3"
)

func TestChooseAddonEnv(t *testing.T) {
	pg := addonRef{slug: "db", kind: "postgres", composeName: "db",
		creds: map[string]string{roleUser: "app", rolePassword: "secret", roleDB: "appdb"}}
	redis := addonRef{slug: "cache", kind: "redis", composeName: "cache",
		creds: map[string]string{rolePassword: "redispw"}}
	mongo := addonRef{slug: "mongo", kind: "mongodb", composeName: "mongo",
		creds: map[string]string{roleUser: "root", rolePassword: "mpw", roleDB: "shop"}}
	mysql := addonRef{slug: "mysql", kind: "mysql", composeName: "mysql",
		creds: map[string]string{roleUser: "wp", rolePassword: "wppw", roleDB: "wordpress"}}
	rp := addonRef{slug: "broker", kind: "redpanda", composeName: "broker"}

	cases := []struct {
		name, value string
		addon       addonRef
		wantRef     string // "" = literal kept
		wantFlag    bool
	}{
		// postgres
		{"DB_URL", "postgres://app:secret@db:5432/app", pg, "${{ db.DATABASE_URL }}", false},
		{"DATABASE_URL", "postgresql://app:secret@db/app?sslmode=disable", pg, "${{ db.DATABASE_URL }}", false},
		{"DIRECT_URL", "postgres://app:secret@db:5432/app", pg, "${{ db.DIRECT_URL }}", false},
		{"DB_HOST", "db", pg, "${{ db.POSTGRES_HOST }}", false},
		{"POSTGRES_HOSTNAME", "db", pg, "${{ db.POSTGRES_HOST }}", false},
		{"PGHOST", "db", pg, "${{ db.POSTGRES_HOST }}", false},
		{"DB_USER", "app", pg, "${{ db.POSTGRES_USER }}", false},
		{"DB_PASSWORD", "secret", pg, "${{ db.POSTGRES_PASSWORD }}", false},
		{"PGPASSWORD", "secret", pg, "${{ db.POSTGRES_PASSWORD }}", false},
		{"DB_NAME", "appdb", pg, "${{ db.POSTGRES_DB }}", false},
		{"DB_SERVER", "db", pg, "${{ db.POSTGRES_HOST }}", true},
		{"DB_HOST", "postgres://app:secret@db:5432/app", pg, "", true},
		{"DB_ADDR", "db:5432", pg, "", true},
		// Unrelated values stay untouched and unflagged.
		{"APP_NAME", "appdb", pg, "", false},
		{"LOG_LEVEL", "debug", pg, "", false},
		{"DB_PORT", "5432", pg, "", false},

		// redis — the live bug: REDIS_HOST must get the HOST, not the URL.
		{"REDIS_HOST", "cache", redis, "${{ cache.REDIS_HOST }}", false},
		{"REDIS_URL", "redis://cache:6379/0", redis, "${{ cache.REDIS_URL }}", false},
		{"REDIS_URL", "redis://:redispw@cache:6379", redis, "${{ cache.REDIS_URL }}", false},
		{"REDIS_PASSWORD", "redispw", redis, "${{ cache.REDIS_PASSWORD }}", false},
		{"CACHE_URL", "cache", redis, "${{ cache.REDIS_HOST }}", true},
		{"REDIS_USER", "cache", redis, "", true}, // redis has no user key
		{"REDIS_ADDR", "cache:6379", redis, "", true},

		// mongodb
		{"MONGO_URL", "mongodb://root:mpw@mongo:27017/shop", mongo, "${{ mongo.MONGO_URL }}", false},
		{"MONGODB_URI", "mongodb://root:mpw@mongo:27017/shop?authSource=admin", mongo, "${{ mongo.MONGODB_URI }}", false},
		{"DB_URL", "mongodb://mongo/shop", mongo, "${{ mongo.MONGO_URL }}", false},
		{"MONGO_HOST", "mongo", mongo, "${{ mongo.MONGO_HOST }}", false},
		{"MONGO_USERNAME", "root", mongo, "${{ mongo.MONGO_USER }}", false},
		{"MONGO_DATABASE", "shop", mongo, "${{ mongo.MONGO_DB }}", false},

		// mysql
		{"DATABASE_URL", "mysql://wp:wppw@mysql:3306/wordpress", mysql, "${{ mysql.DATABASE_URL }}", false},
		{"DB_URL", "mysql://wp:wppw@mysql:3306/wordpress", mysql, "${{ mysql.MYSQL_URL }}", false},
		{"WORDPRESS_DB_HOST", "mysql", mysql, "${{ mysql.MYSQL_HOST }}", false},
		{"WORDPRESS_DB_USER", "wp", mysql, "${{ mysql.MYSQL_USER }}", false},
		{"WORDPRESS_DB_PASSWORD", "wppw", mysql, "${{ mysql.MYSQL_PASSWORD }}", false},
		{"WORDPRESS_DB_NAME", "wordpress", mysql, "${{ mysql.MYSQL_DB }}", false},

		// redpanda: a bootstrap host:port maps onto KAFKA_BROKERS.
		{"KAFKA_BROKERS", "broker:9092", rp, "${{ broker.KAFKA_BROKERS }}", false},
	}
	for _, c := range cases {
		t.Run(c.addon.kind+"/"+c.name+"="+c.value, func(t *testing.T) {
			ch := chooseAddonEnv(c.name, c.value, []addonRef{c.addon})
			if ch.ref != c.wantRef {
				t.Errorf("ref = %q, want %q (why=%q)", ch.ref, c.wantRef, ch.why)
			}
			if ch.flag != c.wantFlag {
				t.Errorf("flag = %v, want %v (why=%q)", ch.flag, c.wantFlag, ch.why)
			}
			if (ch.ref != "" || ch.flag) && ch.why == "" {
				t.Error("a rewrite or flag must explain itself")
			}
		})
	}
}

func TestConvert_EnvRewriteChoosesKeyByShape(t *testing.T) {
	doc, rep := convertString(t, `
services:
  api:
    image: myorg/api:1.0
    environment:
      REDIS_HOST: cache
      DB_URL: postgres://app:secret@db:5432/app
      DB_PASSWORD: secret
      CACHE_ADDR: cache:6379
  cache:
    image: redis:7
  db:
    image: postgres:16
    environment:
      POSTGRES_PASSWORD: secret
`)
	svc := findService(doc, "api")
	if svc == nil {
		t.Fatal("api service not found")
	}
	want := map[string]string{
		"REDIS_HOST":  "${{ cache.REDIS_HOST }}",
		"DB_URL":      "${{ db.DATABASE_URL }}",
		"DB_PASSWORD": "${{ db.POSTGRES_PASSWORD }}",
		"CACHE_ADDR":  "cache:6379",
	}
	for k, v := range want {
		if svc.Env[k] != v {
			t.Errorf("env %s = %q, want %q", k, svc.Env[k], v)
		}
	}
	md := rep.Markdown()
	for _, s := range []string{"key REDIS_HOST", "key DATABASE_URL", "key POSTGRES_PASSWORD", "CACHE_ADDR"} {
		if !strings.Contains(md, s) {
			t.Errorf("report missing %q:\n%s", s, md)
		}
	}
	flagged := false
	for _, n := range rep.Notes {
		if n.Action == ActionFlag && strings.Contains(n.Detail, "CACHE_ADDR") {
			flagged = true
		}
	}
	if !flagged {
		t.Error("CACHE_ADDR (unmappable host:port) must be flagged")
	}
}

func TestConvert_StockImageCapabilities(t *testing.T) {
	doc, rep := convertString(t, `
services:
  web:
    image: nginx:1.27-alpine
    ports: ["8080:80"]
  apache:
    image: php:8.3-apache
  wp:
    image: wordpress:6
  lowport:
    image: myorg/dns:1
    ports: ["53:53"]
  plain:
    image: myorg/api:1
    ports: ["3000:3000"]
  unpriv:
    image: nginxinc/nginx-unprivileged:1.27
    ports: ["8080:8080"]
`)
	cases := map[string][]string{
		"web":     {"CHOWN", "SETUID", "SETGID", "NET_BIND_SERVICE"},
		"apache":  {"SETUID", "SETGID", "NET_BIND_SERVICE"},
		"wp":      {"CHOWN", "SETUID", "SETGID", "NET_BIND_SERVICE"},
		"lowport": {"NET_BIND_SERVICE"},
		"plain":   nil,
		"unpriv":  nil,
	}
	for name, want := range cases {
		svc := findService(doc, name)
		if svc == nil {
			t.Fatalf("service %s missing", name)
		}
		var got []string
		if svc.SecurityContext != nil && svc.SecurityContext.Capabilities != nil {
			got = svc.SecurityContext.Capabilities.Add
		}
		if strings.Join(got, ",") != strings.Join(want, ",") {
			t.Errorf("%s caps = %v, want %v", name, got, want)
		}
		if want == nil {
			continue
		}
		found := false
		for _, n := range rep.Notes {
			if n.Service == name && strings.Contains(n.Detail, "added capabilities "+strings.Join(want, ", ")) {
				found = true
			}
		}
		if !found {
			t.Errorf("%s: no 'added capabilities' report row", name)
		}
	}
	out, err := doc.Marshal()
	if err != nil {
		t.Fatal(err)
	}
	var back Doc
	if err := yaml.Unmarshal(out, &back); err != nil {
		t.Fatal(err)
	}
	if b := findService(&back, "web"); b == nil || b.SecurityContext == nil || b.SecurityContext.Capabilities == nil ||
		len(b.SecurityContext.Capabilities.Add) != 4 || !strings.Contains(string(out), "securityContext:") {
		t.Errorf("kuso.yaml missing securityContext block:\n%s", out)
	}
}
