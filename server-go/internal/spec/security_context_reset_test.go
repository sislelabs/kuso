package spec

import (
	"testing"

	"kuso/server/internal/kube"
)

// LIFE-14: removing the securityContext block from kuso.yaml must reset
// it like every other field; nil meant "leave alone", so an added
// capability survived its removal from the file.
func TestServiceDiff_OmittedSecurityContextResets(t *testing.T) {
	live := &kube.KusoService{}
	live.Spec.SecurityContext = &kube.KusoSecurityContext{Capabilities: &kube.KusoCapabilities{Add: []string{"NET_BIND_SERVICE"}}}
	d := exportService("shop", *live)
	d.SecurityContext = nil

	req, fields := diffServiceSpec(live, d)
	if req.SecurityContext == nil || req.SecurityContext.Capabilities != nil {
		t.Fatalf("omitted securityContext patch = %+v, want an explicit empty reset", req.SecurityContext)
	}
	found := false
	for _, f := range fields {
		found = found || f.Field == "securityContext"
	}
	if !found {
		t.Errorf("plan does not show the securityContext reset: %+v", fields)
	}

	// No live block and none in the file: nothing to send.
	live.Spec.SecurityContext = nil
	if req, _ := diffServiceSpec(live, exportService("shop", *live)); req.SecurityContext != nil {
		t.Errorf("unchanged (absent) securityContext still sent: %+v", req.SecurityContext)
	}
}
