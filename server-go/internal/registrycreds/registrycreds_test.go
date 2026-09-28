package registrycreds

import (
	"context"
	"encoding/base64"
	"encoding/json"
	"errors"
	"strings"
	"testing"

	corev1 "k8s.io/api/core/v1"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/apimachinery/pkg/apis/meta/v1/unstructured"
	"k8s.io/apimachinery/pkg/runtime"
	"k8s.io/apimachinery/pkg/runtime/schema"
	dynamicfake "k8s.io/client-go/dynamic/fake"
	"k8s.io/client-go/kubernetes/fake"

	"kuso/server/internal/kube"
)

func newSvc(t *testing.T, services ...*kube.KusoService) (*Service, *fake.Clientset) {
	t.Helper()
	cs := fake.NewSimpleClientset()
	dyn := dynamicfake.NewSimpleDynamicClientWithCustomListKinds(runtime.NewScheme(), map[schema.GroupVersionResource]string{
		kube.GVRServices: "KusoServiceList",
	})
	for _, s := range services {
		m, err := runtime.DefaultUnstructuredConverter.ToUnstructured(s)
		if err != nil {
			t.Fatal(err)
		}
		u := &unstructured.Unstructured{Object: m}
		u.SetGroupVersionKind(kube.GVRServices.GroupVersion().WithKind("KusoService"))
		if err := dyn.Tracker().Create(kube.GVRServices, u, "kuso"); err != nil {
			t.Fatal(err)
		}
	}
	return New(&kube.Client{Clientset: cs, Dynamic: dyn}, "kuso"), cs
}

func imageService(project, name, pullSecret string) *kube.KusoService {
	return &kube.KusoService{
		ObjectMeta: metav1.ObjectMeta{
			Name: project + "-" + name, Namespace: "kuso",
			Labels: map[string]string{kube.LabelProject: project},
		},
		Spec: kube.KusoServiceSpec{
			Project: project, Runtime: "image",
			Image: &kube.KusoImage{Repository: "ghcr.io/acme/app", Tag: "1", PullSecret: pullSecret},
		},
	}
}

func TestLogin_WritesDockerConfigJSONSecret(t *testing.T) {
	t.Parallel()
	s, cs := newSvc(t)
	cred, err := s.Login(context.Background(), "shop", "ghcr.io", "octo", "s3cret")
	if err != nil {
		t.Fatal(err)
	}
	if cred.SecretName != "shop-regcred-ghcr-io" {
		t.Errorf("secret name = %q", cred.SecretName)
	}
	sec, err := cs.CoreV1().Secrets("kuso").Get(context.Background(), cred.SecretName, metav1.GetOptions{})
	if err != nil {
		t.Fatal(err)
	}
	if sec.Type != corev1.SecretTypeDockerConfigJson {
		t.Errorf("type = %q", sec.Type)
	}
	if sec.Labels[kube.LabelProject] != "shop" {
		t.Errorf("project label = %q", sec.Labels[kube.LabelProject])
	}
	var cfg struct {
		Auths map[string]struct {
			Username string `json:"username"`
			Password string `json:"password"`
			Auth     string `json:"auth"`
		} `json:"auths"`
	}
	if err := json.Unmarshal(sec.Data[corev1.DockerConfigJsonKey], &cfg); err != nil {
		t.Fatal(err)
	}
	a, ok := cfg.Auths["ghcr.io"]
	if !ok {
		t.Fatalf("no ghcr.io auth entry: %v", cfg.Auths)
	}
	if a.Username != "octo" || a.Password != "s3cret" || a.Auth != base64.StdEncoding.EncodeToString([]byte("octo:s3cret")) {
		t.Errorf("auth entry = %+v", a)
	}
}

func TestLogin_DockerHubUsesIndexKey(t *testing.T) {
	t.Parallel()
	s, cs := newSvc(t)
	cred, err := s.Login(context.Background(), "shop", "docker.io", "u", "p")
	if err != nil {
		t.Fatal(err)
	}
	sec, _ := cs.CoreV1().Secrets("kuso").Get(context.Background(), cred.SecretName, metav1.GetOptions{})
	if !strings.Contains(string(sec.Data[corev1.DockerConfigJsonKey]), `"https://index.docker.io/v1/"`) {
		t.Errorf("docker hub creds must be keyed on the index URL kubelet matches: %s", sec.Data[corev1.DockerConfigJsonKey])
	}
}

func TestLogin_RejectsBadInput(t *testing.T) {
	t.Parallel()
	s, _ := newSvc(t)
	bad := [][3]string{
		{"", "u", "p"},
		{"ghcr.io", "", "p"},
		{"ghcr.io", "u", ""},
		{"https://ghcr.io/v2/evil path", "u", "p"},
	}
	for _, b := range bad {
		if _, err := s.Login(context.Background(), "shop", b[0], b[1], b[2]); !errors.Is(err, ErrInvalid) {
			t.Errorf("Login(%q,%q,%q) err = %v, want ErrInvalid", b[0], b[1], b[2], err)
		}
	}
}

func TestLogin_UpsertsAndNormalizesHost(t *testing.T) {
	t.Parallel()
	s, _ := newSvc(t)
	ctx := context.Background()
	if _, err := s.Login(ctx, "shop", "https://GHCR.io/", "old", "p1"); err != nil {
		t.Fatal(err)
	}
	if _, err := s.Login(ctx, "shop", "ghcr.io", "new", "p2"); err != nil {
		t.Fatal(err)
	}
	list, err := s.List(ctx, "shop")
	if err != nil {
		t.Fatal(err)
	}
	if len(list) != 1 || list[0].Registry != "ghcr.io" || list[0].Username != "new" {
		t.Errorf("list = %+v", list)
	}
}

func TestList_NeverCarriesPasswordAndIsProjectScoped(t *testing.T) {
	t.Parallel()
	s, _ := newSvc(t)
	ctx := context.Background()
	if _, err := s.Login(ctx, "shop", "ghcr.io", "octo", "hunter2-secret"); err != nil {
		t.Fatal(err)
	}
	if _, err := s.Login(ctx, "shop-admin", "ghcr.io", "other", "other-secret"); err != nil {
		t.Fatal(err)
	}
	list, err := s.List(ctx, "shop")
	if err != nil {
		t.Fatal(err)
	}
	raw, _ := json.Marshal(list)
	if strings.Contains(string(raw), "hunter2-secret") || strings.Contains(string(raw), base64.StdEncoding.EncodeToString([]byte("octo:hunter2-secret"))) {
		t.Errorf("list leaked the password: %s", raw)
	}
	if len(list) != 1 || list[0].Username != "octo" {
		t.Errorf("list must only show shop's credential, got %s", raw)
	}
}

func TestResolve_AcceptsHostOrNameButOnlyOwnProject(t *testing.T) {
	t.Parallel()
	s, _ := newSvc(t)
	ctx := context.Background()
	own, _ := s.Login(ctx, "shop", "ghcr.io", "u", "p")
	other, _ := s.Login(ctx, "shop-regcred", "ghcr.io", "u", "p")

	for _, ref := range []string{"ghcr.io", "https://ghcr.io", own.SecretName} {
		got, err := s.Resolve(ctx, "shop", ref)
		if err != nil || got != own.SecretName {
			t.Errorf("Resolve(%q) = %q, %v; want %q", ref, got, err, own.SecretName)
		}
	}
	if _, err := s.Resolve(ctx, "shop", other.SecretName); !errors.Is(err, ErrNotFound) {
		t.Errorf("resolving another project's secret must be ErrNotFound, got %v", err)
	}
	if _, err := s.Resolve(ctx, "shop", "quay.io"); !errors.Is(err, ErrNotFound) {
		t.Errorf("unknown registry must be ErrNotFound, got %v", err)
	}
}

func TestLogout_RefusesWhileReferencedThenDeletes(t *testing.T) {
	t.Parallel()
	s, cs := newSvc(t, imageService("shop", "api", "shop-regcred-ghcr-io"))
	ctx := context.Background()
	cred, _ := s.Login(ctx, "shop", "ghcr.io", "u", "p")

	err := s.Logout(ctx, "shop", "ghcr.io")
	if !errors.Is(err, ErrConflict) || !strings.Contains(err.Error(), "api") {
		t.Fatalf("logout of an in-use credential: err = %v, want ErrConflict naming the service", err)
	}

	s2, cs2 := newSvc(t, imageService("shop", "api", ""))
	cred, _ = s2.Login(ctx, "shop", "ghcr.io", "u", "p")
	if err := s2.Logout(ctx, "shop", "ghcr.io"); err != nil {
		t.Fatal(err)
	}
	if _, err := cs2.CoreV1().Secrets("kuso").Get(ctx, cred.SecretName, metav1.GetOptions{}); err == nil {
		t.Error("secret still exists after logout")
	}
	if err := s2.Logout(ctx, "shop", "ghcr.io"); !errors.Is(err, ErrNotFound) {
		t.Errorf("second logout err = %v, want ErrNotFound", err)
	}
	_ = cs
}
