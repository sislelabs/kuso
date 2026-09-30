package handlers

import (
	"testing"

	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"

	"kuso/server/internal/builds"
	"kuso/server/internal/kube"
)

func TestToBuildSummary_StateFields(t *testing.T) {
	t.Parallel()
	mk := func(ann map[string]string) kube.KusoBuild {
		return kube.KusoBuild{ObjectMeta: metav1.ObjectMeta{Name: "b", Annotations: ann}}
	}
	c := toBuildSummary(mk(map[string]string{builds.AnnBuildPhase: "cancelled", builds.AnnBuildMessage: "cancelled by user"}))
	if c.CancelReason != "cancelled by user" {
		t.Errorf("cancelReason = %q", c.CancelReason)
	}
	r := toBuildSummary(mk(map[string]string{
		builds.AnnBuildPhase:     "release-failed",
		builds.AnnReleaseJob:     "web-production-release-abc",
		builds.AnnReleaseLogTail: "line1\nline2",
	}))
	if r.ReleaseJob != "web-production-release-abc" || r.ReleaseLogTail != "line1\nline2" {
		t.Errorf("release fields = %q / %q", r.ReleaseJob, r.ReleaseLogTail)
	}
	q := toBuildSummary(mk(map[string]string{builds.AnnBuildPhase: "queued", "kuso.sislelabs.com/ci-gate": "waiting"}))
	if q.WaitingFor != "GitHub CI checks" {
		t.Errorf("waitingFor = %q", q.WaitingFor)
	}
	ok := toBuildSummary(mk(map[string]string{builds.AnnBuildPhase: "succeeded"}))
	if ok.CancelReason != "" || ok.ReleaseJob != "" || ok.WaitingFor != "" {
		t.Errorf("succeeded build carries state fields: %+v", ok)
	}
}
