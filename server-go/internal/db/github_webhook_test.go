package db

import (
	"context"
	"testing"
)

// BUILD-02: the webhook handler only checks the dedup table on receipt and
// records the delivery after its dispatch succeeded, so the check must not
// record anything itself.
func TestGithubDeliveryHandled_DoesNotRecord(t *testing.T) {
	d := openTestDB(t)
	ctx := context.Background()
	for i := 0; i < 2; i++ {
		handled, err := d.GithubDeliveryHandled(ctx, "guid-1")
		if err != nil {
			t.Fatal(err)
		}
		if handled {
			t.Fatalf("check %d: unrecorded delivery reported handled", i)
		}
	}
	if _, err := d.SeenGithubDelivery(ctx, "guid-1", "push", 1); err != nil {
		t.Fatal(err)
	}
	if handled, err := d.GithubDeliveryHandled(ctx, "guid-1"); err != nil || !handled {
		t.Fatalf("recorded delivery: handled=%v err=%v", handled, err)
	}
}
