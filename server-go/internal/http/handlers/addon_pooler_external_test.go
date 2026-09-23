package handlers

import (
	"encoding/json"
	"testing"

	apiv1 "github.com/sislelabs/kuso/api/apiv1"
)

// The chart grew pooler.externalBackend/host/port so PgBouncer can front a
// managed database, but the wire types only carried `enabled`. A PATCH
// setting the new fields came back with just {"enabled":true} — the pooler
// silently kept pointing at the in-cluster Service, which for an external
// addon does not exist at all.

func TestApiv1AddonMappingsCarryPoolerExternalBackend(t *testing.T) {
	create := apiv1CreateAddonToDomain(apiv1.CreateAddonRequest{
		Name: "psdb", Kind: "postgres",
		Pooler: &apiv1.AddonPoolerSpec{
			Enabled:         true,
			ExternalBackend: true,
			Host:            "aws-eu-central-1-1.pg.psdb.cloud",
			Port:            5432,
			PoolSize:        6,
		},
	})
	if create.Pooler == nil {
		t.Fatal("create mapping dropped the pooler block entirely")
	}
	if !create.Pooler.ExternalBackend {
		t.Error("create mapping dropped pooler.externalBackend")
	}
	if create.Pooler.Host != "aws-eu-central-1-1.pg.psdb.cloud" {
		t.Errorf("create mapping pooler.host = %q, want the provider host", create.Pooler.Host)
	}
	if create.Pooler.Port != 5432 {
		t.Errorf("create mapping pooler.port = %d, want 5432", create.Pooler.Port)
	}
	if create.Pooler.PoolSize != 6 {
		t.Errorf("create mapping dropped pooler.poolSize (got %d) — the pooler would keep the 25 default against a 9-connection backend", create.Pooler.PoolSize)
	}
}

func TestApiv1AddonUpdateCarriesPoolerExternalBackend(t *testing.T) {
	update := apiv1UpdateAddonToDomain(apiv1.UpdateAddonRequest{
		Pooler: &apiv1.AddonPoolerPatch{
			Enabled:         ptrTo(true),
			ExternalBackend: ptrTo(true),
			Host:            ptrTo("ext.example.com"),
			Port:            ptrTo[int32](6543),
		},
	})
	if update.Pooler == nil {
		t.Fatal("update mapping dropped the pooler patch entirely")
	}
	if update.Pooler.ExternalBackend == nil || !*update.Pooler.ExternalBackend {
		t.Error("update mapping dropped pooler.externalBackend")
	}
	if update.Pooler.Host == nil || *update.Pooler.Host != "ext.example.com" {
		t.Error("update mapping dropped pooler.host")
	}
	if update.Pooler.Port == nil || *update.Pooler.Port != 6543 {
		t.Error("update mapping dropped pooler.port")
	}
}

// The web toggle PATCHes only {"pooler":{"enabled":…}}. Absent sibling
// fields must stay nil ("leave alone"); as zero values they reset poolSize
// to the chart default of 25 and flip an external-backend pooler back to
// in-cluster.
func TestApiv1AddonUpdatePoolerToggleLeavesOtherFieldsAlone(t *testing.T) {
	var req apiv1.UpdateAddonRequest
	if err := json.Unmarshal([]byte(`{"pooler":{"enabled":false}}`), &req); err != nil {
		t.Fatal(err)
	}
	update := apiv1UpdateAddonToDomain(req)
	if update.Pooler == nil || update.Pooler.Enabled == nil || *update.Pooler.Enabled {
		t.Fatalf("toggle lost enabled=false: %+v", update.Pooler)
	}
	if update.Pooler.PoolSize != nil {
		t.Errorf("poolSize = %d, want nil (leave alone)", *update.Pooler.PoolSize)
	}
	if update.Pooler.ExternalBackend != nil {
		t.Errorf("externalBackend = %v, want nil (leave alone)", *update.Pooler.ExternalBackend)
	}
	if update.Pooler.Host != nil {
		t.Errorf("host = %q, want nil (leave alone)", *update.Pooler.Host)
	}
	if update.Pooler.Port != nil {
		t.Errorf("port = %d, want nil (leave alone)", *update.Pooler.Port)
	}
}

func ptrTo[T any](v T) *T { return &v }
