package handlers

import (
	"testing"

	apiv1 "github.com/sislelabs/kuso/api/apiv1"
)

// TestApiv1AddonMappingsCarryTLS pins the wire→domain mapping for the
// addon tls field in both directions of the CRUD surface. A dropped
// field here silently reverts users to sslmode=disable (the preview-DB
// cloner had exactly this bug class).
func TestApiv1AddonMappingsCarryTLS(t *testing.T) {
	create := apiv1CreateAddonToDomain(apiv1.CreateAddonRequest{
		Name: "pg", Kind: "postgres", TLS: "require",
	})
	if create.TLS != "require" {
		t.Errorf("create mapping tls = %q, want require", create.TLS)
	}

	req := "require"
	update := apiv1UpdateAddonToDomain(apiv1.UpdateAddonRequest{TLS: &req})
	if update.TLS == nil || *update.TLS != "require" {
		t.Errorf("update mapping tls = %v, want require", update.TLS)
	}
	if empty := apiv1UpdateAddonToDomain(apiv1.UpdateAddonRequest{}); empty.TLS != nil {
		t.Errorf("update mapping invented a tls patch: %v", *empty.TLS)
	}
}

// Same bug class as TLS above: dropping externalCredentials here made the API
// create the source Secret and then write an addon with no spec.external at
// all — the CLI reported success, but the addon wasn't external, so
// resync-external rejected it and no service ever saw the credentials.
func TestApiv1AddonMappingsCarryExternalCredentials(t *testing.T) {
	create := apiv1CreateAddonToDomain(apiv1.CreateAddonRequest{
		Name: "psdb", Kind: "postgres",
		ExternalCredentials: map[string]string{
			"DATABASE_URL": "postgres://u:p@ext.example.com:5432/db",
		},
	})
	if got := create.ExternalCredentials["DATABASE_URL"]; got != "postgres://u:p@ext.example.com:5432/db" {
		t.Errorf("create mapping externalCredentials = %v, want the DSN to survive", create.ExternalCredentials)
	}

	if bare := apiv1CreateAddonToDomain(apiv1.CreateAddonRequest{Name: "pg", Kind: "postgres"}); len(bare.ExternalCredentials) != 0 {
		t.Errorf("create mapping invented credentials: %v", bare.ExternalCredentials)
	}
}
