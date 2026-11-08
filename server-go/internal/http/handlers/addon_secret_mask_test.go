package handlers

import (
	"context"
	"encoding/json"
	"io"
	"log/slog"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"

	"github.com/go-chi/chi/v5"
	corev1 "k8s.io/api/core/v1"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/apimachinery/pkg/apis/meta/v1/unstructured"
	"k8s.io/apimachinery/pkg/runtime"
	"k8s.io/apimachinery/pkg/runtime/schema"
	dynamicfake "k8s.io/client-go/dynamic/fake"
	"k8s.io/client-go/kubernetes/fake"

	"kuso/server/internal/addons"
	"kuso/server/internal/audit"
	"kuso/server/internal/auth"
	"kuso/server/internal/db"
	"kuso/server/internal/kube"
)

func TestMaskConnValues(t *testing.T) {
	t.Parallel()
	cases := []struct {
		key, value  string
		wantDisplay string
		wantMasked  bool
	}{
		{"DATABASE_URL", "postgres://app:s3cret@p1-pg:5432/app?sslmode=disable", "postgres://app:" + connPasswordMask + "@p1-pg:5432/app?sslmode=disable", true},
		{"REDIS_URL", "redis://p1-redis:6379", "", true},
		{"MONGODB_URI", "mongodb://root:pw@m1:27017,m2:27017/admin", "", true},
		{"POSTGRES_HOST", "p1-pg.kuso.svc", "p1-pg.kuso.svc", false},
		{"POSTGRES_PORT", "5432", "5432", false},
		{"POSTGRES_DB", "app", "app", false},
		{"POSTGRES_USER", "app", "app", false},
		{"S3_BUCKET", "media", "media", false},
		{"S3_SECRET_KEY", "AKIAxxxx/yyyy", "", true},
		{"POSTGRES_PASSWORD", "hunter2", "", true},
		// A coordinate-shaped key still masks a value carrying a password.
		{"POSTGRES_HOST", "postgres://u:pw@h/db", "postgres://u:" + connPasswordMask + "@h/db", true},
		{"PGPASSWORD", "", "", false},
		{"DSN", "postgres://u:pw@h:5432/db?sslpassword=x", "postgres://u:" + connPasswordMask + "@h:5432/db?sslpassword=" + connPasswordMask, true},
	}
	for _, c := range cases {
		display, masked := maskConnValues(map[string]string{c.key: c.value})
		if display[c.key] != c.wantDisplay {
			t.Errorf("%s=%q: display %q, want %q", c.key, c.value, display[c.key], c.wantDisplay)
		}
		if got := len(masked) == 1; got != c.wantMasked {
			t.Errorf("%s=%q: masked=%v, want %v", c.key, c.value, got, c.wantMasked)
		}
		if c.wantMasked && (strings.Contains(display[c.key], "pw") || strings.Contains(display[c.key], "s3cret")) {
			t.Errorf("%s: plaintext leaked in %q", c.key, display[c.key])
		}
	}
}

func TestMaskConnValues_SortedMaskedList(t *testing.T) {
	t.Parallel()
	_, masked := maskConnValues(map[string]string{"Z_PASSWORD": "a", "A_TOKEN": "b", "HOST": "h", "EMPTY_SECRET": ""})
	if strings.Join(masked, ",") != "A_TOKEN,Z_PASSWORD" {
		t.Fatalf("masked = %v", masked)
	}
}

// Masked reads must not audit (the overview polls them); a per-key reveal
// must audit exactly once and name the key, never the value.
func TestAddonSecret_MaskedNotAuditedKeyAudited(t *testing.T) {
	d := openTestDB(t)
	seedUserWithProjectRole(t, d, "pa", "p1", db.ProjectRoleAdmin)

	dyn := dynamicfake.NewSimpleDynamicClientWithCustomListKinds(runtime.NewScheme(), map[schema.GroupVersionResource]string{
		kube.GVRAddons: "KusoAddonList",
	})
	m, err := runtime.DefaultUnstructuredConverter.ToUnstructured(&kube.KusoAddon{
		ObjectMeta: metav1.ObjectMeta{Name: "p1-pg", Namespace: "kuso"},
		Spec:       kube.KusoAddonSpec{Project: "p1", Kind: "postgres"},
	})
	if err != nil {
		t.Fatal(err)
	}
	u := &unstructured.Unstructured{Object: m}
	u.SetGroupVersionKind(kube.GVRAddons.GroupVersion().WithKind("KusoAddon"))
	if err := dyn.Tracker().Create(kube.GVRAddons, u, "kuso"); err != nil {
		t.Fatal(err)
	}
	cs := fake.NewSimpleClientset(&corev1.Secret{
		ObjectMeta: metav1.ObjectMeta{Name: "p1-pg-conn", Namespace: "kuso"},
		Data: map[string][]byte{
			"POSTGRES_HOST":     []byte("p1-pg"),
			"POSTGRES_PASSWORD": []byte("hunter2"),
			"DATABASE_URL":      []byte("postgres://app:hunter2@p1-pg:5432/app"),
		},
	})
	h := &AddonsHandler{Svc: addons.New(&kube.Client{Dynamic: dyn, Clientset: cs}, "kuso"), DB: d,
		Audit:  &audit.Service{DB: d, Enabled: true},
		Logger: slog.New(slog.NewTextHandler(io.Discard, nil))}
	r := chi.NewRouter()
	h.Mount(r)

	get := func(query string) (int, string) {
		req := httptest.NewRequest(http.MethodGet, "/api/projects/p1/addons/pg/secret"+query, nil)
		req = req.WithContext(auth.WithClaimsForTest(context.Background(), &auth.Claims{UserID: "pa"}))
		rec := httptest.NewRecorder()
		r.ServeHTTP(rec, req)
		return rec.Code, rec.Body.String()
	}
	auditRows := func() []string {
		rows, err := d.DB.Query(`SELECT message FROM "Audit" WHERE action = 'addon.secret_read'`)
		if err != nil {
			t.Fatal(err)
		}
		defer rows.Close()
		var out []string
		for rows.Next() {
			var s string
			if err := rows.Scan(&s); err != nil {
				t.Fatal(err)
			}
			out = append(out, s)
		}
		return out
	}

	code, body := get("?masked=1")
	if code != http.StatusOK {
		t.Fatalf("masked: status %d body %s", code, body)
	}
	if strings.Contains(body, "hunter2") {
		t.Fatalf("masked response leaked plaintext: %s", body)
	}
	var resp struct {
		Values map[string]string `json:"values"`
		Masked []string          `json:"masked"`
	}
	if err := json.Unmarshal([]byte(body), &resp); err != nil {
		t.Fatal(err)
	}
	if resp.Values["POSTGRES_HOST"] != "p1-pg" || strings.Join(resp.Masked, ",") != "DATABASE_URL,POSTGRES_PASSWORD" {
		t.Fatalf("masked response = %+v", resp)
	}
	if got := auditRows(); len(got) != 0 {
		t.Fatalf("masked read audited: %v", got)
	}

	code, body = get("?key=POSTGRES_PASSWORD")
	if code != http.StatusOK || !strings.Contains(body, `"POSTGRES_PASSWORD":"hunter2"`) || strings.Contains(body, "DATABASE_URL") {
		t.Fatalf("key: status %d body %s", code, body)
	}
	got := auditRows()
	if len(got) != 1 || !strings.Contains(got[0], "POSTGRES_PASSWORD") || strings.Contains(got[0], "hunter2") {
		t.Fatalf("key read audit rows = %v, want one naming the key without the value", got)
	}

	if code, _ := get("?key=NOPE"); code != http.StatusNotFound {
		t.Fatalf("unknown key: status %d, want 404", code)
	}
	if got := auditRows(); len(got) != 1 {
		t.Fatalf("unknown key audited: %v", got)
	}
}
