package kusoCli

import (
	"bytes"
	"strings"
	"testing"
)

func TestReadProjectFromYAML(t *testing.T) {
	cases := map[string]string{
		"project: foo\n":                        "foo",
		"project: foo # note\n":                 "foo",
		"project: foo  # trailing comment\n":    "foo",
		"project: \"foo\" # q\n":                "foo",
		"project: 'foo' # q\n":                  "foo",
		"project: my#app\n":                     "my#app", // '#' not preceded by space = literal
		"project: 'a # b'\n":                    "a # b",  // '#' inside quotes is literal
		"baseDomain: x\nproject: bar\n":         "bar",
		"# project: commented\nproject: real\n": "real",
	}
	for in, want := range cases {
		if got := readProjectFromYAML([]byte(in)); got != want {
			t.Errorf("readProjectFromYAML(%q) = %q, want %q", in, got, want)
		}
	}
}

func TestStripInlineComment(t *testing.T) {
	cases := map[string]string{
		"foo":        "foo",
		"foo # bar":  "foo",
		"foo\t# bar": "foo",
		"my#app":     "my#app",
		"'a # b'":    "'a # b'",
		"\"a # b\"":  "\"a # b\"",
		"# whole":    "",
		"a 'b' # c":  "a 'b'",
	}
	for in, want := range cases {
		if got := stripInlineComment(in); got != want {
			t.Errorf("stripInlineComment(%q) = %q, want %q", in, got, want)
		}
	}
}

func TestRenderApplyResult_PrintsFieldDiffs(t *testing.T) {
	body := []byte(`{
		"servicesToUpdate": ["api"], "servicesUnchanged": ["worker"],
		"addonsUnchanged": ["db"], "cronsUnchanged": [],
		"changes": [
			{"resource": "service:api", "fields": [
				{"field": "domains", "from": "[a.com(tls)]", "to": "(none)", "destructive": true},
				{"field": "port", "from": "8080", "to": "9090"}
			]},
			{"resource": "addon:db", "notApplied": true, "fields": [{"field": "version", "from": "16", "to": "17"}]}
		],
		"warnings": ["service api: env FEATURE_X is {secret: true} but the service's Secret doesn't hold it"]
	}`)
	var out bytes.Buffer
	if failed := renderApplyResult(&out, &out, body, true); failed {
		t.Fatalf("dry-run with no errors reported failure")
	}
	got := out.String()
	for _, want := range []string{
		"services: would create 0, update 1, delete 0, unchanged 1",
		"addons:   would create 0, update 0, delete 0, unchanged 1",
		"~ service api",
		"domains: [a.com(tls)] → (none)  [DESTRUCTIVE]",
		"port: 8080 → 9090",
		"1 destructive change",
		"addon db (drift, not applied",
		"version: 16 → 17",
		"FEATURE_X",
	} {
		if !strings.Contains(got, want) {
			t.Errorf("output missing %q:\n%s", want, got)
		}
	}
}

func TestSecretEnvNote(t *testing.T) {
	doc := []byte("apiVersion: kuso/v1\nproject: rs\nservices:\n  - name: api\n    env:\n      APP_ENV: production\n      FEATURE_X:\n        secret: true\n  - name: web\n    env:\n      TOKEN:\n        generate: hex32\n")
	note := secretEnvNote(doc)
	if !strings.Contains(note, "api: FEATURE_X") || strings.Contains(note, "APP_ENV") || strings.Contains(note, "TOKEN") {
		t.Fatalf("note = %q", note)
	}
	if secretEnvNote([]byte("project: rs\n")) != "" {
		t.Fatalf("no secret keys must produce no note")
	}
}
