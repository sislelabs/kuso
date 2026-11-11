package kusoCli

import (
	"encoding/json"
	"os"
	"path/filepath"
	"testing"
)

func TestNormalizeAPIPath(t *testing.T) {
	cases := map[string]string{
		"projects": "/api/projects",
		"/api/x":   "/api/x",
		"/foo":     "/api/foo",
		"api/y":    "/api/y",
	}
	for in, want := range cases {
		if got := normalizeAPIPath(in); got != want {
			t.Errorf("normalizeAPIPath(%q) = %q, want %q", in, got, want)
		}
	}
}

func TestValidateMethod(t *testing.T) {
	if m, err := validateMethod("post"); err != nil || m != "POST" {
		t.Errorf("post -> %q, %v", m, err)
	}
	if _, err := validateMethod("FROBNICATE"); err == nil {
		t.Error("expected error for bad method")
	}
}

func TestBuildBody_Fields(t *testing.T) {
	b, err := buildBody("", []string{"count=3", "enabled=true", "name=foo"}, []string{"id=007"})
	if err != nil {
		t.Fatal(err)
	}
	var m map[string]any
	if err := json.Unmarshal(b, &m); err != nil {
		t.Fatalf("not json: %s", b)
	}
	if m["count"].(float64) != 3 || m["enabled"] != true || m["name"] != "foo" || m["id"] != "007" {
		t.Fatalf("coercion wrong: %#v", m)
	}
}

func TestBuildBody(t *testing.T) {
	f := filepath.Join(t.TempDir(), "b.json")
	if err := os.WriteFile(f, []byte(`{"x":1}`), 0o600); err != nil {
		t.Fatal(err)
	}
	for _, tc := range []struct {
		name    string
		data    string
		fields  []string
		wantErr bool
		want    []byte // nil: the body itself must be nil
	}{
		{name: "DataFile", data: "@" + f, want: []byte(`{"x":1}`)},
		// --data and -f both set
		{name: "MutuallyExclusive", data: `{"a":1}`, fields: []string{"b=2"}, wantErr: true},
		{name: "Empty"},
	} {
		t.Run(tc.name, func(t *testing.T) {
			b, err := buildBody(tc.data, tc.fields, nil)
			if tc.wantErr {
				if err == nil {
					t.Error("expected error")
				}
				return
			}
			if err != nil {
				t.Fatal(err)
			}
			if tc.want == nil && b != nil || string(b) != string(tc.want) {
				t.Fatalf("body = %s, want %s", b, tc.want)
			}
		})
	}
}
