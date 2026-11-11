package projects

import (
	"context"
	"errors"
	"testing"
)

// ISO-2/ISO-4/ISO-11: a new project starts with env isolation on (including
// between non-production envs) and without the legacy project-wide egress
// to the shared instance Postgres.
func TestCreate_NewProjectIsIsolated(t *testing.T) {
	t.Parallel()
	s := fakeService(t)
	if _, err := s.Create(context.Background(), CreateProjectRequest{Name: "alpha"}); err != nil {
		t.Fatalf("Create: %v", err)
	}
	p, err := s.Kube.GetKusoProject(context.Background(), s.Namespace, "alpha")
	if err != nil {
		t.Fatal(err)
	}
	np := p.Spec.NetworkPolicy
	if np == nil || np.EnvIsolation == nil || !*np.EnvIsolation ||
		np.IsolateNonProdEnvs == nil || !*np.IsolateNonProdEnvs ||
		np.InstanceDBEgress == nil || *np.InstanceDBEgress {
		t.Fatalf("new project networkPolicy = %+v", np)
	}
}

// ISO-12: the preview basic-auth Secret is set and cleared through Update;
// a control-plane Secret name is refused before it reaches the apiserver.
func TestUpdate_PreviewBasicAuthSecret(t *testing.T) {
	t.Parallel()
	s := fakeService(t)
	ctx := context.Background()
	if _, err := s.Create(ctx, CreateProjectRequest{Name: "alpha"}); err != nil {
		t.Fatalf("Create: %v", err)
	}
	set := func(v string) error {
		_, err := s.Update(ctx, "alpha", UpdateProjectRequest{Previews: &UpdateProjectPreviewsSpec{BasicAuthSecret: &v}})
		return err
	}
	if err := set("kuso-server-secrets"); !errors.Is(err, ErrInvalid) {
		t.Fatalf("kuso-* secret: err = %v, want ErrInvalid", err)
	}
	if err := set("alpha-preview-auth"); err != nil {
		t.Fatalf("set: %v", err)
	}
	p, _ := s.Kube.GetKusoProject(ctx, s.Namespace, "alpha")
	if p.Spec.Previews.BasicAuthSecret != "alpha-preview-auth" {
		t.Fatalf("basicAuthSecret = %q", p.Spec.Previews.BasicAuthSecret)
	}
	if err := set(""); err != nil {
		t.Fatalf("clear: %v", err)
	}
	p, _ = s.Kube.GetKusoProject(ctx, s.Namespace, "alpha")
	if p.Spec.Previews.BasicAuthSecret != "" {
		t.Fatalf("not cleared: %q", p.Spec.Previews.BasicAuthSecret)
	}
}
