package handlers

import (
	"context"
	"net/url"
	"testing"
	"time"

	corev1 "k8s.io/api/core/v1"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/client-go/kubernetes/fake"

	"kuso/server/internal/kube"
)

// ?timeout=2h was ignored (anything >= 1h fell back to 5m), so every dump
// longer than five minutes was cut off.
func TestDumpTimeout(t *testing.T) {
	t.Parallel()
	for in, want := range map[string]time.Duration{
		"":    5 * time.Minute,
		"bad": 5 * time.Minute,
		"30m": 30 * time.Minute,
		"2h":  2 * time.Hour,
		"72h": maxDumpTimeout,
	} {
		if got := dumpTimeout(in); got != want {
			t.Errorf("dumpTimeout(%q) = %s, want %s", in, got, want)
		}
	}
}

// The DSN was formatted by hand, so a password with @ / ? # : broke it.
func TestAddonDSN_EscapesPassword(t *testing.T) {
	t.Parallel()
	const pw = "p@ss/w?rd#:x"
	cs := fake.NewSimpleClientset(&corev1.Secret{
		ObjectMeta: metav1.ObjectMeta{Name: "shop-db-conn", Namespace: "kuso"},
		Data: map[string][]byte{
			"POSTGRES_HOST": []byte("shop-db"), "POSTGRES_USER": []byte("app"),
			"POSTGRES_DB": []byte("shop"), "POSTGRES_PASSWORD": []byte(pw),
		},
	})
	h := &BackupsHandler{Kube: &kube.Client{Clientset: cs}}
	dsn, err := h.addonDSN(context.Background(), "kuso", "shop-db")
	if err != nil {
		t.Fatal(err)
	}
	u, err := url.Parse(dsn)
	if err != nil {
		t.Fatalf("DSN %q does not parse: %v", dsn, err)
	}
	if got, _ := u.User.Password(); got != pw || u.Path != "/shop" {
		t.Errorf("parsed password %q path %q from %q", got, u.Path, dsn)
	}
}
