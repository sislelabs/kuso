package main

import (
	"context"
	"testing"
	"time"

	corev1 "k8s.io/api/core/v1"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/client-go/kubernetes/fake"
)

func TestWaitForConnSecrets(t *testing.T) {
	cs := fake.NewSimpleClientset()
	envFrom := []string{"p-db-qa-conn", "p-api-secrets"} // non-conn secrets are not awaited

	ctx, cancel := context.WithTimeout(context.Background(), 50*time.Millisecond)
	defer cancel()
	if err := waitForConnSecrets(ctx, cs, "ns", envFrom, 5*time.Millisecond); err == nil {
		t.Fatal("returned before the conn secret existed")
	}

	go func() {
		time.Sleep(20 * time.Millisecond)
		_, _ = cs.CoreV1().Secrets("ns").Create(context.Background(),
			&corev1.Secret{ObjectMeta: metav1.ObjectMeta{Name: "p-db-qa-conn", Namespace: "ns"}}, metav1.CreateOptions{})
	}()
	ctx2, cancel2 := context.WithTimeout(context.Background(), time.Second)
	defer cancel2()
	if err := waitForConnSecrets(ctx2, cs, "ns", envFrom, 5*time.Millisecond); err != nil {
		t.Fatalf("should succeed once the secret appears: %v", err)
	}
}
