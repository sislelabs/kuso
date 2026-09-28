package kusoCli

import (
	"encoding/json"
	"strings"
	"testing"
)

func TestParseWatchPathsFlag(t *testing.T) {
	t.Parallel()
	got := parseWatchPathsFlag(" apps/web/**, packages/**\nkuso.yml ,")
	if strings.Join(got, "|") != "apps/web/**|packages/**|kuso.yml" {
		t.Errorf("got %q", got)
	}
	cleared := parseWatchPathsFlag("")
	if cleared == nil || len(cleared) != 0 {
		t.Errorf("empty flag must be a non-nil empty list (clear), got %#v", cleared)
	}
	raw, _ := json.Marshal(struct {
		W *[]string `json:"watchPaths,omitempty"`
	}{&cleared})
	if string(raw) != `{"watchPaths":[]}` {
		t.Errorf("clear must reach the wire as [], got %s", raw)
	}
}

func TestImagePullSecretPatch(t *testing.T) {
	t.Parallel()
	cur := []byte(`{"spec":{"runtime":"image","image":{"repository":"ghcr.io/acme/api","tag":"v3","pullSecret":"old"}}}`)
	p, err := imagePullSecretPatch(cur, "ghcr.io")
	if err != nil {
		t.Fatal(err)
	}
	raw, _ := json.Marshal(p)
	if string(raw) != `{"repository":"ghcr.io/acme/api","tag":"v3","pullSecret":"ghcr.io"}` {
		t.Errorf("patch = %s", raw)
	}
	p, _ = imagePullSecretPatch(cur, "")
	raw, _ = json.Marshal(p)
	if string(raw) != `{"repository":"ghcr.io/acme/api","tag":"v3","pullSecret":""}` {
		t.Errorf("clear patch must send an explicit empty pullSecret, got %s", raw)
	}
	if _, err := imagePullSecretPatch([]byte(`{"spec":{"runtime":"dockerfile"}}`), "ghcr.io"); err == nil {
		t.Error("a service without an image must be refused")
	}
}

func TestBuildRegistryLogin(t *testing.T) {
	t.Parallel()
	req, err := buildRegistryLogin("https://ghcr.io", "octo", strings.NewReader("s3cret\n"))
	if err != nil {
		t.Fatal(err)
	}
	if req.Registry != "https://ghcr.io" || req.Username != "octo" || req.Password != "s3cret" {
		t.Errorf("req = %+v", req)
	}
	if _, err := buildRegistryLogin("ghcr.io", "", strings.NewReader("pw")); err == nil {
		t.Error("missing username must fail")
	}
	if _, err := buildRegistryLogin("ghcr.io", "u", strings.NewReader("  \n")); err == nil {
		t.Error("empty stdin password must fail")
	}
}
