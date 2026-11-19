package projects

import (
	"context"
	"testing"

	corev1 "k8s.io/api/core/v1"
	apierrors "k8s.io/apimachinery/pkg/api/errors"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/apimachinery/pkg/runtime"
	"k8s.io/apimachinery/pkg/runtime/schema"
	dynamicfake "k8s.io/client-go/dynamic/fake"
	kubefake "k8s.io/client-go/kubernetes/fake"

	"kuso/server/internal/kube"
)

// TestDeleteProject_KeepsHelmOwnedConnSecrets guards the project-delete
// label sweep against destroying helm-owned addon conn/tls Secrets. Those
// carry helm.sh/resource-policy: keep because their data (the addon
// password) must keep matching the surviving pgdata PVC on a
// delete+recreate cycle. Sweeping them mints a fresh password that fails to
// auth against the old data. Imperative kuso-server secrets (the
// <project>-shared secret and friends) must STILL be swept — they're not
// helm-owned and orphan in the shared namespace otherwise.
func TestDeleteProject_KeepsHelmOwnedConnSecrets(t *testing.T) {
	const ns = "kuso"
	scheme := runtime.NewScheme()
	listKinds := map[schema.GroupVersionResource]string{
		kube.GVRKuso:         "KusoList",
		kube.GVRProjects:     "KusoProjectList",
		kube.GVRServices:     "KusoServiceList",
		kube.GVREnvironments: "KusoEnvironmentList",
		kube.GVRAddons:       "KusoAddonList",
		kube.GVRBuilds:       "KusoBuildList",
		kube.GVRCrons:        "KusoCronList",
		kube.GVRRuns:         "KusoRunList",
	}
	dyn := dynamicfake.NewSimpleDynamicClientWithCustomListKinds(scheme, listKinds)

	proj := seedProject("beta", kube.KusoProjectSpec{DefaultRepo: &kube.KusoRepoRef{URL: "x"}})
	if err := dyn.Tracker().Create(proj.gvr, proj.obj, proj.obj.GetNamespace()); err != nil {
		t.Fatalf("seed project: %v", err)
	}

	// Helm-owned addon conn + tls secrets: project-labelled AND carrying
	// the keep policy. These MUST survive the sweep.
	connSecret := &corev1.Secret{ObjectMeta: metav1.ObjectMeta{
		Name:        "beta-pg-conn",
		Namespace:   ns,
		Labels:      map[string]string{labelProject: "beta"},
		Annotations: map[string]string{"helm.sh/resource-policy": "keep"},
	}}
	tlsSecret := &corev1.Secret{ObjectMeta: metav1.ObjectMeta{
		Name:        "beta-pg-tls",
		Namespace:   ns,
		Labels:      map[string]string{labelProject: "beta"},
		Annotations: map[string]string{"helm.sh/resource-policy": "keep"},
	}}

	// Imperative kuso-server secret: project-labelled, NOT helm-owned.
	// Must be swept so it doesn't orphan in the shared namespace and
	// re-seed a same-named project later.
	sharedSecret := &corev1.Secret{ObjectMeta: metav1.ObjectMeta{
		Name:      "beta-shared",
		Namespace: ns,
		Labels:    map[string]string{labelProject: "beta"},
	}}

	cs := kubefake.NewSimpleClientset(connSecret, tlsSecret, sharedSecret)
	s := New(&kube.Client{Dynamic: dyn, Clientset: cs}, "kuso")

	// Plain Delete (no PurgeData): the safe path that keeps data.
	if err := s.Delete(context.Background(), "beta"); err != nil {
		t.Fatalf("Delete: %v", err)
	}

	mustExist := func(name string) {
		if _, err := cs.CoreV1().Secrets(ns).Get(context.Background(), name, metav1.GetOptions{}); err != nil {
			t.Errorf("helm-owned secret %s should have survived project delete, err=%v", name, err)
		}
	}
	mustGone := func(name string) {
		if _, err := cs.CoreV1().Secrets(ns).Get(context.Background(), name, metav1.GetOptions{}); !apierrors.IsNotFound(err) {
			t.Errorf("imperative secret %s should have been swept, err=%v", name, err)
		}
	}
	mustExist("beta-pg-conn")
	mustExist("beta-pg-tls")
	mustGone("beta-shared")
}

// A no-purge project delete keeps the project's own addon data, but an env
// clone's data goes with its env as everywhere else: a recreated project's
// same-named env group would otherwise mount the old clone's PVC.
func TestDeleteProject_NoPurgeReclaimsEnvCloneData(t *testing.T) {
	const ns = "kuso"
	scheme := runtime.NewScheme()
	listKinds := map[schema.GroupVersionResource]string{
		kube.GVRKuso:         "KusoList",
		kube.GVRProjects:     "KusoProjectList",
		kube.GVRServices:     "KusoServiceList",
		kube.GVREnvironments: "KusoEnvironmentList",
		kube.GVRAddons:       "KusoAddonList",
		kube.GVRBuilds:       "KusoBuildList",
		kube.GVRCrons:        "KusoCronList",
		kube.GVRRuns:         "KusoRunList",
	}
	dyn := dynamicfake.NewSimpleDynamicClientWithCustomListKinds(scheme, listKinds)
	addon := func(name string, labels map[string]string) seed {
		return typedSeed(kube.GVRAddons, "KusoAddon", name, &kube.KusoAddon{
			ObjectMeta: metav1.ObjectMeta{Name: name, Namespace: ns, Labels: labels},
			Spec:       kube.KusoAddonSpec{Project: "beta", Kind: "postgres"},
		})
	}
	for _, sd := range []seed{
		seedProject("beta", kube.KusoProjectSpec{DefaultRepo: &kube.KusoRepoRef{URL: "x"}}),
		addon("beta-pg", map[string]string{labelProject: "beta"}),
		addon("beta-pg-staging", map[string]string{labelProject: "beta", labelEnv: "staging"}),
	} {
		if err := dyn.Tracker().Create(sd.gvr, sd.obj, sd.obj.GetNamespace()); err != nil {
			t.Fatalf("seed: %v", err)
		}
	}
	var objs []runtime.Object
	for _, fqn := range []string{"beta-pg", "beta-pg-staging"} {
		objs = append(objs,
			&corev1.PersistentVolumeClaim{ObjectMeta: metav1.ObjectMeta{
				Name: "data-" + fqn + "-0", Namespace: ns,
				Labels: map[string]string{"app.kubernetes.io/instance": fqn},
			}},
			&corev1.Secret{ObjectMeta: metav1.ObjectMeta{
				Name: fqn + "-conn", Namespace: ns,
				Labels:      map[string]string{labelProject: "beta", "app.kubernetes.io/instance": fqn},
				Annotations: map[string]string{"helm.sh/resource-policy": "keep"},
			}},
		)
	}
	cs := kubefake.NewSimpleClientset(objs...)
	s := New(&kube.Client{Dynamic: dyn, Clientset: cs}, "kuso")

	if err := s.Delete(context.Background(), "beta"); err != nil {
		t.Fatalf("Delete: %v", err)
	}
	ctx := context.Background()
	if _, err := cs.CoreV1().PersistentVolumeClaims(ns).Get(ctx, "data-beta-pg-0", metav1.GetOptions{}); err != nil {
		t.Errorf("project addon PVC must be retained: %v", err)
	}
	if _, err := cs.CoreV1().Secrets(ns).Get(ctx, "beta-pg-conn", metav1.GetOptions{}); err != nil {
		t.Errorf("project addon conn must be retained: %v", err)
	}
	if _, err := cs.CoreV1().PersistentVolumeClaims(ns).Get(ctx, "data-beta-pg-staging-0", metav1.GetOptions{}); !apierrors.IsNotFound(err) {
		t.Errorf("env clone PVC survived the project delete (err=%v)", err)
	}
	if _, err := cs.CoreV1().Secrets(ns).Get(ctx, "beta-pg-staging-conn", metav1.GetOptions{}); !apierrors.IsNotFound(err) {
		t.Errorf("env clone conn survived the project delete (err=%v)", err)
	}
}
