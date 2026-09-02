package kube

import (
	"strings"
	"testing"
)

// addonChartDir and helmTemplateAddon are defined in addon_conn_secret_test.go.

// A postgres addon backed by an external managed database (PlanetScale, Neon,
// RDS) renders no workload of its own — the user's Secret is the connection
// source. Pooling such a backend is still useful, and on providers with a low
// connection cap it's the only way to run more than a couple of app pods:
// PgBouncer multiplexes many client connections onto a few real ones.
//
// It stays opt-in behind pooler.externalBackend because pooling a database you
// don't operate has different failure modes than pooling the in-cluster pod —
// a pooler restart drops connections to a backend kuso can't restart in turn.

func TestPooler_ExternalBackendOffByDefault(t *testing.T) {
	t.Parallel()

	out := helmTemplateAddon(t, "postgres",
		"external.secretName=my-planetscale",
		"pooler.enabled=true",
	)

	if strings.Contains(out, "-pooler") {
		t.Errorf("external addon rendered a pooler without pooler.externalBackend; "+
			"pooling a remote backend must stay opt-in.\nRendered:\n%s", out)
	}
}

func TestPooler_ExternalBackendRendersWhenOptedIn(t *testing.T) {
	t.Parallel()

	out := helmTemplateAddon(t, "postgres",
		"external.secretName=my-planetscale",
		"pooler.enabled=true",
		"pooler.externalBackend=true",
		"pooler.host=aws-eu-central-1-1.pg.psdb.cloud",
		"pooler.port=5432",
	)

	for _, want := range []string{
		"test-addon-pooler",
		"host=aws-eu-central-1-1.pg.psdb.cloud",
		"port=5432",
	} {
		if !strings.Contains(out, want) {
			t.Errorf("rendered pooler missing %q.\nRendered:\n%s", want, out)
		}
	}

	// The in-cluster Service name must NOT be the backend for an external addon.
	if strings.Contains(out, "* = host=test-addon port=5432") {
		t.Errorf("external pooler still points at the in-cluster Service.\nRendered:\n%s", out)
	}
}

// Managed providers mandate TLS. PgBouncer talks to backends unencrypted
// unless told otherwise, so without this the backend login silently fails.
func TestPooler_ExternalBackendRequiresServerTLS(t *testing.T) {
	t.Parallel()

	out := helmTemplateAddon(t, "postgres",
		"external.secretName=my-planetscale",
		"pooler.enabled=true",
		"pooler.externalBackend=true",
		"pooler.host=aws-eu-central-1-1.pg.psdb.cloud",
	)

	if !strings.Contains(out, "server_tls_sslmode") {
		t.Errorf("external pooler must set server_tls_sslmode; a managed backend "+
			"rejects the plaintext login PgBouncer defaults to.\nRendered:\n%s", out)
	}
}

// The in-cluster pooler must keep pointing at the addon's own Service, and
// must not start demanding TLS to a backend that serves plaintext by default.
func TestPooler_InClusterBackendUnchanged(t *testing.T) {
	t.Parallel()

	out := helmTemplateAddon(t, "postgres", "pooler.enabled=true")

	if !strings.Contains(out, "* = host=test-addon port=5432") {
		t.Errorf("in-cluster pooler no longer targets the addon Service.\nRendered:\n%s", out)
	}
	if strings.Contains(out, "server_tls_sslmode") {
		t.Errorf("in-cluster pooler must not force backend TLS.\nRendered:\n%s", out)
	}
}

// pgx sends these as startup parameters; PgBouncer's transaction pooling mode
// rejects unknown ones outright ("unsupported startup parameter"), which kills
// the pool on connect. Every Go app on the platform hits this, so the chart
// ignores them rather than making each app work around it.
func TestPooler_IgnoresTimeoutStartupParameters(t *testing.T) {
	t.Parallel()

	out := helmTemplateAddon(t, "postgres", "pooler.enabled=true")

	for _, param := range []string{"statement_timeout", "idle_in_transaction_session_timeout"} {
		if !strings.Contains(out, param) {
			t.Errorf("ignore_startup_parameters is missing %q, so pgx-based apps "+
				"crash on connect through the pooler.\nRendered:\n%s", param, out)
		}
	}
}
