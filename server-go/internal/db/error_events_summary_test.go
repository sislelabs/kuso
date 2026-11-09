package db

import (
	"context"
	"testing"
	"time"
)

func TestSummarizeProjectErrors(t *testing.T) {
	d := openTestDB(t)
	ctx := context.Background()
	now := time.Now().UTC()
	for _, e := range []ErrorEvent{
		{Project: "p", Service: "p-api", Env: "production", Fingerprint: "a", Message: "boom", Ts: now},
		{Project: "p", Service: "p-api", Env: "production", Fingerprint: "a", Message: "boom", Ts: now},
		{Project: "p", Service: "p-api", Env: "staging", Fingerprint: "b", Message: "other", Ts: now},
		{Project: "p", Service: "p-web", Env: "production", Fingerprint: "c", Message: "x", Ts: now},
		{Project: "p", Service: "p-web", Env: "production", Fingerprint: "c", Message: "old", Ts: now.Add(-48 * time.Hour)},
		{Project: "q", Service: "q-api", Env: "production", Fingerprint: "a", Message: "other project", Ts: now},
	} {
		if err := d.InsertErrorEvent(ctx, e); err != nil {
			t.Fatal(err)
		}
	}
	since := now.Add(-24 * time.Hour)

	all, err := d.SummarizeProjectErrors(ctx, "p", "", since, nil)
	if err != nil {
		t.Fatal(err)
	}
	if len(all) != 2 {
		t.Fatalf("want 2 services, got %+v", all)
	}
	if all[0].Service != "p-api" || all[0].Occurrences != 3 || all[0].Groups != 2 {
		t.Errorf("p-api = %+v; want 3 occurrences in 2 groups", all[0])
	}
	if all[1].Service != "p-web" || all[1].Occurrences != 1 || all[1].Groups != 1 {
		t.Errorf("p-web = %+v; want 1 occurrence (the 48h-old one is outside the window)", all[1])
	}

	prod, err := d.SummarizeProjectErrors(ctx, "p", "production", since, nil)
	if err != nil {
		t.Fatal(err)
	}
	if len(prod) != 2 || prod[0].Occurrences != 2 || prod[0].Groups != 1 {
		t.Errorf("production = %+v; want p-api with 2 occurrences in 1 group", prod)
	}
}
