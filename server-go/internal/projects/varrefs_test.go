package projects

import (
	"errors"
	"testing"
)

func TestParseVarRef(t *testing.T) {
	cases := []struct {
		name      string
		in        string
		wantRef   VarRef
		wantOK    bool
		wantErrIs error
	}{
		{"empty", "", VarRef{}, false, nil},
		{"plain literal", "hello", VarRef{}, false, nil},
		{"plain literal with curlies", "{not a ref}", VarRef{}, false, nil},
		{"pure ref", "${{ pg.DATABASE_URL }}", VarRef{Name: "pg", Key: "DATABASE_URL"}, true, nil},
		{"pure ref no inner spaces", "${{pg.DATABASE_URL}}", VarRef{Name: "pg", Key: "DATABASE_URL"}, true, nil},
		{"pure ref with hyphen name", "${{ analiz-pg.PGHOST }}", VarRef{Name: "analiz-pg", Key: "PGHOST"}, true, nil},
		{"pure ref with underscore name", "${{ my_redis.REDIS_URL }}", VarRef{Name: "my_redis", Key: "REDIS_URL"}, true, nil},
		{"composite prefix", "prefix-${{ pg.DATABASE_URL }}", VarRef{}, false, ErrCompositeVarRef},
		{"composite suffix", "${{ pg.DATABASE_URL }}-suffix", VarRef{}, false, ErrCompositeVarRef},
		{"composite both sides", "a-${{ pg.URL }}-b", VarRef{}, false, ErrCompositeVarRef},
		{"two refs", "${{ a.A }} ${{ b.B }}", VarRef{}, false, ErrCompositeVarRef},
		{"lowercase key invalid pattern", "${{ pg.url }}", VarRef{}, false, ErrCompositeVarRef},
		{"key starting with digit", "${{ pg.1FOO }}", VarRef{}, false, ErrCompositeVarRef},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			ref, ok, err := ParseVarRef(tc.in)
			if tc.wantErrIs != nil {
				if !errors.Is(err, tc.wantErrIs) {
					t.Fatalf("err: got %v, want %v", err, tc.wantErrIs)
				}
				return
			}
			if err != nil {
				t.Fatalf("unexpected err: %v", err)
			}
			if ok != tc.wantOK {
				t.Errorf("ok: got %v, want %v", ok, tc.wantOK)
			}
			if ref != tc.wantRef {
				t.Errorf("ref: got %+v, want %+v", ref, tc.wantRef)
			}
		})
	}
}

func TestVarRef_SecretName(t *testing.T) {
	if got := (VarRef{Name: "pg"}).SecretName(); got != "pg-conn" {
		t.Errorf("got %q, want %q", got, "pg-conn")
	}
}

func TestRewriteEnvVar_Literal(t *testing.T) {
	in := EnvVar{Name: "FOO", Value: "bar"}
	got, err := RewriteEnvVar(in, nil, nil)
	if err != nil {
		t.Fatalf("err: %v", err)
	}
	if got.Name != "FOO" || got.Value != "bar" || got.ValueFrom != nil {
		t.Errorf("got %+v, want %+v", got, in)
	}
}

func TestRewriteEnvVar_AddonRef(t *testing.T) {
	in := EnvVar{Name: "DATABASE_URL", Value: "${{ pg.DATABASE_URL }}"}
	got, err := RewriteEnvVar(in, nil, nil)
	if err != nil {
		t.Fatalf("err: %v", err)
	}
	if got.Value != "" {
		t.Errorf("got value %q, want empty", got.Value)
	}
	if got.ValueFrom == nil {
		t.Fatal("got nil ValueFrom")
	}
	skr, ok := got.ValueFrom["secretKeyRef"].(map[string]any)
	if !ok {
		t.Fatalf("got valueFrom %+v, want secretKeyRef map", got.ValueFrom)
	}
	if skr["name"] != "pg-conn" {
		t.Errorf("name: got %v, want pg-conn", skr["name"])
	}
	if skr["key"] != "DATABASE_URL" {
		t.Errorf("key: got %v, want DATABASE_URL", skr["key"])
	}
}

// TestRewriteEnvVar_ServiceRef covers the Railway-style reference path:
// `${{api.HOST}}` resolves to a literal DNS string when the resolver
// recognises "api" as a service in the project. Also exercises the
// kuso PUBLIC_* extension (browser-reachable URL via the production
// env's host + TLS flag).
func TestRewriteEnvVar_ServiceRef(t *testing.T) {
	resolver := func(name string) (ServiceRef, bool) {
		if name == "api" {
			return ServiceRef{
				FQN:        "myproj-api",
				Port:       8080,
				NS:         "kuso",
				PublicHost: "api.myproj.kuso.sislelabs.com",
				PublicTLS:  true,
			}, true
		}
		return ServiceRef{}, false
	}
	cases := []struct {
		key  string
		want string
	}{
		{"HOST", "myproj-api.kuso.svc.cluster.local"},
		{"PORT", "8080"},
		{"URL", "http://myproj-api.kuso.svc.cluster.local:8080"},
		{"INTERNAL_URL", "http://myproj-api.kuso.svc.cluster.local:8080"},
		{"PUBLIC_HOST", "api.myproj.kuso.sislelabs.com"},
		{"PUBLIC_URL", "https://api.myproj.kuso.sislelabs.com"},
	}
	for _, tc := range cases {
		t.Run(tc.key, func(t *testing.T) {
			in := EnvVar{Name: "X", Value: "${{ api." + tc.key + " }}"}
			got, err := RewriteEnvVar(in, resolver, nil)
			if err != nil {
				t.Fatalf("err: %v", err)
			}
			if got.ValueFrom != nil {
				t.Errorf("got valueFrom %+v, want literal", got.ValueFrom)
			}
			if got.Value != tc.want {
				t.Errorf("got %q, want %q", got.Value, tc.want)
			}
		})
	}
}

// TestRewriteEnvVar_ServiceRef_PublicURL_NoIngress asserts that a
// service without a public host resolves PUBLIC_URL to empty rather
// than falling back to the in-cluster URL. A frontend pointing at
// `${{ worker.PUBLIC_URL }}` would silently hit a closed door — the
// empty string makes the misconfig visible at first request.
func TestRewriteEnvVar_ServiceRef_PublicURL_NoIngress(t *testing.T) {
	resolver := func(name string) (ServiceRef, bool) {
		if name == "worker" {
			return ServiceRef{FQN: "myproj-worker", Port: 8080, NS: "kuso"}, true
		}
		return ServiceRef{}, false
	}
	in := EnvVar{Name: "X", Value: "${{ worker.PUBLIC_URL }}"}
	got, err := RewriteEnvVar(in, resolver, nil)
	if err != nil {
		t.Fatalf("err: %v", err)
	}
	if got.Value != "" {
		t.Errorf("got %q, want empty (worker has no ingress)", got.Value)
	}
}

// TestRewriteEnvVar_ServiceRef_FallsBackToAddon ensures that a
// service-style key (HOST etc.) for a name that DOES NOT resolve as a
// service falls through to the addon path. Lets users reference an
// addon named "host" if they really want to (uncommon, but legal).
func TestRewriteEnvVar_ServiceRef_FallsBackToAddon(t *testing.T) {
	noServices := func(string) (ServiceRef, bool) { return ServiceRef{}, false }
	in := EnvVar{Name: "X", Value: "${{ pg.HOST }}"}
	got, err := RewriteEnvVar(in, noServices, nil)
	if err != nil {
		t.Fatalf("err: %v", err)
	}
	if got.ValueFrom == nil {
		t.Fatalf("got %+v, want secretKeyRef", got)
	}
}

func TestRewriteEnvVar_Composite(t *testing.T) {
	in := EnvVar{Name: "URL", Value: "https://${{ pg.HOST }}/db"}
	_, err := RewriteEnvVar(in, nil, nil)
	if !errors.Is(err, ErrCompositeVarRef) {
		t.Errorf("got %v, want ErrCompositeVarRef", err)
	}
}

func TestRewriteEnvVar_PassthroughValueFrom(t *testing.T) {
	in := EnvVar{
		Name: "X",
		ValueFrom: map[string]any{
			"secretKeyRef": map[string]any{"name": "other", "key": "K"},
		},
	}
	got, err := RewriteEnvVar(in, nil, nil)
	if err != nil {
		t.Fatalf("err: %v", err)
	}
	if got.Name != "X" || got.ValueFrom == nil {
		t.Errorf("got %+v", got)
	}
}

func TestRewriteEnvVars_Multiple(t *testing.T) {
	in := []EnvVar{
		{Name: "A", Value: "literal"},
		{Name: "B", Value: "${{ pg.URL }}"},
		{Name: "C", Value: ""},
	}
	out, err := RewriteEnvVars(in, nil, nil)
	if err != nil {
		t.Fatalf("err: %v", err)
	}
	if len(out) != 3 {
		t.Fatalf("got len %d, want 3", len(out))
	}
	if out[0].Value != "literal" || out[0].ValueFrom != nil {
		t.Errorf("idx 0: got %+v", out[0])
	}
	if out[1].ValueFrom == nil {
		t.Errorf("idx 1: got %+v, want valueFrom set", out[1])
	}
	if out[2].Value != "" {
		t.Errorf("idx 2: got %+v", out[2])
	}
}

func TestFormatVarRef(t *testing.T) {
	got := FormatVarRef("pg", "DATABASE_URL")
	want := "${{ pg.DATABASE_URL }}"
	if got != want {
		t.Errorf("got %q, want %q", got, want)
	}
}
