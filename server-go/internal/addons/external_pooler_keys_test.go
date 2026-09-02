package addons

import (
	"context"
	"strings"
	"testing"

	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"

	"kuso/server/internal/kube"
)

// An in-cluster postgres addon with a pooler publishes POOLER_HOST/PORT/URL in
// its conn secret, so apps opt in with ${{ <addon>.POOLER_URL }}. External
// addons got no such keys: their conn secret is a verbatim mirror of the user's
// Secret, and the chart block that renders pooler keys is gated on
// (not .Values.external).
//
// So an external addon could HAVE a running pooler serving traffic while
// ${{ psdb.POOLER_URL }} failed to resolve — leaving a literal DSN, with the
// database password sitting in plaintext in the service's env vars, as the only
// way to point an app at it.
//
// The pooler is kuso's own Service either way; its address is derivable from
// the addon name, and the credentials are already in the mirrored secret.
func TestMirrorExternalSecret_AddsPoolerKeysWhenPoolerEnabled(t *testing.T) {
	t.Parallel()
	s := fakeServiceWithSecrets(t, seedProj("tickero"))

	if _, err := s.Add(context.Background(), "tickero", CreateAddonRequest{
		Name: "psdb",
		Kind: "postgres",
		ExternalCredentials: map[string]string{
			"DATABASE_URL":      "postgres://u:pw@ext.example.com:5432/db?sslmode=require",
			"POSTGRES_USER":     "u",
			"POSTGRES_PASSWORD": "pw",
			"POSTGRES_DB":       "db",
		},
		Pooler: &kube.KusoAddonPooler{
			Enabled:         true,
			ExternalBackend: true,
			Host:            "ext.example.com",
			Port:            5432,
		},
	}); err != nil {
		t.Fatalf("Add: %v", err)
	}

	conn, err := s.Kube.Clientset.CoreV1().Secrets("kuso").
		Get(context.Background(), "tickero-psdb-conn", metav1.GetOptions{})
	if err != nil {
		t.Fatalf("conn secret: %v", err)
	}

	if got := string(conn.Data["POOLER_HOST"]); got != "tickero-psdb-pooler" {
		t.Errorf("POOLER_HOST = %q, want tickero-psdb-pooler", got)
	}
	if got := string(conn.Data["POOLER_PORT"]); got != "6432" {
		t.Errorf("POOLER_PORT = %q, want 6432", got)
	}
	url := string(conn.Data["POOLER_URL"])
	if url == "" {
		t.Fatal("POOLER_URL is empty — ${{ psdb.POOLER_URL }} can't resolve, so " +
			"pointing an app at the pooler needs an inline DSN with the password in it")
	}
	// PgBouncer serves plaintext on :6432; a require/verify DSN fails the
	// TLS negotiation against it (same as the in-cluster pooler URL).
	for _, want := range []string{"tickero-psdb-pooler:6432", "sslmode=disable", "u:pw@"} {
		if !strings.Contains(url, want) {
			t.Errorf("POOLER_URL = %q, missing %q", url, want)
		}
	}
}

// Without a pooler the keys must stay absent rather than advertise an address
// nothing is listening on.
func TestMirrorExternalSecret_NoPoolerKeysWhenDisabled(t *testing.T) {
	t.Parallel()
	s := fakeServiceWithSecrets(t, seedProj("tickero"))

	if _, err := s.Add(context.Background(), "tickero", CreateAddonRequest{
		Name: "psdb2",
		Kind: "postgres",
		ExternalCredentials: map[string]string{
			"DATABASE_URL": "postgres://u:pw@ext.example.com:5432/db",
		},
	}); err != nil {
		t.Fatalf("Add: %v", err)
	}

	conn, err := s.Kube.Clientset.CoreV1().Secrets("kuso").
		Get(context.Background(), "tickero-psdb2-conn", metav1.GetOptions{})
	if err != nil {
		t.Fatalf("conn secret: %v", err)
	}
	if _, ok := conn.Data["POOLER_URL"]; ok {
		t.Error("POOLER_URL present with no pooler enabled")
	}
}
