package db

import (
	"context"
	"fmt"
	"testing"
	"time"
)

// BUILD-02: the webhook handler only checks the dedup table on receipt and
// records the delivery after its dispatch succeeded, so the check must not
// record anything itself.
func TestGithubDeliveryHandled_DoesNotRecord(t *testing.T) {
	d := openTestDB(t)
	ctx := context.Background()
	// openTestDB does not truncate GithubWebhookDelivery.
	guid := fmt.Sprintf("guid-%d", time.Now().UnixNano())
	for i := 0; i < 2; i++ {
		handled, err := d.GithubDeliveryHandled(ctx, guid)
		if err != nil {
			t.Fatal(err)
		}
		if handled {
			t.Fatalf("check %d: unrecorded delivery reported handled", i)
		}
	}
	if _, err := d.SeenGithubDelivery(ctx, guid, "push", 1); err != nil {
		t.Fatal(err)
	}
	if handled, err := d.GithubDeliveryHandled(ctx, guid); err != nil || !handled {
		t.Fatalf("recorded delivery: handled=%v err=%v", handled, err)
	}
}
