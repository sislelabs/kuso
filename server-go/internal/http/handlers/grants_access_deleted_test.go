package handlers_test

import (
	"context"
	"encoding/json"
	"log/slog"
	"net/http"
	"testing"
	"time"

	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/apimachinery/pkg/apis/meta/v1/unstructured"
	"k8s.io/apimachinery/pkg/runtime"
	"k8s.io/apimachinery/pkg/runtime/schema"
	dynamicfake "k8s.io/client-go/dynamic/fake"

	"kuso/server/internal/auth"
	httpsrv "kuso/server/internal/http"
	"kuso/server/internal/kube"
)

// A grant left behind by a project deleted before delete cleaned up
// grants must not show in the user's access view as a live project.
func TestGetUserAccess_SkipsDeletedProjects(t *testing.T) {
	d := openHandlerTestDB(t)
	iss, _ := auth.NewIssuer("test-secret", time.Hour)
	dyn := dynamicfake.NewSimpleDynamicClientWithCustomListKinds(runtime.NewScheme(), map[schema.GroupVersionResource]string{
		kube.GVRProjects: "KusoProjectList",
	})
	m, _ := runtime.DefaultUnstructuredConverter.ToUnstructured(&kube.KusoProject{
		ObjectMeta: metav1.ObjectMeta{Name: "alive", Namespace: "kuso"},
	})
	u := &unstructured.Unstructured{Object: m}
	u.SetGroupVersionKind(kube.GVRProjects.GroupVersion().WithKind("KusoProject"))
	if err := dyn.Tracker().Create(kube.GVRProjects, u, "kuso"); err != nil {
		t.Fatal(err)
	}
	r := httpsrv.NewRouter(httpsrv.Deps{DB: d, Issuer: iss, Logger: slog.Default(), Kube: &kube.Client{Dynamic: dyn}, Namespace: "kuso"})

	seedGrantsUser(t, d, "alice")
	ctx := context.Background()
	for _, p := range []string{"alive", "gone"} {
		if _, err := d.AddProjectGrant(ctx, p, "alice", "", ""); err != nil {
			t.Fatalf("grant %s: %v", p, err)
		}
	}

	admin := mintToken(t, iss, "admin", auth.PermSettingsAdmin, auth.PermUserWrite)
	rr := do(t, r, http.MethodGet, "/api/users/alice/access", admin, "")
	if rr.Code != http.StatusOK {
		t.Fatalf("access: code=%d body=%s", rr.Code, rr.Body.String())
	}
	var got struct {
		Projects []struct {
			Project string `json:"project"`
		} `json:"projects"`
	}
	if err := json.Unmarshal(rr.Body.Bytes(), &got); err != nil {
		t.Fatal(err)
	}
	if len(got.Projects) != 1 || got.Projects[0].Project != "alive" {
		t.Fatalf("projects = %+v, want only alive", got.Projects)
	}
}
