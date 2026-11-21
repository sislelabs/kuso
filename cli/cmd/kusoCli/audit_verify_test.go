package kusoCli

import (
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"

	"kuso/pkg/kusoApi"
)

func TestAuditVerify_BrokenChainFailsAndNamesSeq(t *testing.T) {
	var got string
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		got = r.URL.Path + "?" + r.URL.RawQuery
		_, _ = w.Write([]byte(`{"ok":false,"checkedAt":"2026-11-21T10:00:00Z","unchainedSinceStart":0,
"chains":[{"chain":"security","ok":false,"floorSeq":1,"headSeq":9,"headHash":"aa","firstSeq":1,"rows":4,
"break":{"seq":5,"reason":"row content changed since it was written","expected":"e1","actual":"a1"}},
{"chain":"routine","ok":true,"floorSeq":1,"headSeq":3,"headHash":"bb","firstSeq":1,"rows":3}],
"checkpoint":{"chain":"security","seq":9,"hash":"cc","ok":false,"reason":"stored hash differs"}}`))
	}))
	t.Cleanup(srv.Close)
	api = &kusoApi.KusoClient{}
	api.Init(srv.URL, "test-token")
	t.Cleanup(func() { api = nil; auditVerifyChain, auditVerifySeq, auditVerifyHash = "", 0, "" })

	var err error
	out := captureStdout(t, func() {
		_, err = runRoot(t, "audit", "verify", "--chain", "security", "--seq", "9", "--hash", "cc")
	})
	if err == nil {
		t.Fatal("broken chain must exit non-zero")
	}
	if got != "/api/audit/verify?chain=security&hash=cc&seq=9" {
		t.Errorf("request = %q", got)
	}
	for _, want := range []string{"BROKEN", "breaks at seq 5", "expected e1", "does NOT match"} {
		if !strings.Contains(out, want) {
			t.Errorf("output missing %q:\n%s", want, out)
		}
	}

	auditVerifyChain, auditVerifySeq, auditVerifyHash = "", 0, ""
	if _, err := runRoot(t, "audit", "verify", "--chain", "security"); err == nil || !strings.Contains(err.Error(), "go together") {
		t.Errorf("partial checkpoint flags: err = %v", err)
	}
}
