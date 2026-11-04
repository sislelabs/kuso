package compose

import (
	"context"
	"os"
	"path/filepath"
	"strings"
	"testing"
)

func TestParseUploaded_RefusesServerFileReferences(t *testing.T) {
	dir := t.TempDir()
	secret := filepath.Join(dir, "secret.yml")
	if err := os.WriteFile(secret, []byte("services:\n  y:\n    image: nginx\n    environment:\n      API_KEY: leaked-value\n"), 0o600); err != nil {
		t.Fatal(err)
	}
	cases := map[string]string{
		"extends.file":    "services:\n  x:\n    extends:\n      file: " + secret + "\n      service: y\n",
		"aliased extends": "x-base: &b\n  file: " + secret + "\n  service: y\nservices:\n  x:\n    extends: *b\n",
		"include":         "include:\n  - " + secret + "\nservices:\n  x:\n    image: nginx\n",
	}
	for name, doc := range cases {
		proj, err := ParseUploaded(context.Background(), []byte(doc))
		if err == nil {
			t.Errorf("%s: want error, got services %+v", name, proj.Services)
			continue
		}
		if strings.Contains(err.Error(), "leaked-value") || strings.Contains(err.Error(), "API_KEY") {
			t.Errorf("%s: error leaks file contents: %v", name, err)
		}
	}

	// Same-file extends is not a filesystem read and keeps working.
	proj, err := ParseUploaded(context.Background(), []byte("services:\n  base:\n    image: nginx\n  x:\n    extends: base\n"))
	if err != nil {
		t.Fatalf("same-file extends: %v", err)
	}
	if proj.Services["x"].Image != "nginx" {
		t.Fatalf("same-file extends not applied: %+v", proj.Services["x"])
	}

	// The local CLI path (Parse) still honours extends.file.
	proj, err = Parse(context.Background(), []byte(cases["extends.file"]), dir)
	if err != nil {
		t.Fatalf("Parse extends.file: %v", err)
	}
	if proj.Services["x"].Image != "nginx" {
		t.Fatalf("Parse extends.file not applied: %+v", proj.Services["x"])
	}
}

func TestConvert_ChartBackedDatastoresBecomeAddons(t *testing.T) {
	doc, _ := convertString(t, `
services:
  api:
    image: myorg/api:1.0
    environment:
      AMQP_URL: amqp://guest:guest@mq:5672
      MYSQL_URL: mysql://u:p@my:3306/app
  my:
    image: mysql:8.0
  mo:
    image: mongo:7
  va:
    image: valkey/valkey:8
  mq:
    image: rabbitmq:3-management
`)
	for name, kind := range map[string]string{"my": "mysql", "mo": "mongodb", "va": "valkey", "mq": "rabbitmq"} {
		a := findAddon(doc, name)
		if a == nil || a.Kind != kind {
			t.Errorf("%s: want %s addon, got %+v", name, kind, a)
		}
	}
	api := findService(doc, "api")
	if api == nil {
		t.Fatal("api service missing")
	}
	if got := api.Env["AMQP_URL"]; got != "${{ mq.AMQP_URL }}" {
		t.Errorf("AMQP_URL = %q, want ${{ mq.AMQP_URL }}", got)
	}
	if got := api.Env["MYSQL_URL"]; got != "${{ my.MYSQL_URL }}" {
		t.Errorf("MYSQL_URL = %q, want ${{ my.MYSQL_URL }}", got)
	}
}

func TestDedupeSlugs_NeverHandsOutSameNameTwice(t *testing.T) {
	got := dedupeSlugs([]string{"Worker", "worker", "worker-2"})
	seen := map[string]string{}
	for in, slug := range got {
		if prev, dup := seen[slug]; dup {
			t.Fatalf("%q and %q both got slug %q (%v)", prev, in, slug, got)
		}
		seen[slug] = in
	}
	if len(got) != 3 {
		t.Fatalf("want 3 slugs, got %v", got)
	}
}
