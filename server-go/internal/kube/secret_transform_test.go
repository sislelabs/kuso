package kube

import (
	"testing"

	corev1 "k8s.io/api/core/v1"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
)

// The Secret informer caches every Secret in the cluster — addon
// passwords, clone tokens, JWT signing keys. stripSecretData is what
// keeps their VALUES out of that cache: only key names are needed (for
// managed-secret enrichment on the dashboard), so anything more is
// permanent, needless exposure in every replica's heap.
//
// This test is the guard on that property. If it fails, the cache has
// started retaining plaintext secrets.
func TestStripSecretData_RemovesValuesKeepsKeys(t *testing.T) {
	in := &corev1.Secret{
		ObjectMeta: metav1.ObjectMeta{Name: "db-conn", Namespace: "kuso"},
		Data: map[string][]byte{
			"POSTGRES_PASSWORD": []byte("super-secret-value"),
			"DATABASE_URL":      []byte("postgres://u:p@h/db"),
		},
		StringData: map[string]string{"PLAIN": "also-secret"},
	}

	got, err := stripSecretData(in)
	if err != nil {
		t.Fatalf("stripSecretData: %v", err)
	}
	sec, ok := got.(*corev1.Secret)
	if !ok {
		t.Fatalf("got %T, want *corev1.Secret", got)
	}

	// Keys must survive — they're the whole point of the cache.
	for _, k := range []string{"POSTGRES_PASSWORD", "DATABASE_URL"} {
		if _, present := sec.Data[k]; !present {
			t.Errorf("key %q was dropped; the key listing path needs it", k)
		}
	}
	// Values must be gone.
	for k, v := range sec.Data {
		if len(v) != 0 {
			t.Errorf("key %q retained a value (%d bytes) — secret VALUES must never enter the cache", k, len(v))
		}
	}
	if sec.StringData != nil {
		t.Errorf("StringData retained: %v — must be cleared", sec.StringData)
	}
	// Metadata must survive so the lister can find it by name.
	if sec.Name != "db-conn" || sec.Namespace != "kuso" {
		t.Errorf("metadata mangled: %s/%s", sec.Namespace, sec.Name)
	}

	// The ORIGINAL object must not be mutated: the informer may hand us
	// an object shared with other cache consumers.
	if string(in.Data["POSTGRES_PASSWORD"]) != "super-secret-value" {
		t.Error("stripSecretData mutated its input; it must copy before clearing")
	}
}

// Non-Secret objects (and tombstones) pass through untouched.
func TestStripSecretData_PassesThroughOtherTypes(t *testing.T) {
	pod := &corev1.Pod{ObjectMeta: metav1.ObjectMeta{Name: "p"}}
	got, err := stripSecretData(pod)
	if err != nil {
		t.Fatalf("stripSecretData: %v", err)
	}
	if got != any(pod) {
		t.Error("non-Secret object should pass through unchanged")
	}
}
