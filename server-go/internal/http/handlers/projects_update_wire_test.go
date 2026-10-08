package handlers

import (
	"context"
	"log/slog"
	"net/http"
	"net/http/httptest"
	"reflect"
	"strings"
	"testing"

	"github.com/go-chi/chi/v5"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"

	apiv1 "github.com/sislelabs/kuso/api/apiv1"
	"kuso/server/internal/auth"
	"kuso/server/internal/kube"
	"kuso/server/internal/projects"
)

type updateCaptureSvc struct {
	ProjectsAPI
	got projects.UpdateProjectRequest
}

func (f *updateCaptureSvc) Update(_ context.Context, name string, req projects.UpdateProjectRequest) (*kube.KusoProject, error) {
	f.got = req
	return &kube.KusoProject{ObjectMeta: metav1.ObjectMeta{Name: name}}, nil
}

// The web "Uptime checks" toggle and `kuso uptime disable <project>` send
// {"uptime":{"disabled":true}}; the wire type had no field for it, so the
// PATCH returned 200 and changed nothing.
func TestProjectsUpdate_CarriesUptime(t *testing.T) {
	svc := &updateCaptureSvc{}
	h := &ProjectsHandler{Svc: svc, Logger: slog.Default()}
	r := httptest.NewRequest(http.MethodPatch, "/api/projects/alpha", strings.NewReader(`{"uptime":{"disabled":true}}`))
	rctx := chi.NewRouteContext()
	rctx.URLParams.Add("project", "alpha")
	r = r.WithContext(auth.WithClaimsForTest(context.WithValue(r.Context(), chi.RouteCtxKey, rctx),
		&auth.Claims{UserID: "admin", Permissions: []string{string(auth.PermSettingsAdmin)}}))
	rr := httptest.NewRecorder()
	h.Update(rr, r)
	if rr.Code != http.StatusOK {
		t.Fatalf("status = %d, body %s", rr.Code, rr.Body.String())
	}
	if svc.got.Uptime == nil || svc.got.Uptime.Disabled == nil || !*svc.got.Uptime.Disabled {
		t.Fatalf("uptime not passed to the domain layer: %+v", svc.got.Uptime)
	}
}

// fillWire sets every reachable field of v to a non-zero value.
func fillWire(v reflect.Value) {
	switch v.Kind() {
	case reflect.String:
		v.SetString("x")
	case reflect.Bool:
		v.SetBool(true)
	case reflect.Int, reflect.Int32, reflect.Int64:
		v.SetInt(7)
	case reflect.Pointer:
		p := reflect.New(v.Type().Elem())
		fillWire(p.Elem())
		v.Set(p)
	case reflect.Struct:
		for i := 0; i < v.NumField(); i++ {
			fillWire(v.Field(i))
		}
	}
}

// Every field of the domain update request must be reachable from the wire
// type: a hand-maintained conversion that silently drops a field is how
// uptime went missing.
func TestApiv1UpdateToDomain_CoversEveryDomainField(t *testing.T) {
	var in apiv1.UpdateProjectRequest
	fillWire(reflect.ValueOf(&in).Elem())
	out := reflect.ValueOf(apiv1UpdateToDomain(in))
	var walk func(path string, v reflect.Value)
	walk = func(path string, v reflect.Value) {
		switch v.Kind() {
		case reflect.Pointer:
			if v.IsNil() {
				t.Errorf("%s is not mapped from apiv1.UpdateProjectRequest", path)
				return
			}
			walk(path, v.Elem())
		case reflect.Struct:
			for i := 0; i < v.NumField(); i++ {
				walk(path+"."+v.Type().Field(i).Name, v.Field(i))
			}
		default:
			if v.IsZero() {
				t.Errorf("%s is not mapped from apiv1.UpdateProjectRequest", path)
			}
		}
	}
	walk("UpdateProjectRequest", out)
}
