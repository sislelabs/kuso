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

func TestBuildBody_DataFile(t *testing.T) {
	dir := t.TempDir()
	f := filepath.Join(dir, "b.json")
	_ = os.WriteFile(f, []byte(`{"x":1}`), 0o600)
	b, err := buildBody("@"+f, nil, nil)
	if err != nil {
		t.Fatal(err)
	}
	if string(b) != `{"x":1}` {
		t.Fatalf("data-file body = %s", b)
	}
}

func TestBuildBody_MutuallyExclusive(t *testing.T) {
	if _, err := buildBody(`{"a":1}`, []string{"b=2"}, nil); err == nil {
		t.Error("expected error when --data and -f both set")
	}
}

func TestBuildBody_Empty(t *testing.T) {
	b, err := buildBody("", nil, nil)
	if err != nil || b != nil {
		t.Fatalf("empty inputs should yield nil body, got %v %v", b, err)
	}
}
