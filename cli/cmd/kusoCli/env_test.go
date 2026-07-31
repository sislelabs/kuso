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

// TestEnvUnset_PreservesValueFrom is the regression test for the data-loss
// bug: `kuso env unset` must NOT drop secret-backed (valueFrom) env vars
// when removing an unrelated plain var. Before the fix it rebuilt every
// surviving entry as {name,value}, emitting value:nil for secretKeyRef vars,
// which the server then pruned — silently deleting every secret-backed var.
func TestEnvUnset_PreservesValueFrom(t *testing.T) {
	// The service currently has: a plain var (DROP_ME), a plain var to keep
	// (KEEP_PLAIN), and a secret-ref var (KEEP_SECRET via valueFrom).
	getBody := `{"envVars":[
		{"name":"DROP_ME","value":"x"},
		{"name":"KEEP_PLAIN","value":"y"},
		{"name":"KEEP_SECRET","valueFrom":{"secretKeyRef":{"name":"some-conn","key":"S3_ACCESS_KEY_ID"}}}
	]}`

	var posted kusoApi.SetEnvRequest
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		switch {
		case r.Method == http.MethodGet:
			w.Header().Set("Content-Type", "application/json")
			_, _ = io.WriteString(w, getBody)
		case r.Method == http.MethodPost:
			defer r.Body.Close()
			if err := json.NewDecoder(r.Body).Decode(&posted); err != nil {
				http.Error(w, err.Error(), 400)
				return
			}
			w.WriteHeader(http.StatusNoContent)
		default:
			http.Error(w, "unexpected", 405)
		}
	}))
	defer srv.Close()

	api = &kusoApi.KusoClient{}
	api.Init(srv.URL, "test-token")
	defer func() { api = nil }()

	envUnsetCmd.SetArgs([]string{"alpha", "web", "DROP_ME"})
	if err := envUnsetCmd.RunE(envUnsetCmd, []string{"alpha", "web", "DROP_ME"}); err != nil {
		t.Fatalf("unset RunE: %v", err)
	}

	// The POSTed env list must contain KEEP_PLAIN and KEEP_SECRET (with its
	// valueFrom intact) and must NOT contain DROP_ME.
	names := map[string]map[string]any{}
	for _, e := range posted.EnvVars {
		names[asString(e["name"])] = e
	}
	if _, gone := names["DROP_ME"]; gone {
		t.Error("DROP_ME should have been removed")
	}
	if _, ok := names["KEEP_PLAIN"]; !ok {
		t.Error("KEEP_PLAIN should survive")
	}
	secret, ok := names["KEEP_SECRET"]
	if !ok {
		t.Fatal("KEEP_SECRET (secret-backed) was dropped — the valueFrom data-loss bug")
	}
	if secret["valueFrom"] == nil {
		t.Errorf("KEEP_SECRET lost its valueFrom: %+v", secret)
	}
}

// TestEnvSet_Secret verifies `kuso env set … --secret` sends
// {"secretValue":"…"} to the single-var PUT (so the value lands in the
// kuso-managed <service>-secrets Secret) rather than {"value":"…"} on the
// bulk POST.
func TestEnvSet_Secret(t *testing.T) {
	var (
		putPath string
		body    map[string]any
	)
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
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
	// Reset the package-level flag so state doesn't leak across tests.
	envSecretFlag = true
	defer func() { envSecretFlag = false }()

	args := []string{"alpha", "web", "WETRAVEL_API_KEY=s3cr3t"}
	if err := envSetCmd.RunE(envSetCmd, args); err != nil {
		t.Fatalf("set --secret RunE: %v", err)
	}

	if want := "/api/projects/alpha/services/web/env-vars/WETRAVEL_API_KEY"; putPath != want {
		t.Errorf("PUT path = %q, want %q", putPath, want)
	}
	// Must send secretValue, NOT value.
	if _, hasValue := body["value"]; hasValue {
		t.Errorf("body carried plaintext value; want secretValue only: %+v", body)
	}
	sv, ok := body["secretValue"]
	if !ok {
		t.Fatalf("body missing secretValue: %+v", body)
	}
	if sv != "s3cr3t" {
		t.Errorf("secretValue = %v, want s3cr3t", sv)
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

// TestEnvSet_Auto verifies the default `kuso env set` (no --secret, no --env)
// now uses the UNIFIED write: a single-var PUT carrying {value, auto:true} so
// the SERVER decides storage. It must NOT fall back to the old
// read-merge-whole-list POST, and must not send secretValue/secretRef.
func TestEnvSet_Auto(t *testing.T) {
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
	// Ensure the default path (both scoping flags off).
	envSecretFlag = false
	envScopeFlag = ""

	args := []string{"alpha", "web", "FEATURE_X=1"}
	if err := envSetCmd.RunE(envSetCmd, args); err != nil {
		t.Fatalf("set (auto) RunE: %v", err)
	}

	if method != http.MethodPut {
		t.Fatalf("expected a PUT (single-var unified write), got %q", method)
	}
	if want := "/api/projects/alpha/services/web/env-vars/FEATURE_X"; putPath != want {
		t.Errorf("PUT path = %q, want %q", putPath, want)
	}
	if body["auto"] != true {
		t.Errorf("body missing auto:true: %+v", body)
	}
	if body["value"] != "1" {
		t.Errorf("body value = %v, want \"1\"", body["value"])
	}
	if _, has := body["secretValue"]; has {
		t.Errorf("auto write must not send secretValue: %+v", body)
	}
	if _, has := body["secretRef"]; has {
		t.Errorf("auto write must not send secretRef: %+v", body)
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

// TestEnvList_TypeClassification covers the one-secret-primitive TYPE column:
// plain literal → "env", managed secret → "secret", secretKeyRef → "ref"
// with a "→ name.key" wiring value when NOT revealed.
func TestEnvList_TypeClassification(t *testing.T) {
	getBody := `{"masked":false,"revealed":false,"envVars":[
		{"name":"PLAIN_ONE","value":"hello"},
		{"name":"MANAGED_ONE","source":"managed-secret","value":""},
		{"name":"REF_ONE","valueFrom":{"secretKeyRef":{"name":"db-conn","key":"DATABASE_URL"}}}
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

	// Plain literal → env
	if !rowHas(out, "PLAIN_ONE", "hello", "env") {
		t.Errorf("PLAIN_ONE not rendered as env with value 'hello':\n%s", out)
	}
	// Managed secret → secret, value masked
	if !rowHas(out, "MANAGED_ONE", "•••••", "secret") {
		t.Errorf("MANAGED_ONE not rendered as masked secret:\n%s", out)
	}
	// Ref → ref, value shows wiring "→ name.key"
	if !rowHas(out, "REF_ONE", "→ db-conn.DATABASE_URL", "ref") {
		t.Errorf("REF_ONE not rendered as ref with wiring target:\n%s", out)
	}
}

// TestEnvList_RevealedValues asserts that when the server reports revealed:true
// the real resolved value is printed in the VALUE column for managed secrets
// and refs (not the mask / wiring form).
func TestEnvList_RevealedValues(t *testing.T) {
	getBody := `{"masked":false,"revealed":true,"envVars":[
		{"name":"MANAGED_ONE","source":"managed-secret","value":"s3cr3t-plain"},
		{"name":"REF_ONE","value":"postgres://resolved","valueFrom":{"secretKeyRef":{"name":"db-conn","key":"DATABASE_URL"}}}
	]}`
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", "application/json")
		_, _ = io.WriteString(w, getBody)
	}))
	defer srv.Close()

	api = &kusoApi.KusoClient{}
	api.Init(srv.URL, "test-token")
	defer func() { api = nil }()
	envRevealFlag = true
	defer func() { envRevealFlag = false }()

	out := captureStdout(t, func() {
		if err := envListCmd.RunE(envListCmd, []string{"alpha", "web"}); err != nil {
			t.Fatalf("list --reveal RunE: %v", err)
		}
	})

	if !rowHas(out, "MANAGED_ONE", "s3cr3t-plain", "secret") {
		t.Errorf("revealed managed secret didn't show plaintext value:\n%s", out)
	}
	if !rowHas(out, "REF_ONE", "postgres://resolved", "ref") {
		t.Errorf("revealed ref didn't show resolved value:\n%s", out)
	}
	// The masked / wiring sentinels must NOT appear when revealed.
	if strings.Contains(out, "•••••") || strings.Contains(out, "→ ") {
		t.Errorf("revealed output still contains a mask/wiring sentinel:\n%s", out)
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
