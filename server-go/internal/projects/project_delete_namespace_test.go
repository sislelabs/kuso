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

// Bug #20: `kuso project delete e2e --purge-data` on a project with its own
// namespace (kuso-e2e) left the namespace behind holding KusoRun CRs (+ their
// helm release Secrets), every keep-policy addon conn Secret (live
// credentials for deleted databases), the mirrored kuso-backup-s3 credential
// and a cert-manager `<env>-tls` Secret from an env deleted earlier.

const e2eNS = "kuso-e2e"

func nsSeed(gvr schema.GroupVersionResource, kind string, obj metav1.Object, full any) seed {
	sd := typedSeed(gvr, kind, obj.GetName(), full)
	sd.obj.SetNamespace(obj.GetNamespace())
	return sd
}

type deleteNSFixture struct {
	s   *Service
	dyn *dynamicfake.FakeDynamicClient
	cs  *kubefake.Clientset
}

func newDeleteNSFixture(t *testing.T, ns string, nsObj *corev1.Namespace, extraSecrets ...*corev1.Secret) deleteNSFixture {
	t.Helper()
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
	dyn := dynamicfake.NewSimpleDynamicClientWithCustomListKinds(runtime.NewScheme(), listKinds)

	projNS := ns
	if ns == "kuso" {
		projNS = ""
	}
	seeds := []seed{
		seedProject("e2e", kube.KusoProjectSpec{Namespace: projNS}),
		// Sibling project whose name extends "e2e-": its resources in a
		// shared namespace must never be mistaken for e2e's.
		seedProject("e2e-api", kube.KusoProjectSpec{}),
		nsSeed(kube.GVRServices, "KusoService", &metav1.ObjectMeta{Name: "e2e-api", Namespace: ns}, &kube.KusoService{
			ObjectMeta: metav1.ObjectMeta{Name: "e2e-api", Namespace: ns, Labels: map[string]string{labelProject: "e2e", labelService: "api"}},
		}),
		nsSeed(kube.GVRAddons, "KusoAddon", &metav1.ObjectMeta{Name: "e2e-db", Namespace: ns}, &kube.KusoAddon{
			ObjectMeta: metav1.ObjectMeta{Name: "e2e-db", Namespace: ns, Labels: map[string]string{labelProject: "e2e"}},
			Spec:       kube.KusoAddonSpec{Kind: "postgres"},
		}),
		nsSeed(kube.GVRRuns, "KusoRun", &metav1.ObjectMeta{Name: "e2e-api-run-a", Namespace: ns}, &kube.KusoRun{
			ObjectMeta: metav1.ObjectMeta{Name: "e2e-api-run-a", Namespace: ns, Labels: map[string]string{labelProject: "e2e"}},
			Spec:       kube.KusoRunSpec{Project: "e2e", Service: "e2e-api"},
		}),
		nsSeed(kube.GVRRuns, "KusoRun", &metav1.ObjectMeta{Name: "e2e-api-run-b", Namespace: ns}, &kube.KusoRun{
			ObjectMeta: metav1.ObjectMeta{Name: "e2e-api-run-b", Namespace: ns, Labels: map[string]string{labelProject: "e2e"}},
			Spec:       kube.KusoRunSpec{Project: "e2e", Service: "e2e-api"},
		}),
		nsSeed(kube.GVRCrons, "KusoCron", &metav1.ObjectMeta{Name: "e2e-api-nightly", Namespace: ns}, &kube.KusoCron{
			ObjectMeta: metav1.ObjectMeta{Name: "e2e-api-nightly", Namespace: ns, Labels: map[string]string{labelProject: "e2e"}},
			Spec:       kube.KusoCronSpec{Project: "e2e"},
		}),
		// Another project's run in the same namespace (only reachable in
		// the shared home-namespace case) must survive.
		nsSeed(kube.GVRRuns, "KusoRun", &metav1.ObjectMeta{Name: "e2e-api-x-run-z", Namespace: ns}, &kube.KusoRun{
			ObjectMeta: metav1.ObjectMeta{Name: "e2e-api-x-run-z", Namespace: ns, Labels: map[string]string{labelProject: "e2e-api"}},
			Spec:       kube.KusoRunSpec{Project: "e2e-api", Service: "e2e-api-x"},
		}),
	}
	for _, sd := range seeds {
		if err := dyn.Tracker().Create(sd.gvr, sd.obj, sd.obj.GetNamespace()); err != nil {
			t.Fatalf("seed %s: %v", sd.obj.GetName(), err)
		}
	}

	keep := map[string]string{"helm.sh/resource-policy": "keep"}
	certAnn := func(cert string) map[string]string {
		return map[string]string{"cert-manager.io/certificate-name": cert}
	}
	objs := []runtime.Object{
		&corev1.Namespace{ObjectMeta: metav1.ObjectMeta{Name: "kuso"}},
		// Live addon's conn Secret.
		&corev1.Secret{ObjectMeta: metav1.ObjectMeta{Name: "e2e-db-conn", Namespace: ns,
			Labels: map[string]string{labelProject: "e2e", "kuso.sislelabs.com/addon": "e2e-db"}, Annotations: keep}},
		// Env-clone addon whose CR is already gone; keep policy left the
		// Secret behind.
		&corev1.Secret{ObjectMeta: metav1.ObjectMeta{Name: "e2e-db-staging-conn", Namespace: ns,
			Labels: map[string]string{labelProject: "e2e", "kuso.sislelabs.com/addon": "e2e-db-staging"}, Annotations: keep}},
		// cert-manager Secret of a preview env deleted before the project.
		&corev1.Secret{ObjectMeta: metav1.ObjectMeta{Name: "e2e-api-qa-pr-1-tls", Namespace: ns,
			Labels: map[string]string{"controller.cert-manager.io/fao": "true"}, Annotations: certAnn("e2e-api-qa-pr-1-tls")},
			Type: corev1.SecretTypeTLS},
		// Sibling project e2e-api's cert Secret: shares the "e2e-api-" prefix.
		&corev1.Secret{ObjectMeta: metav1.ObjectMeta{Name: "e2e-api-x-production-tls", Namespace: "kuso",
			Annotations: certAnn("e2e-api-x-production-tls")}, Type: corev1.SecretTypeTLS},
		// Home namespace's own backup credential: must never be touched.
		&corev1.Secret{ObjectMeta: metav1.ObjectMeta{Name: "kuso-backup-s3", Namespace: "kuso",
			Labels: map[string]string{kube.ManagedByLabel: kube.ManagedByValue}}},
	}
	if ns != "kuso" {
		objs = append(objs,
			&corev1.Secret{ObjectMeta: metav1.ObjectMeta{Name: "kuso-backup-s3", Namespace: ns,
				Labels: map[string]string{kube.ManagedByLabel: kube.ManagedByValue}}},
		)
	}
	if nsObj != nil {
		objs = append(objs, nsObj)
	}
	for _, sec := range extraSecrets {
		objs = append(objs, sec)
	}
	cs := kubefake.NewSimpleClientset(objs...)
	return deleteNSFixture{s: New(&kube.Client{Dynamic: dyn, Clientset: cs}, "kuso"), dyn: dyn, cs: cs}
}

func kusoCreatedNS(name string) *corev1.Namespace {
	return &corev1.Namespace{ObjectMeta: metav1.ObjectMeta{
		Name:        name,
		Labels:      map[string]string{kube.ManagedByLabel: kube.ManagedByValue},
		Annotations: map[string]string{kube.NamespaceCreatedByKusoAnnotation: "true"},
	}}
}

func (f deleteNSFixture) secretExists(t *testing.T, ns, name string) bool {
	t.Helper()
	_, err := f.cs.CoreV1().Secrets(ns).Get(context.Background(), name, metav1.GetOptions{})
	if err != nil && !apierrors.IsNotFound(err) {
		t.Fatalf("get secret %s/%s: %v", ns, name, err)
	}
	return err == nil
}

func (f deleteNSFixture) namespaceExists(t *testing.T, name string) bool {
	t.Helper()
	_, err := f.cs.CoreV1().Namespaces().Get(context.Background(), name, metav1.GetOptions{})
	if err != nil && !apierrors.IsNotFound(err) {
		t.Fatalf("get namespace %s: %v", name, err)
	}
	return err == nil
}

func (f deleteNSFixture) crExists(t *testing.T, gvr schema.GroupVersionResource, ns, name string) bool {
	t.Helper()
	_, err := f.dyn.Resource(gvr).Namespace(ns).Get(context.Background(), name, metav1.GetOptions{})
	if err != nil && !apierrors.IsNotFound(err) {
		t.Fatalf("get %s %s/%s: %v", gvr.Resource, ns, name, err)
	}
	return err == nil
}

func TestDeleteProject_PurgeRemovesEverythingInKusoCreatedNamespace(t *testing.T) {
	f := newDeleteNSFixture(t, e2eNS, kusoCreatedNS(e2eNS))
	if err := f.s.DeleteWithOptions(context.Background(), "e2e", DeleteProjectOptions{PurgeData: true}); err != nil {
		t.Fatalf("DeleteWithOptions: %v", err)
	}
	for _, name := range []string{"e2e-api-run-a", "e2e-api-run-b"} {
		if f.crExists(t, kube.GVRRuns, e2eNS, name) {
			t.Errorf("KusoRun %s survived project delete", name)
		}
	}
	if f.crExists(t, kube.GVRCrons, e2eNS, "e2e-api-nightly") {
		t.Error("KusoCron survived project delete")
	}
	for _, name := range []string{"e2e-db-conn", "e2e-db-staging-conn", "kuso-backup-s3", "e2e-api-qa-pr-1-tls"} {
		if f.secretExists(t, e2eNS, name) {
			t.Errorf("Secret %s/%s survived purge delete", e2eNS, name)
		}
	}
	if f.namespaceExists(t, e2eNS) {
		t.Error("kuso-created project namespace survived purge delete")
	}
	if !f.namespaceExists(t, "kuso") {
		t.Fatal("home namespace deleted")
	}
	if !f.secretExists(t, "kuso", "kuso-backup-s3") {
		t.Error("home kuso-backup-s3 deleted")
	}
}

// Without --purge-data the addon PVCs stay so a recreate can reuse the data;
// the conn Secret holds the password that data was initialised with, so it
// must stay too, and so must the namespace the PVCs live in.
func TestDeleteProject_NoPurgeKeepsConnSecretsAndNamespace(t *testing.T) {
	f := newDeleteNSFixture(t, e2eNS, kusoCreatedNS(e2eNS))
	if err := f.s.Delete(context.Background(), "e2e"); err != nil {
		t.Fatalf("Delete: %v", err)
	}
	if f.crExists(t, kube.GVRRuns, e2eNS, "e2e-api-run-a") {
		t.Error("KusoRun survived project delete")
	}
	for _, name := range []string{"e2e-db-conn", "e2e-db-staging-conn"} {
		if !f.secretExists(t, e2eNS, name) {
			t.Errorf("conn Secret %s deleted without purge-data; retained PVC would lose its password", name)
		}
	}
	for _, name := range []string{"kuso-backup-s3", "e2e-api-qa-pr-1-tls"} {
		if f.secretExists(t, e2eNS, name) {
			t.Errorf("Secret %s survived project delete", name)
		}
	}
	if !f.namespaceExists(t, e2eNS) {
		t.Error("namespace deleted without purge-data; retained PVCs went with it")
	}
}

// A namespace the user pre-created (and labelled managed-by=kuso so kuso
// would adopt it) is not kuso's to delete, even on purge.
func TestDeleteProject_PurgeKeepsAdoptedNamespace(t *testing.T) {
	adopted := &corev1.Namespace{ObjectMeta: metav1.ObjectMeta{
		Name:   e2eNS,
		Labels: map[string]string{kube.ManagedByLabel: kube.ManagedByValue},
	}}
	f := newDeleteNSFixture(t, e2eNS, adopted)
	if err := f.s.DeleteWithOptions(context.Background(), "e2e", DeleteProjectOptions{PurgeData: true}); err != nil {
		t.Fatalf("DeleteWithOptions: %v", err)
	}
	if !f.namespaceExists(t, e2eNS) {
		t.Fatal("deleted a namespace kuso did not create")
	}
	if f.secretExists(t, e2eNS, "e2e-db-conn") || f.secretExists(t, e2eNS, "kuso-backup-s3") {
		t.Error("kuso-minted Secrets survived purge in adopted namespace")
	}
}

// Home-namespace project: never delete the home namespace or its backup
// credential, and never touch a sibling project's runs or cert Secrets.
func TestDeleteProject_PurgeInHomeNamespaceLeavesSiblingsAlone(t *testing.T) {
	f := newDeleteNSFixture(t, "kuso", nil)
	if err := f.s.DeleteWithOptions(context.Background(), "e2e", DeleteProjectOptions{PurgeData: true}); err != nil {
		t.Fatalf("DeleteWithOptions: %v", err)
	}
	if !f.namespaceExists(t, "kuso") {
		t.Fatal("home namespace deleted")
	}
	if !f.secretExists(t, "kuso", "kuso-backup-s3") {
		t.Error("home kuso-backup-s3 deleted")
	}
	if f.crExists(t, kube.GVRRuns, "kuso", "e2e-api-run-a") {
		t.Error("own KusoRun survived")
	}
	if !f.crExists(t, kube.GVRRuns, "kuso", "e2e-api-x-run-z") {
		t.Error("sibling project's KusoRun deleted")
	}
	if !f.secretExists(t, "kuso", "e2e-api-x-production-tls") {
		t.Error("sibling project e2e-api's cert Secret deleted")
	}
	if f.secretExists(t, "kuso", "e2e-db-conn") {
		t.Error("conn Secret survived purge")
	}
}
