package tools

import (
	"context"
	"net/http"
	"net/http/httptest"
	"net/url"
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

// The description claimed delete destroys data, but the server keeps it
// unless purgeData; and the X-Kuso-Warning on a landed delete was lost.
func TestManageAddonDelete_PurgeDataAndWarning(t *testing.T) {
	var gotQuery url.Values
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		gotQuery = r.URL.Query()
		w.Header().Set("X-Kuso-Warning", "env refresh failed")
		w.WriteHeader(http.StatusNoContent)
	}))
	defer srv.Close()
	client := kusoclient.New(&config.Config{URL: srv.URL, Token: "test"})
	out, err := runManageAddon(context.Background(), client, manageAddonArgs{
		Project: "shop", Action: "delete", Name: "db", ConfirmName: "db", Confirm: true, PurgeData: true,
	})
	if err != nil {
		t.Fatal(err)
	}
	if gotQuery.Get("purgeData") != "true" || gotQuery.Get("confirm") != "db" {
		t.Fatalf("query %v", gotQuery)
	}
	if out.Warning != "env refresh failed" || !strings.Contains(manageAddonText(out), "env refresh failed") {
		t.Fatalf("warning lost: %+v", out)
	}
}
