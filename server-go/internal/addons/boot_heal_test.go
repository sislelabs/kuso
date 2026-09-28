package addons

import (
	"context"
	"log/slog"
	"testing"

	corev1 "k8s.io/api/core/v1"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	dynamicfake "k8s.io/client-go/dynamic/fake"
	kubefake "k8s.io/client-go/kubernetes/fake"

	"kuso/server/internal/kube"
)

func seedAddonCR(name string, labels, annotations map[string]string, spec kube.KusoAddonSpec) seed {
	return typedSeed(kube.GVRAddons, "KusoAddon", &kube.KusoAddon{
		ObjectMeta: metav1.ObjectMeta{Name: name, Namespace: "kuso", Labels: labels, Annotations: annotations},
		Spec:       spec,
	})
}

func TestHealCloneSourceAnnotations(t *testing.T) {
	t.Parallel()
	lbl := func(env string) map[string]string {
		m := map[string]string{kube.LabelProject: "alpha"}
		if env != "" {
			m[kube.LabelEnv] = env
		}
		return m
	}
	pg := kube.KusoAddonSpec{Project: "alpha", Kind: "postgres"}
	s := fakeService(t,
		seedAddonCR("alpha-db", lbl(""), nil, pg),
		// Unambiguous pre-fix named-env clone → stamped.
		seedAddonCR("alpha-db-staging", lbl("staging"), nil, pg),
		// Already stamped → untouched (even though the value differs).
		seedAddonCR("alpha-db-qa", lbl("qa"), map[string]string{envGroupSourceAddonKey: "alpha-other"}, pg),
		// Source gone → skipped, never guessed.
		seedAddonCR("alpha-gone-staging", lbl("staging"), nil, pg),
		// Name derives a source of a different kind → skipped.
		seedAddonCR("alpha-cache", lbl(""), nil, kube.KusoAddonSpec{Project: "alpha", Kind: "redis"}),
		seedAddonCR("alpha-cache-dev", lbl("dev"), nil, pg),
		// Name doesn't encode the scope → skipped.
		seedAddonCR("alpha-db-custom", lbl("staging"), nil, pg),
		// Preview clone → not a named-env clone.
		seedAddonCR("alpha-db-pr-3", map[string]string{kube.LabelProject: "alpha", kube.LabelEnv: "preview-pr-3", "kuso.sislelabs.com/preview-pr": "3"}, nil, pg),
		// Derived source is itself an env clone → ambiguous, skipped.
		seedAddonCR("alpha-db-staging-v2", lbl("v2"), nil, pg),
	)
	s.Kube.Clientset = kubefake.NewSimpleClientset()

	stamped, err := s.HealCloneSourceAnnotations(context.Background(), slog.Default())
	if err != nil {
		t.Fatal(err)
	}
	if stamped != 1 {
		t.Errorf("stamped = %d, want 1", stamped)
	}
	want := map[string]string{
		"alpha-db-staging":    "alpha-db",
		"alpha-db-qa":         "alpha-other",
		"alpha-gone-staging":  "",
		"alpha-cache-dev":     "",
		"alpha-db-custom":     "",
		"alpha-db-pr-3":       "",
		"alpha-db-staging-v2": "",
		"alpha-db":            "",
	}
	for name, v := range want {
		a, err := s.Kube.GetKusoAddon(context.Background(), "kuso", name)
		if err != nil {
			t.Fatalf("get %s: %v", name, err)
		}
		if got := a.Annotations[envGroupSourceAddonKey]; got != v {
			t.Errorf("%s annotation = %q, want %q", name, got, v)
		}
	}
	// Idempotent.
	if n, _ := s.HealCloneSourceAnnotations(context.Background(), slog.Default()); n != 0 {
		t.Errorf("second run stamped %d, want 0", n)
	}
}

func TestHealInstanceConnHosts(t *testing.T) {
	t.Parallel()
	inst := func(name, ns string) seed {
		sd := seedAddonCR(name, map[string]string{kube.LabelProject: "e2e"}, nil,
			kube.KusoAddonSpec{Project: "e2e", Kind: "postgres", UseInstanceAddon: "kuso-instance-pg"})
		sd.obj.SetNamespace(ns)
		return sd
	}
	s := fakeService(t)
	for _, sd := range []seed{inst("e2e-db", "kuso-e2e"), inst("e2e-done", "kuso-e2e"), inst("home-db", "kuso")} {
		seedInNS(t, s, sd)
	}
	bareDSN := "postgres://e2e_db:keep-me@kuso-instance-pg:5432/e2e_db?sslmode=disable"
	bare := func(name, ns string) *corev1.Secret {
		return &corev1.Secret{
			ObjectMeta: metav1.ObjectMeta{Name: name, Namespace: ns, Labels: map[string]string{"custom": "label"}},
			Data: map[string][]byte{
				"DATABASE_URL":      []byte("postgres://e2e_db:keep-me@kuso-instance-pg-pooler:6432/e2e_db?sslmode=disable"),
				"DIRECT_URL":        []byte(bareDSN),
				"POSTGRES_HOST":     []byte("kuso-instance-pg"),
				"POSTGRES_PORT":     []byte("5432"),
				"POSTGRES_USER":     []byte("e2e_db"),
				"POSTGRES_PASSWORD": []byte("keep-me"),
				"POSTGRES_DB":       []byte("e2e_db"),
				"POOLER_HOST":       []byte("kuso-instance-pg-pooler"),
				"POOLER_PORT":       []byte("6432"),
				"POOLER_URL":        []byte("postgres://e2e_db:keep-me@kuso-instance-pg-pooler:6432/e2e_db?sslmode=disable"),
				"EXTRA":             []byte("x"),
			},
		}
	}
	done := bare("e2e-done-conn", "kuso-e2e")
	done.Data["POSTGRES_HOST"] = []byte("kuso-instance-pg.kuso.svc")
	done.Data["DIRECT_URL"] = []byte("postgres://e2e_db:keep-me@kuso-instance-pg.kuso.svc:5432/e2e_db?sslmode=disable")
	s.Kube.Clientset = kubefake.NewSimpleClientset(bare("e2e-db-conn", "kuso-e2e"), done, bare("home-db-conn", "kuso"))

	ctx := context.Background()
	n, err := s.HealInstanceConnHosts(ctx, slog.Default())
	if err != nil {
		t.Fatal(err)
	}
	if n != 1 {
		t.Errorf("healed = %d, want 1", n)
	}
	sec, _ := s.Kube.Clientset.CoreV1().Secrets("kuso-e2e").Get(ctx, "e2e-db-conn", metav1.GetOptions{})
	want := map[string]string{
		"POSTGRES_HOST":     "kuso-instance-pg.kuso.svc",
		"POSTGRES_PASSWORD": "keep-me",
		"DIRECT_URL":        "postgres://e2e_db:keep-me@kuso-instance-pg.kuso.svc:5432/e2e_db?sslmode=disable",
		"DATABASE_URL":      "postgres://e2e_db:keep-me@kuso-instance-pg-pooler.kuso.svc:6432/e2e_db?sslmode=disable",
		"POOLER_HOST":       "kuso-instance-pg-pooler.kuso.svc",
		"EXTRA":             "x",
	}
	for k, v := range want {
		if got := string(sec.Data[k]); got != v {
			t.Errorf("%s = %q, want %q", k, got, v)
		}
	}
	if sec.Labels["custom"] != "label" {
		t.Error("existing labels dropped")
	}
	// Home-namespace project: bare host resolves there, leave it alone.
	home, _ := s.Kube.Clientset.CoreV1().Secrets("kuso").Get(ctx, "home-db-conn", metav1.GetOptions{})
	if got := string(home.Data["POSTGRES_HOST"]); got != "kuso-instance-pg" {
		t.Errorf("home-ns POSTGRES_HOST = %q, want untouched", got)
	}
	if n, _ := s.HealInstanceConnHosts(ctx, slog.Default()); n != 0 {
		t.Errorf("second run healed %d, want 0", n)
	}
}

func seedInNS(t *testing.T, s *Service, sd seed) {
	t.Helper()
	if err := s.Kube.Dynamic.(*dynamicfake.FakeDynamicClient).Tracker().Create(sd.gvr, sd.obj, sd.obj.GetNamespace()); err != nil {
		t.Fatalf("seed: %v", err)
	}
}
