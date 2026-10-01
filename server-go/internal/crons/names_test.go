package crons

import (
	"context"
	"errors"
	"strings"
	"testing"

	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/apimachinery/pkg/apis/meta/v1/unstructured"

	"kuso/server/internal/kube"
)

func projectCron(name string) CreateProjectCronRequest {
	return CreateProjectCronRequest{Name: name, Kind: "http", Schedule: "0 * * * *", URL: "https://example.com/tick"}
}

// "Nightly Job!" reached the apiserver and came back as a 500; a name
// whose CronJob exceeds 52 characters was accepted and never rendered.
func TestAddProject_ValidatesName(t *testing.T) {
	t.Parallel()
	s := cronFakeService(t)
	if _, err := s.AddProject(context.Background(), "alpha", projectCron("Nightly Job!")); !errors.Is(err, ErrInvalid) {
		t.Errorf("bad name: err = %v, want ErrInvalid", err)
	}
	if _, err := s.AddProject(context.Background(), "alpha", projectCron(strings.Repeat("a", 50))); !errors.Is(err, ErrInvalid) {
		t.Errorf("long name: err = %v, want ErrInvalid", err)
	}
	if _, err := s.AddProject(context.Background(), "alpha", projectCron("nightly")); err != nil {
		t.Errorf("good name: %v", err)
	}
}

// Project cron "web" is CR alpha-web, the same helm release name as
// service "web"; the second one to be created never installs.
func TestAddProject_RefusesNameHeldByService(t *testing.T) {
	t.Parallel()
	s := cronFakeService(t)
	svc := &unstructured.Unstructured{}
	svc.SetGroupVersionKind(kube.GVRServices.GroupVersion().WithKind("KusoService"))
	svc.SetNamespace("kuso")
	svc.SetName("alpha-web")
	if _, err := s.Kube.Dynamic.Resource(kube.GVRServices).Namespace("kuso").Create(context.Background(), svc, metav1.CreateOptions{}); err != nil {
		t.Fatal(err)
	}
	if _, err := s.AddProject(context.Background(), "alpha", projectCron("web")); !errors.Is(err, ErrConflict) {
		t.Fatalf("err = %v, want ErrConflict", err)
	}
}
