package kusoCli

import (
	"encoding/json"
	"io"
	"net/http"
	"net/http/httptest"
	"os"
	"strings"
	"testing"

	"kuso/pkg/kusoApi"
)

// captureStdout runs fn with os.Stdout redirected to a pipe and returns
// everything it wrote. The table renderer writes straight to os.Stdout, so
// this is how we assert on rendered rows.
func captureStdout(t *testing.T, fn func()) string {
	t.Helper()
	orig := os.Stdout
	r, w, err := os.Pipe()
	if err != nil {
		t.Fatalf("pipe: %v", err)
	}
	os.Stdout = w
	done := make(chan string, 1)
	go func() {
		b, _ := io.ReadAll(r)
		done <- string(b)
	}()
	fn()
	_ = w.Close()
	os.Stdout = orig
	return <-done
}

// TestServerSharedKeyCount covers the truthful-count helper used by the
// share/unshare commands.
func TestServerSharedKeyCount(t *testing.T) {
	cases := []struct {
		name     string
		body     string
		fallback int
		want     int
	}{
		{"decodes server list", `{"spec":{"sharedEnvKeys":["A","B","C"]}}`, 99, 3},
		{"empty list is zero, not fallback", `{"spec":{"sharedEnvKeys":[]}}`, 99, 0},
		{"missing field falls back", `{"spec":{}}`, 7, 7},
		{"garbage falls back", `not json`, 5, 5},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			if got := serverSharedKeyCount([]byte(tc.body), tc.fallback); got != tc.want {
				t.Errorf("serverSharedKeyCount(%q, %d) = %d, want %d", tc.body, tc.fallback, got, tc.want)
			}
		})
	}
}

// TestEnvUnset_OnlyDeletesNamedKey guards that `kuso env unset` removes
// ONLY the named var and never touches the others. It now issues a per-key
// DELETE /env-vars/{name} (which the server's UnsetEnvVar handles for every
// form, incl. managed secrets) rather than a read-modify-write bulk POST
// that couldn't remove a managed secret and risked dropping surviving vars.
// Structurally, only the named key is ever mentioned.
func TestEnvUnset_OnlyDeletesNamedKey(t *testing.T) {
	var deleted []string
	var sawBulkPost bool
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		switch {
		case r.Method == http.MethodDelete && strings.Contains(r.URL.Path, "/env-vars/"):
			parts := strings.Split(r.URL.Path, "/env-vars/")
			deleted = append(deleted, parts[len(parts)-1])
			w.WriteHeader(http.StatusOK)
			_, _ = io.WriteString(w, `{"kind":"KusoService"}`)
		case r.Method == http.MethodPost && strings.HasSuffix(r.URL.Path, "/env"):
			sawBulkPost = true
			w.WriteHeader(http.StatusOK)
		default:
			w.WriteHeader(http.StatusOK)
			_, _ = io.WriteString(w, `{"envVars":[]}`)
		}
	}))
	defer srv.Close()

	api = &kusoApi.KusoClient{}
	api.Init(srv.URL, "test-token")
	defer func() { api = nil }()

	// `env unset` is gated by confirmDestructive, which fails closed off
	// a TTY (this test process has no terminal). Opt in the same way a
	// script would, with --yes: this test is about WHICH keys get
	// deleted, not about the prompt. The guard itself is covered in
	// confirm_test.go and destructive_guard_sweep_test.go.
	origYes := envUnsetYes
	envUnsetYes = true
	t.Cleanup(func() { envUnsetYes = origYes })

	envUnsetCmd.SetArgs([]string{"alpha", "web", "DROP_ME"})
	if err := envUnsetCmd.RunE(envUnsetCmd, []string{"alpha", "web", "DROP_ME"}); err != nil {
		t.Fatalf("unset RunE: %v", err)
	}
	if len(deleted) != 1 || deleted[0] != "DROP_ME" {
		t.Fatalf("expected exactly one DELETE of DROP_ME, got %v", deleted)
	}
	if sawBulkPost {
		t.Error("env unset must NOT bulk-POST the env list (can't remove managed secrets; risks dropping survivors)")
	}
}

// TestEnvSet_SingleVarPUT covers both single-var PUT shapes:
//
//   - Secret: `kuso env set … --secret` sends {"secretValue":"…"} (so the
//     value lands in the kuso-managed <service>-secrets Secret) rather than
//     {"value":"…"} on the bulk POST.
//   - Auto: the default `kuso env set` (no --secret, no --env) uses the
//     UNIFIED write, {value, auto:true}, so the SERVER decides storage. It
//     must NOT fall back to the old read-merge-whole-list POST, and must not
//     send secretValue/secretRef.
func TestEnvSet_SingleVarPUT(t *testing.T) {
	for _, tc := range []struct {
		name     string
		secret   bool
		arg      string
		wantPath string
		want     map[string]any
		absent   []string
	}{
		{"Secret", true, "WETRAVEL_API_KEY=s3cr3t", "/api/projects/alpha/services/web/env-vars/WETRAVEL_API_KEY",
			map[string]any{"secretValue": "s3cr3t"}, []string{"value"}},
		{"Auto", false, "FEATURE_X=1", "/api/projects/alpha/services/web/env-vars/FEATURE_X",
			map[string]any{"auto": true, "value": "1"}, []string{"secretValue", "secretRef"}},
	} {
		t.Run(tc.name, func(t *testing.T) {
			var (
				putPath string
				method  string
				body    map[string]any
			)
			srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
				method = r.Method
				if r.Method != http.MethodPut {
					http.Error(w, "expected PUT to the single-var endpoint, got "+r.Method, 405)
					return
				}
				putPath = r.URL.Path
				defer r.Body.Close()
				if err := json.NewDecoder(r.Body).Decode(&body); err != nil {
					http.Error(w, err.Error(), 400)
					return
				}
				w.WriteHeader(http.StatusOK)
				_, _ = io.WriteString(w, "{}")
			}))
			defer srv.Close()

			api = &kusoApi.KusoClient{}
			api.Init(srv.URL, "test-token")
			defer func() { api = nil }()
			// Package-level flags: set for this row, reset so state doesn't leak.
			envSecretFlag = tc.secret
			envScopeFlag = ""
			defer func() { envSecretFlag = false }()

			if err := envSetCmd.RunE(envSetCmd, []string{"alpha", "web", tc.arg}); err != nil {
				t.Fatalf("set RunE: %v", err)
			}
			if method != http.MethodPut {
				t.Fatalf("expected a PUT (single-var write), got %q", method)
			}
			if putPath != tc.wantPath {
				t.Errorf("PUT path = %q, want %q", putPath, tc.wantPath)
			}
			for k, v := range tc.want {
				if body[k] != v {
					t.Errorf("body %s = %v, want %v: %+v", k, body[k], v, body)
				}
			}
			for _, k := range tc.absent {
				if _, has := body[k]; has {
					t.Errorf("body must not carry %s: %+v", k, body)
				}
			}
		})
	}
}

// TestEnvSet_SecretRejectsEnvScope guards the incompatibility: --secret is
// a service-level write; the per-env override path has no secretValue wire
// field, so combining them must error rather than silently drop the value.
func TestEnvSet_SecretRejectsEnvScope(t *testing.T) {
	api = &kusoApi.KusoClient{}
	api.Init("http://127.0.0.1:0", "test-token")
	defer func() { api = nil }()
	envSecretFlag = true
	envScopeFlag = "production"
	defer func() { envSecretFlag = false; envScopeFlag = "" }()

	err := envSetCmd.RunE(envSetCmd, []string{"alpha", "web", "K=v"})
	if err == nil {
		t.Fatal("expected --secret + --env to error, got nil")
	}
}

// TestEnvList_RevealHitsRevealURL asserts `env list --reveal` calls the
// ?reveal=true URL (and that the flag is off by default → plain /env).
func TestEnvList_RevealHitsRevealURL(t *testing.T) {
	var gotRawQuery string
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		gotRawQuery = r.URL.RawQuery
		w.Header().Set("Content-Type", "application/json")
		_, _ = io.WriteString(w, `{"envVars":[],"revealed":true}`)
	}))
	defer srv.Close()

	api = &kusoApi.KusoClient{}
	api.Init(srv.URL, "test-token")
	defer func() { api = nil }()

	// reveal on → ?reveal=true
	envRevealFlag = true
	defer func() { envRevealFlag = false }()
	_ = captureStdout(t, func() {
		if err := envListCmd.RunE(envListCmd, []string{"alpha", "web"}); err != nil {
			t.Fatalf("list --reveal RunE: %v", err)
		}
	})
	if gotRawQuery != "reveal=true" {
		t.Errorf("reveal query = %q, want reveal=true", gotRawQuery)
	}

	// reveal off → no reveal query param
	envRevealFlag = false
	_ = captureStdout(t, func() {
		if err := envListCmd.RunE(envListCmd, []string{"alpha", "web"}); err != nil {
			t.Fatalf("list RunE: %v", err)
		}
	})
	if strings.Contains(gotRawQuery, "reveal") {
		t.Errorf("non-reveal list must not send reveal query, got %q", gotRawQuery)
	}
}

// TestEnvList_Rendering covers the one-secret-primitive TYPE column.
func TestEnvList_Rendering(t *testing.T) {
	for _, tc := range []struct {
		name    string
		getBody string
		reveal  bool
		rows    [][]string // each: cells that must share one rendered row
		absent  []string
	}{
		// plain literal → "env", managed secret → "secret" (masked),
		// secretKeyRef → "ref" with a "→ name.key" wiring value when NOT revealed.
		{
			name: "TypeClassification",
			getBody: `{"masked":false,"revealed":false,"envVars":[
				{"name":"PLAIN_ONE","value":"hello"},
				{"name":"MANAGED_ONE","source":"managed-secret","value":""},
				{"name":"REF_ONE","valueFrom":{"secretKeyRef":{"name":"db-conn","key":"DATABASE_URL"}}}
			]}`,
			rows: [][]string{
				{"PLAIN_ONE", "hello", "env"},
				{"MANAGED_ONE", "•••••", "secret"},
				{"REF_ONE", "→ db-conn.DATABASE_URL", "ref"},
			},
		},
		// When the server reports revealed:true the real resolved value is
		// printed for managed secrets and refs, not the mask / wiring form.
		{
			name: "RevealedValues",
			getBody: `{"masked":false,"revealed":true,"envVars":[
				{"name":"MANAGED_ONE","source":"managed-secret","value":"s3cr3t-plain"},
				{"name":"REF_ONE","value":"postgres://resolved","valueFrom":{"secretKeyRef":{"name":"db-conn","key":"DATABASE_URL"}}}
			]}`,
			reveal: true,
			rows: [][]string{
				{"MANAGED_ONE", "s3cr3t-plain", "secret"},
				{"REF_ONE", "postgres://resolved", "ref"},
			},
			absent: []string{"•••••", "→ "},
		},
	} {
		t.Run(tc.name, func(t *testing.T) {
			srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
				w.Header().Set("Content-Type", "application/json")
				_, _ = io.WriteString(w, tc.getBody)
			}))
			defer srv.Close()

			api = &kusoApi.KusoClient{}
			api.Init(srv.URL, "test-token")
			defer func() { api = nil }()
			envRevealFlag = tc.reveal
			defer func() { envRevealFlag = false }()

			out := captureStdout(t, func() {
				if err := envListCmd.RunE(envListCmd, []string{"alpha", "web"}); err != nil {
					t.Fatalf("list RunE: %v", err)
				}
			})
			for _, row := range tc.rows {
				if !rowHas(out, row...) {
					t.Errorf("no row with %q:\n%s", row, out)
				}
			}
			for _, s := range tc.absent {
				if strings.Contains(out, s) {
					t.Errorf("output still contains sentinel %q:\n%s", s, out)
				}
			}
		})
	}
}

// TestEnvList_AddonSecretShowsSource covers addon-supplied keys: the server
// sends them with no value, so a blank VALUE cell read as "unset" and invited
// an `env set` that shadows the addon secret. They must name their addon,
// and -o json must pass the addon through.
func TestEnvList_AddonSecretShowsSource(t *testing.T) {
	getBody := `{"masked":false,"revealed":false,"envVars":[
		{"name":"REDIS_HOST","source":"addon-secret","addon":"cache"}
	]}`
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", "application/json")
		_, _ = io.WriteString(w, getBody)
	}))
	defer srv.Close()

	api = &kusoApi.KusoClient{}
	api.Init(srv.URL, "test-token")
	defer func() { api = nil }()
	envRevealFlag = false

	out := captureStdout(t, func() {
		if err := envListCmd.RunE(envListCmd, []string{"alpha", "web"}); err != nil {
			t.Fatalf("list RunE: %v", err)
		}
	})
	if !rowHas(out, "REDIS_HOST", "(from addon cache)", "addon") {
		t.Errorf("addon key not rendered with its source:\n%s", out)
	}

	outputFormat = "json"
	defer func() { outputFormat = "table" }()
	out = captureStdout(t, func() {
		if err := envListCmd.RunE(envListCmd, []string{"alpha", "web"}); err != nil {
			t.Fatalf("list -o json RunE: %v", err)
		}
	})
	if !strings.Contains(out, `"addon": "cache"`) {
		t.Errorf("-o json dropped the addon field:\n%s", out)
	}
}

// rowHas checks the rendered table contains a single row mentioning all the
// given cell substrings. tablewriter pads/uppercases nothing in cells, so a
// per-line substring scan is enough.
func rowHas(out string, cells ...string) bool {
	for _, line := range strings.Split(out, "\n") {
		ok := true
		for _, c := range cells {
			if !strings.Contains(line, c) {
				ok = false
				break
			}
		}
		if ok {
			return true
		}
	}
	return false
}

// A 404 from the per-key DELETE used to be read as "already absent" even
// when the service itself didn't exist, so `env unset` on a typo'd service
// printed success.
func TestEnvUnset_MissingServiceErrors(t *testing.T) {
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.WriteHeader(http.StatusNotFound)
		_, _ = io.WriteString(w, `{"error":"service not found"}`)
	}))
	defer srv.Close()
	api = &kusoApi.KusoClient{}
	api.Init(srv.URL, "test-token")
	defer func() { api = nil }()
	origYes := envUnsetYes
	envUnsetYes = true
	t.Cleanup(func() { envUnsetYes = origYes })

	err := envUnsetCmd.RunE(envUnsetCmd, []string{"alpha", "wbe", "KEY"})
	if err == nil || !strings.Contains(err.Error(), "not found") {
		t.Fatalf("want service-not-found error, got %v", err)
	}
}

func TestEnvUnset_MissingKeyOnExistingServiceIsNotAnError(t *testing.T) {
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.Method == http.MethodDelete {
			w.WriteHeader(http.StatusNotFound)
			_, _ = io.WriteString(w, `{"error":"not found: env var \"KEY\""}`)
			return
		}
		_, _ = io.WriteString(w, `{"metadata":{"name":"alpha-web"}}`)
	}))
	defer srv.Close()
	api = &kusoApi.KusoClient{}
	api.Init(srv.URL, "test-token")
	defer func() { api = nil }()
	origYes := envUnsetYes
	envUnsetYes = true
	t.Cleanup(func() { envUnsetYes = origYes })

	if err := envUnsetCmd.RunE(envUnsetCmd, []string{"alpha", "web", "KEY"}); err != nil {
		t.Fatalf("missing key should stay idempotent, got %v", err)
	}
}
