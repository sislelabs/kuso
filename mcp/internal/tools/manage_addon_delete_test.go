package tools

import (
	"context"
	"strings"
	"testing"

	"github.com/sislelabs/kuso/mcp/internal/config"
	"github.com/sislelabs/kuso/mcp/internal/kusoclient"
)

// The server's ?confirm=<addon-name> data-loss guard must be satisfied by
// a name the caller typed, not auto-filled from args.Name.
func TestManageAddonDeleteRequiresTypedName(t *testing.T) {
	client := kusoclient.New(&config.Config{URL: "http://127.0.0.1:1", Token: "test"})
	base := manageAddonArgs{Project: "shop", Action: "delete", Name: "db", Confirm: true}

	for _, typed := range []string{"", "other"} {
		args := base
		args.ConfirmName = typed
		_, err := runManageAddon(context.Background(), client, args)
		if err == nil || !strings.Contains(err.Error(), "confirmName") {
			t.Fatalf("confirmName=%q: want refusal naming confirmName, got %v", typed, err)
		}
		if strings.Contains(err.Error(), "dial") || strings.Contains(err.Error(), "refused") {
			t.Fatalf("confirmName=%q reached the network: %v", typed, err)
		}
	}

	args := base
	args.ConfirmName = "db"
	_, err := runManageAddon(context.Background(), client, args)
	if err == nil || strings.Contains(err.Error(), "confirmName") {
		t.Fatalf("matching confirmName should pass the gate and hit the (unroutable) server, got %v", err)
	}
}
