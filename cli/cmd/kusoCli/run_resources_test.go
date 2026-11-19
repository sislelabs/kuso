package kusoCli

import (
	"encoding/json"
	"io"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"

	"kuso/pkg/kusoApi"
)

// CLI-36: neither a run's resources nor a service's tcpLiveness could be
// sent, so a heavy migration couldn't get more memory and the TCP probe
// couldn't be turned off after a runtime change.
func TestRunAndServiceSetSendNewFields(t *testing.T) {
	bodies := map[string]string{}
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		b, _ := io.ReadAll(r.Body)
		if len(b) > 0 {
			bodies[r.Method+" "+r.URL.Path] = string(b)
		}
		w.Header().Set("Content-Type", "application/json")
		_, _ = io.WriteString(w, `{"metadata":{"name":"r1"}}`)
	}))
	t.Cleanup(srv.Close)
	api = &kusoApi.KusoClient{}
	api.Init(srv.URL, "test-token")
	t.Cleanup(func() {
		api = nil
		runMemory, serviceSetTCPLiveness = "", ""
		runCmd.Flags().Lookup("memory").Changed = false
		serviceSetCmd.Flags().Lookup("tcp-liveness").Changed = false
	})

	captureStdout(t, func() {
		if _, err := runRoot(t, "run", "shop", "api", "--memory", "2Gi", "--", "migrate"); err != nil {
			t.Fatal(err)
		}
		if _, err := runRoot(t, "project", "service", "set", "shop", "api", "--tcp-liveness", "off"); err != nil {
			t.Fatal(err)
		}
	})
	var run kusoApi.CreateRunRequest
	_ = json.Unmarshal([]byte(bodies["POST /api/projects/shop/services/api/runs"]), &run)
	if run.Resources == nil || run.Resources.Limits["memory"] != "2Gi" {
		t.Errorf("run body %q", bodies["POST /api/projects/shop/services/api/runs"])
	}
	var patched string
	for k, v := range bodies {
		if strings.HasPrefix(k, "PATCH ") {
			patched = v
		}
	}
	if !strings.Contains(patched, `"tcpLiveness":false`) {
		t.Errorf("patch body %q (all %v)", patched, bodies)
	}
}
