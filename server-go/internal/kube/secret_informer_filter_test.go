package kube

import (
	"context"
	"testing"
	"time"

	corev1 "k8s.io/api/core/v1"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/apimachinery/pkg/fields"
	"k8s.io/apimachinery/pkg/runtime"
	"k8s.io/client-go/kubernetes/fake"
	k8stesting "k8s.io/client-go/testing"
)

// Helm release Secrets were 20.2 of the Secret informer's 22.3 MB live, and
// no cache consumer reads them. The informer must ask the apiserver to leave
// them out. The fake clientset ignores field selectors, so the reactor below
// applies the selector the way the apiserver would.
func TestSecretInformerExcludesHelmReleases(t *testing.T) {
	regular := &corev1.Secret{
		ObjectMeta: metav1.ObjectMeta{Name: "svc-secrets", Namespace: "kuso"},
		Data:       map[string][]byte{"A_KEY": []byte("v")},
	}
	release := &corev1.Secret{
		ObjectMeta: metav1.ObjectMeta{Name: "sh.helm.release.v1.svc.v1", Namespace: "kuso", Labels: map[string]string{"owner": "helm"}},
		Type:       "helm.sh/release.v1",
		Data:       map[string][]byte{"release": []byte("big")},
	}
	cs := fake.NewSimpleClientset()
	cs.PrependReactor("list", "secrets", func(a k8stesting.Action) (bool, runtime.Object, error) {
		sel := fields.Everything()
		if r := a.(k8stesting.ListAction).GetListRestrictions().Fields; r != nil {
			sel = r
		}
		out := &corev1.SecretList{}
		for _, s := range []*corev1.Secret{regular, release} {
			if sel.Matches(fields.Set{"type": string(s.Type)}) {
				out.Items = append(out.Items, *s)
			}
		}
		return true, out, nil
	})

	c := &Client{Clientset: cs, Dynamic: fakeClient(t).Dynamic}
	c.Cache = NewCache(c)
	c.Cache.Start()
	t.Cleanup(c.Cache.Stop)
	ctx, cancel := context.WithTimeout(context.Background(), 10*time.Second)
	defer cancel()
	if !c.Cache.WaitForSync(ctx) {
		t.Fatal("informers did not sync")
	}

	secs, ok := c.Cache.ListSecrets("kuso")
	if !ok {
		t.Fatal("ListSecrets: cache not ready")
	}
	if len(secs) != 1 || secs[0].Name != "svc-secrets" {
		names := []string{}
		for _, s := range secs {
			names = append(names, s.Name)
		}
		t.Fatalf("cached secrets = %v, want only svc-secrets", names)
	}
	if keys, ok := c.Cache.SecretKeysOnly("kuso", "svc-secrets"); !ok || len(keys) != 1 {
		t.Errorf("SecretKeysOnly = %v, %v; want [A_KEY], true", keys, ok)
	}
}
