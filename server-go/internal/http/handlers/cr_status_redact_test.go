package handlers

import (
	"net/http/httptest"
	"os"
	"path/filepath"
	"regexp"
	"strings"
	"testing"

	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"

	"kuso/server/internal/kube"
)

// The helm-operator writes the full rendered release into
// status.deployedRelease.manifest: conn Secrets (base64 DSNs) and every
// literal env value. Viewers can list CRs, so any response carrying that
// field hands them credentials behind the secrets:read gate. These tests
// pin writeJSON as the choke point that removes it, whatever CR type or
// wrapper shape a handler serialises.

const leakedManifest = "kind: Secret\ndata:\n  DATABASE_URL: cG9zdGdyZXM6Ly9zdXBlcjpodW50ZXIyQGRi\n"

func leakyMeta() metav1.ObjectMeta {
	return metav1.ObjectMeta{
		Name: "x",
		ManagedFields: []metav1.ManagedFieldsEntry{
			{Manager: "kuso-server", Operation: metav1.ManagedFieldsOperationUpdate},
		},
	}
}

func leakyStatus() map[string]any {
	return map[string]any{
		"conditions": []any{map[string]any{"type": "Deployed", "status": "True"}},
		"deployedRelease": map[string]any{
			"name":     "x",
			"manifest": leakedManifest,
		},
	}
}

func TestWriteJSON_StripsReleaseManifestAndManagedFields(t *testing.T) {
	t.Parallel()

	project := kube.KusoProject{ObjectMeta: leakyMeta(), Status: leakyStatus()}
	service := kube.KusoService{ObjectMeta: leakyMeta(), Status: leakyStatus()}
	env := kube.KusoEnvironment{ObjectMeta: leakyMeta(), Status: leakyStatus()}
	addon := kube.KusoAddon{ObjectMeta: leakyMeta(), Status: leakyStatus()}
	build := kube.KusoBuild{ObjectMeta: leakyMeta(), Status: leakyStatus()}
	cron := kube.KusoCron{ObjectMeta: leakyMeta(), Status: leakyStatus()}
	run := kube.KusoRun{ObjectMeta: leakyMeta(), Status: leakyStatus()}

	cases := map[string]any{
		"project":         project,
		"service ptr":     &service,
		"env slice":       []kube.KusoEnvironment{env},
		"addon ptr slice": []*kube.KusoAddon{&addon},
		"build":           build,
		"cron":            cron,
		"run":             run,
		"nested wrapper": map[string]any{
			"project": &project,
			"envs":    []kube.KusoEnvironment{env},
			"extra":   struct{ Addons []kube.KusoAddon }{[]kube.KusoAddon{addon}},
		},
	}
	for name, v := range cases {
		t.Run(name, func(t *testing.T) {
			rec := httptest.NewRecorder()
			writeJSON(rec, 200, v)
			body := rec.Body.String()
			if strings.Contains(body, "cG9zdGdyZXM6") || strings.Contains(body, `"manifest"`) {
				t.Errorf("deployedRelease.manifest leaked: %s", body)
			}
			if strings.Contains(body, "managedFields") {
				t.Errorf("metadata.managedFields leaked: %s", body)
			}
			// The web canvas reads status.conditions for readiness.
			if !strings.Contains(body, `"Deployed"`) {
				t.Errorf("stripping removed status.conditions: %s", body)
			}
		})
	}

	// writeJSON must not mutate the caller's object: handlers pass
	// informer-cache pointers and drift detection reads managedFields.
	if len(service.ManagedFields) == 0 {
		t.Error("writeJSON cleared managedFields on the caller's object")
	}
	if _, ok := service.Status["deployedRelease"].(map[string]any)["manifest"]; !ok {
		t.Error("writeJSON deleted the manifest from the caller's object")
	}
}

// Every handler response must go through writeJSON so the stripping
// above applies. A handler that encodes straight onto the
// ResponseWriter bypasses it; the allowlist holds the ones that never
// carry CRs.
func TestHandlersEncodeJSONOnlyViaWriteJSON(t *testing.T) {
	t.Parallel()
	allowed := map[string]bool{
		"projects.go":       true, // writeJSON itself
		"auth.go":           true, // login/token payloads
		"import_coolify.go": true, // import summary counts
		"export.go":         true, // import summary counts; the tar export is admin-only by design
	}
	direct := regexp.MustCompile(`json\.NewEncoder\(w\)|json\.NewEncoder\(rw\)`)
	files, err := filepath.Glob("*.go")
	if err != nil {
		t.Fatal(err)
	}
	for _, f := range files {
		if strings.HasSuffix(f, "_test.go") || allowed[f] {
			continue
		}
		src, err := os.ReadFile(f)
		if err != nil {
			t.Fatal(err)
		}
		if direct.Match(src) {
			t.Errorf("%s encodes JSON directly onto the ResponseWriter; use writeJSON so CR status is stripped", f)
		}
	}
}
