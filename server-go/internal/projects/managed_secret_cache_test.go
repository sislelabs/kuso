package projects

import (
	"context"
	"sync/atomic"
	"testing"
	"time"

	corev1 "k8s.io/api/core/v1"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/apimachinery/pkg/runtime"
	"k8s.io/apimachinery/pkg/runtime/schema"
	dynamicfake "k8s.io/client-go/dynamic/fake"
	kubefake "k8s.io/client-go/kubernetes/fake"
	k8stesting "k8s.io/client-go/testing"

	"kuso/server/internal/kube"
)

// SCL-1: the summary/describe enrichment did a live Secret GET for every
// service without a <svc>-secrets and for every env (it looked up
// <project>-<project>-<svc>-secrets, which never exists). On a synced
// cache the whole describe + enrich pass must issue zero live Secret GETs
// and still surface the service secret's keys on the env.
func TestDescribeEnrich_NoLiveSecretGetsOnSyncedCache(t *testing.T) {
	scheme := runtime.NewScheme()
	dyn := dynamicfake.NewSimpleDynamicClientWithCustomListKinds(scheme, map[schema.GroupVersionResource]string{
		kube.GVRKuso:         "KusoList",
		kube.GVRProjects:     "KusoProjectList",
		kube.GVRServices:     "KusoServiceList",
		kube.GVREnvironments: "KusoEnvironmentList",
		kube.GVRAddons:       "KusoAddonList",
		kube.GVRBuilds:       "KusoBuildList",
		kube.GVRCrons:        "KusoCronList",
		kube.GVRRuns:         "KusoRunList",
	})
	for _, sd := range []seed{
		seedProject("p", kube.KusoProjectSpec{DefaultRepo: &kube.KusoRepoRef{URL: "x"}}),
		seedService("p", "web", kube.KusoServiceSpec{Runtime: "dockerfile"}),
		seedService("p", "api", kube.KusoServiceSpec{Runtime: "dockerfile"}),
		seedEnv("p", "web", "production", "main", "p-web-production"),
		seedEnv("p", "api", "production", "main", "p-api-production"),
	} {
		if err := dyn.Tracker().Create(sd.gvr, sd.obj, sd.obj.GetNamespace()); err != nil {
			t.Fatalf("seed %s: %v", sd.obj.GetName(), err)
		}
	}
	cs := kubefake.NewSimpleClientset(&corev1.Secret{
		ObjectMeta: metav1.ObjectMeta{Name: kube.ServiceSecretName("p", "web"), Namespace: "kuso"},
		Data:       map[string][]byte{"WEB_TOKEN": []byte("v")},
	})
	kc := &kube.Client{Dynamic: dyn, Clientset: cs}
	kc.Cache = kube.NewCache(kc)
	kc.Cache.Start()
	t.Cleanup(kc.Cache.Stop)
	ctx, cancel := context.WithTimeout(context.Background(), 10*time.Second)
	defer cancel()
	if !kc.Cache.WaitForSync(ctx) {
		t.Fatal("informers did not sync")
	}

	var gets atomic.Int32
	cs.PrependReactor("get", "secrets", func(k8stesting.Action) (bool, runtime.Object, error) {
		gets.Add(1)
		return false, nil, nil
	})

	s := New(kc, "kuso")
	d, err := s.Describe(ctx, "p")
	if err != nil {
		t.Fatalf("describe: %v", err)
	}
	for i := range d.Services {
		s.EnrichServiceWithManagedSecretKeys(ctx, "p", shortServiceName("p", d.Services[i].Name), &d.Services[i])
	}
	for i := range d.Environments {
		s.EnrichEnvWithManagedSecretKeys(ctx, "p", &d.Environments[i])
	}
	if n := gets.Load(); n != 0 {
		t.Fatalf("describe+enrich issued %d live Secret GETs on a synced cache, want 0", n)
	}

	var webEnv *kube.KusoEnvironment
	for i := range d.Environments {
		if d.Environments[i].Name == "p-web-production" {
			webEnv = &d.Environments[i]
		}
	}
	if webEnv == nil || !hasEnvVar(webEnv.Spec.EnvVars, "WEB_TOKEN") {
		t.Fatalf("web env did not surface the service secret's key WEB_TOKEN: %+v", webEnv)
	}
}

func hasEnvVar(vars []kube.KusoEnvVar, name string) bool {
	for _, v := range vars {
		if v.Name == name {
			return true
		}
	}
	return false
}
