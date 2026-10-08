package kusoCli

import (
	"io"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"

	"kuso/pkg/kusoApi"
)

// The server gates node-local volumes on ?force=true, separate from the
// body's drain force. --force alone used to hit the 409 forever.
func TestNodeRemove_AcceptDataLossSetsQueryForce(t *testing.T) {
	var queries []string
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		_, _ = io.ReadAll(r.Body)
		queries = append(queries, r.URL.RawQuery)
		w.Header().Set("Content-Type", "application/json")
		if r.URL.Query().Get("force") != "true" {
			w.WriteHeader(http.StatusConflict)
			_, _ = io.WriteString(w, `{"error":"1 volume(s) store data only on w2 — move or back them up, or retry with force=true","pinned":[{"namespace":"kuso","pvc":"data-db-0","kind":"addon","owner":"shop-db","size":"5Gi"}]}`)
			return
		}
		_, _ = io.WriteString(w, `{"removed":"w2"}`)
	}))
	t.Cleanup(srv.Close)
	api = &kusoApi.KusoClient{}
	api.Init(srv.URL, "test-token")
	t.Cleanup(func() { api = nil; nodeRemoveForce, nodeRemoveAcceptDataLoss, nodeRemoveYes = false, false, false })

	_, err := runRoot(t, "node", "remove", "w2", "--force", "--yes")
	if err == nil {
		t.Fatal("want the pinned-volume refusal without --accept-data-loss")
	}
	if !strings.Contains(err.Error(), "data-db-0") || !strings.Contains(err.Error(), "--accept-data-loss") {
		t.Fatalf("refusal should list the pinned volume and the flag, got: %v", err)
	}

	captureStdout(t, func() {
		if _, err := runRoot(t, "node", "remove", "w2", "--accept-data-loss", "--yes"); err != nil {
			t.Fatalf("remove with --accept-data-loss: %v", err)
		}
	})
	if last := queries[len(queries)-1]; last != "force=true" {
		t.Fatalf("want ?force=true on the request, got %q", last)
	}
}
