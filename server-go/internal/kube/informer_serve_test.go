package kube

import (
	"context"
	"testing"
	"time"

	batchv1 "k8s.io/api/batch/v1"
	corev1 "k8s.io/api/core/v1"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/apimachinery/pkg/labels"
	"k8s.io/client-go/kubernetes/fake"
)

// End-to-end: the Job + Secret informers must actually start, sync, and
// serve — and the Secret path must serve KEYS while holding NO values.
func TestJobAndSecretInformersServe(t *testing.T) {
	cs := fake.NewSimpleClientset(
		&batchv1.Job{ObjectMeta: metav1.ObjectMeta{
			Name: "b1", Namespace: "kuso",
			Labels: map[string]string{"kuso.sislelabs.com/cron": "nightly"},
		}},
		&corev1.Secret{
			ObjectMeta: metav1.ObjectMeta{Name: "svc-secrets", Namespace: "kuso"},
			Data:       map[string][]byte{"B_KEY": []byte("v2"), "A_KEY": []byte("v1")},
		},
	)
	c := &Client{Clientset: cs, Dynamic: fakeClient(t).Dynamic}
	c.Cache = NewCache(c)
	c.Cache.Start()
	t.Cleanup(c.Cache.Stop)

	ctx, cancel := context.WithTimeout(context.Background(), 10*time.Second)
	defer cancel()
	if !c.Cache.WaitForSync(ctx) {
		t.Fatal("informers did not sync")
	}

	if _, ok := c.Cache.GetJob("kuso", "b1"); !ok {
		t.Error("GetJob: job not served from cache")
	}
	sel, _ := labels.Parse("kuso.sislelabs.com/cron")
	if jobs, ok := c.Cache.ListJobs("", sel); !ok || len(jobs) != 1 {
		t.Errorf("ListJobs: ok=%v n=%d, want ok=true n=1", ok, len(jobs))
	}
	keys, ok := c.Cache.SecretKeysOnly("kuso", "svc-secrets")
	if !ok {
		t.Fatal("SecretKeysOnly: not served from cache")
	}
	if len(keys) != 2 || keys[0] != "A_KEY" || keys[1] != "B_KEY" {
		t.Errorf("keys = %v, want sorted [A_KEY B_KEY]", keys)
	}
	// The cached Secret must carry no values.
	sec, err := c.Cache.secretLister.Secrets("kuso").Get("svc-secrets")
	if err != nil {
		t.Fatalf("lister get: %v", err)
	}
	for k, v := range sec.Data {
		if len(v) != 0 {
			t.Errorf("cached secret key %q holds %d bytes — values must be stripped", k, len(v))
		}
	}
}
