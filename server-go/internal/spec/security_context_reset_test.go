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

// An export -> apply round trip must keep the user namespace: dropping
// hostUsers from the export would make `kuso apply` silently turn it off.
func TestServiceDiff_HostUsersRoundTrips(t *testing.T) {
	f := false
	live := &kube.KusoService{}
	live.Spec.SecurityContext = &kube.KusoSecurityContext{HostUsers: &f}
	d := exportService("shop", *live)
	if d.SecurityContext == nil || d.SecurityContext.HostUsers == nil || *d.SecurityContext.HostUsers {
		t.Fatalf("export securityContext = %+v, want hostUsers false", d.SecurityContext)
	}
	if req, fields := diffServiceSpec(live, d); req.SecurityContext != nil {
		t.Errorf("unchanged hostUsers still sent: %+v (%+v)", req.SecurityContext, fields)
	}
	if got := toKubeSecurityContext(d.SecurityContext); got.PodHostUsers() == nil {
		t.Errorf("apply mapping dropped hostUsers: %+v", got)
	}
}
