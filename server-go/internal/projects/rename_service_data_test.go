package projects

import (
	"context"
	"errors"
	"testing"

	corev1 "k8s.io/api/core/v1"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	k8sfake "k8s.io/client-go/kubernetes/fake"

	"kuso/server/internal/kube"
)

func renameFixture(t *testing.T, svcSpec kube.KusoServiceSpec) (*Service, *k8sfake.Clientset) {
	t.Helper()
	env := &kube.KusoEnvironment{
		ObjectMeta: metav1.ObjectMeta{
			Name: "alpha-web-production", Namespace: "kuso",
			Labels: map[string]string{labelProject: "alpha", labelService: "web", labelEnv: "production"},
		},
		Spec: kube.KusoEnvironmentSpec{
			Project: "alpha", Service: "alpha-web", Kind: "production", Branch: "main",
			EnvFromSecrets: []string{"alpha-db-conn", "alpha-web-secrets", "alpha-web-production-secrets"},
			EnvVars: []kube.KusoEnvVar{{Name: "TOKEN", ValueFrom: map[string]any{
				"secretKeyRef": map[string]any{"name": "alpha-web-secrets", "key": "TOKEN"},
			}}},
		},
	}
	s := fakeService(t,
		seedProject("alpha", kube.KusoProjectSpec{}),
		seedService("alpha", "web", svcSpec),
		typedSeed(kube.GVREnvironments, "KusoEnvironment", env.Name, env),
	)
	cs := k8sfake.NewSimpleClientset(
		&corev1.Secret{ObjectMeta: metav1.ObjectMeta{Name: "alpha-web-secrets", Namespace: "kuso"}, Data: map[string][]byte{"TOKEN": []byte("s3cret")}},
		&corev1.Secret{ObjectMeta: metav1.ObjectMeta{Name: "alpha-web-production-secrets", Namespace: "kuso"}, Data: map[string][]byte{"API_KEY": []byte("k")}},
	)
	s.Kube.Clientset = cs
	del := func(name string) error {
		return cs.CoreV1().Secrets("kuso").Delete(context.Background(), name, metav1.DeleteOptions{})
	}
	s.SecretsCleanupForService = func(_ context.Context, p, svc string) error { return del(kube.ServiceSecretName(p, svc)) }
	s.SecretsCleanupForEnv = func(_ context.Context, p, svc, e string) error { return del(kube.EnvSecretName(p, svc, e)) }
	return s, cs
}

// Rename used to copy env specs verbatim (still mounting the OLD secret
// names) and then delete the old secrets with the old service, so pods
// came back without any `env set` value.
func TestRenameService_KeepsManagedSecrets(t *testing.T) {
	t.Parallel()
	s, cs := renameFixture(t, kube.KusoServiceSpec{Project: "alpha", Port: 3000})
	ctx := context.Background()
	if _, err := s.RenameService(ctx, "alpha", "web", "api"); err != nil {
		t.Fatalf("RenameService: %v", err)
	}
	sec, err := cs.CoreV1().Secrets("kuso").Get(ctx, "alpha-api-secrets", metav1.GetOptions{})
	if err != nil || string(sec.Data["TOKEN"]) != "s3cret" {
		t.Fatalf("service secret not carried to new name: %v %v", sec, err)
	}
	if _, err := cs.CoreV1().Secrets("kuso").Get(ctx, "alpha-api-production-secrets", metav1.GetOptions{}); err != nil {
		t.Fatalf("env secret not carried to new name: %v", err)
	}
	env := envByName(t, s, "alpha", "api")["alpha-api-production"]
	want := []string{"alpha-db-conn", "alpha-api-secrets", "alpha-api-production-secrets"}
	if len(env.Spec.EnvFromSecrets) != len(want) {
		t.Fatalf("envFromSecrets = %v, want %v", env.Spec.EnvFromSecrets, want)
	}
	for i := range want {
		if env.Spec.EnvFromSecrets[i] != want[i] {
			t.Fatalf("envFromSecrets = %v, want %v", env.Spec.EnvFromSecrets, want)
		}
	}
	if ref := secretKeyRefOf(env.Spec.EnvVars[0]); ref == nil || ref.name != "alpha-api-secrets" {
		t.Fatalf("secretKeyRef not rewritten: %+v", env.Spec.EnvVars[0])
	}
}

func TestRenameService_RefusesWithVolumes(t *testing.T) {
	t.Parallel()
	s, _ := renameFixture(t, kube.KusoServiceSpec{Project: "alpha", Volumes: []kube.KusoVolume{{Name: "data", MountPath: "/data"}}})
	_, err := s.RenameService(context.Background(), "alpha", "web", "api")
	if !errors.Is(err, ErrInvalid) {
		t.Fatalf("want ErrInvalid for a service with volumes, got %v", err)
	}
	if _, gerr := s.GetService(context.Background(), "alpha", "web"); gerr != nil {
		t.Fatalf("old service must survive a refused rename: %v", gerr)
	}
}

func TestAddService_RejectsOverlongDerivedNames(t *testing.T) {
	t.Parallel()
	s := fakeService(t, seedProject("acme-marketing-website-2026x", kube.KusoProjectSpec{}))
	_, err := s.AddService(context.Background(), "acme-marketing-website-2026x", CreateServiceRequest{Name: "frontend-dashboard", Runtime: "dockerfile"})
	if !errors.Is(err, ErrInvalid) {
		t.Fatalf("want ErrInvalid for a 58-char env name, got %v", err)
	}
}
