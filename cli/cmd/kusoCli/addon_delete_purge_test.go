package kusoCli

import (
	"net/http"
	"net/http/httptest"
	"testing"

	"kuso/pkg/kusoApi"
)

// BLOB-R7: the API took purgeData but the CLI had no way to send it.
func TestAddonDelete_PurgeDataFlag(t *testing.T) {
	var queries []string
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		queries = append(queries, r.Method+" "+r.URL.Path+"?"+r.URL.RawQuery)
		w.WriteHeader(http.StatusNoContent)
	}))
	t.Cleanup(srv.Close)
	api = &kusoApi.KusoClient{}
	api.Init(srv.URL, "test-token")
	t.Cleanup(func() { api = nil; addonDeletePurge = false; addonDeleteYes = false })

	for args, want := range map[*[]string]string{
		{"project", "addon", "delete", "shop", "files", "-y"}:                 "DELETE /api/projects/shop/addons/files?confirm=files",
		{"project", "addon", "delete", "shop", "files", "-y", "--purge-data"}: "DELETE /api/projects/shop/addons/files?confirm=files&purgeData=true",
	} {
		queries = nil
		addonDeletePurge = false
		captureStdout(t, func() {
			if _, err := runRoot(t, (*args)...); err != nil {
				t.Fatalf("%v: %v", *args, err)
			}
		})
		if len(queries) != 1 || queries[0] != want {
			t.Errorf("%v: sent %v, want %q", *args, queries, want)
		}
	}
}
