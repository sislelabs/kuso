package tools

import (
	"net/http"
	"strings"
	"testing"
)

func TestGetVulnerabilities_PathQueryAndOrder(t *testing.T) {
	var gotPath, gotQuery string
	sess, reqs := newRecordingSession(t, func(r *http.Request) (int, string) {
		gotPath, gotQuery = r.URL.EscapedPath(), r.URL.RawQuery
		return http.StatusOK, `{"effectiveMode":"enforce","policy":"inherit",
			"image":{"imageRef":"reg/web:1","digest":"sha256:abc","scan":{"id":3,"status":"succeeded","reason":"gate","counts":{"critical":1,"fixableCritical":1}}},
			"findings":[{"vulnId":"CVE-9","severity":"LOW"},{"vulnId":"CVE-2","severity":"CRITICAL","fixedVersion":"1.2"}]}`
	})

	text, isErr, err := callText(t, sess, "get_vulnerabilities", map[string]any{
		"project": "shop", "service": "web", "env": "staging", "severity": "critical, low",
	})
	if err != nil || isErr {
		t.Fatalf("call failed: err=%v isErr=%v text=%s", err, isErr, text)
	}
	if gotPath != "/api/projects/shop/services/web/vulns" || gotQuery != "env=staging&severity=CRITICAL%2CLOW" {
		t.Errorf("request = %s?%s", gotPath, gotQuery)
	}
	if strings.Index(text, "CVE-2") > strings.Index(text, "CVE-9") {
		t.Errorf("CRITICAL finding should be listed before LOW:\n%s", text)
	}
	if !strings.Contains(text, "gate mode: enforce") {
		t.Errorf("missing gate mode:\n%s", text)
	}

	if _, _, err := callText(t, sess, "get_vulnerabilities", map[string]any{"project": "shop", "addon": "db"}); err != nil {
		t.Fatal(err)
	}
	if gotPath != "/api/projects/shop/addons/db/vulns" || gotQuery != "" {
		t.Errorf("addon request = %s?%s", gotPath, gotQuery)
	}

	n := len(reqs())
	for _, args := range []map[string]any{
		{"project": "shop"},
		{"project": "shop", "service": "web", "addon": "db"},
		{"project": "shop", "addon": "db", "env": "staging"},
	} {
		_, isErr, err := callText(t, sess, "get_vulnerabilities", args)
		if err == nil && !isErr {
			t.Errorf("%v: want an error", args)
		}
	}
	if len(reqs()) != n {
		t.Error("invalid arguments reached the API")
	}
	for _, r := range reqs() {
		if r.Method != http.MethodGet {
			t.Errorf("read-only tool sent %s %s", r.Method, r.Path)
		}
	}
}
