package kusoCli

import (
	"encoding/json"
	"io"
	"net/http"
	"net/http/httptest"
	"testing"

	"kuso/pkg/kusoApi"
)

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
