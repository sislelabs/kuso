package db

import (
	"context"
	"testing"
	"time"
)

// The Errors tab mixed staging and production in one list: the endpoint
// had no env filter, so staging noise read as production errors.
func TestListErrorGroups_FiltersByEnv(t *testing.T) {
	d := openTestDB(t)
	ctx := context.Background()
	now := time.Now().UTC()
	for _, e := range []ErrorEvent{
		{Project: "p", Service: "p-api", Env: "production", Pod: "p1", Fingerprint: "a", Message: "boom", Ts: now},
		{Project: "p", Service: "p-api", Env: "staging", Pod: "s1", Fingerprint: "a", Message: "boom", Ts: now},
		{Project: "p", Service: "p-api", Env: "staging", Pod: "s1", Fingerprint: "b", Message: "other", Ts: now},
	} {
		if err := d.InsertErrorEvent(ctx, e); err != nil {
			t.Fatal(err)
		}
	}
	since := now.Add(-time.Hour)
	all, err := d.ListErrorGroups(ctx, "p", "p-api", "", since, 50, 0)
	if err != nil || len(all) != 2 {
		t.Fatalf("all envs: %d groups, err %v; want 2", len(all), err)
	}
	prod, err := d.ListErrorGroups(ctx, "p", "p-api", "production", since, 50, 0)
	if err != nil {
		t.Fatal(err)
	}
	if len(prod) != 1 || prod[0].Count != 1 || prod[0].SampleEnv != "production" || prod[0].SamplePod != "p1" {
		t.Fatalf("production groups = %+v", prod)
	}
}
