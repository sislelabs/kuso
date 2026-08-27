package builds

import (
	"context"
	"strings"
	"testing"
	"time"

	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"

	"kuso/server/internal/kube"
)

// fakeArchivedLister returns a fixed set of archived rows regardless of
// the namespace it is asked for — which is exactly how the production
// adapter behaves: BuildRecord has no namespace column, so the db query
// is cluster-wide.
type fakeArchivedLister struct {
	rows    []ArchivedImageRecord
	cleared []string
}

func (f *fakeArchivedLister) ListArchivedImages(_ context.Context, _ string) ([]ArchivedImageRecord, error) {
	return f.rows, nil
}

func (f *fakeArchivedLister) ClearImageTag(_ context.Context, project, buildName string) error {
	f.cleared = append(f.cleared, project+"/"+buildName)
	return nil
}

// TestSweepImagesPastWindow_IgnoresOtherNamespacesArchivedRows is the
// regression for the cross-namespace untag found 2026-08-27.
//
// The sweep runs once per namespace. Its live-CR list and its `protected`
// set (built from ListKusoEnvironments) are both namespace-scoped, but the
// archived-record lister is cluster-wide. So when sweeping namespace
// kuso-alpha, archived rows for project "beta" (namespace kuso-beta) were
// merged in, formed (project, service) groups with no live CRs and nothing
// protecting them, and their images were untagged — including images beta
// was actively running. That re-opened the exact ImagePullBackOff outage
// the protection block was written to prevent.
func TestSweepImagesPastWindow_IgnoresOtherNamespacesArchivedRows(t *testing.T) {
	t.Parallel()
	at := func(s int64) time.Time { return time.Unix(s, 0) }

	// Namespace under sweep holds project "alpha" only.
	seedAlphaBuild := func(name, tag string, ts int64) seed {
		return typedSeed(kube.GVRBuilds, "KusoBuild", &kube.KusoBuild{
			ObjectMeta: metav1.ObjectMeta{
				Name: name, Namespace: "kuso",
				CreationTimestamp: metav1.NewTime(at(ts)),
				Labels:            map[string]string{"kuso.sislelabs.com/build-state": "done"},
				Annotations:       map[string]string{annPhase: "succeeded"},
			},
			Spec: kube.KusoBuildSpec{
				Project: "alpha", Service: "alpha-web",
				Image: &kube.KusoImage{Repository: "kuso-registry:5000/alpha/web", Tag: tag},
			},
		})
	}
	s := fakeService(t,
		seedAlphaBuild("alpha-web-1", "alpha001", 100),
		seedAlphaBuild("alpha-web-2", "alpha002", 200),
		seedAlphaBuild("alpha-web-3", "alpha003", 300),
	)

	// Cluster-wide archived rows: alpha's own history PLUS beta's, which
	// lives in a different namespace and must not be touched here.
	lister := &fakeArchivedLister{rows: []ArchivedImageRecord{
		{BuildName: "alpha-web-0", Project: "alpha", Service: "web",
			ImageTag: "alpha000", Status: "succeeded", CreatedAt: at(50)},
		{BuildName: "beta-api-1", Project: "beta", Service: "api",
			ImageTag: "betalive00", Status: "succeeded", CreatedAt: at(10)},
		{BuildName: "beta-api-2", Project: "beta", Service: "api",
			ImageTag: "betalive01", Status: "succeeded", CreatedAt: at(20)},
	}}

	del := &fakeDeleter{}
	// keep=1: everything past the newest build per service is sweepable,
	// so beta's rows would certainly be untagged if they were considered.
	n, err := SweepImagesPastWindow(context.Background(), s.Kube, "kuso", lister, del, 1, nil)
	if err != nil {
		t.Fatalf("sweep: %v", err)
	}

	for _, d := range del.deleted {
		if strings.Contains(d, "beta") {
			t.Errorf("sweep of the alpha namespace untagged an image belonging to another namespace: %q "+
				"(all deletes: %v)", d, del.deleted)
		}
	}
	for _, c := range lister.cleared {
		if strings.HasPrefix(c, "beta/") {
			t.Errorf("sweep of the alpha namespace cleared another namespace's build record: %q", c)
		}
	}
	// Sanity: the sweep must still do its actual job in its own namespace,
	// or this test would pass trivially with a broken no-op sweep.
	if n == 0 {
		t.Error("sweep untagged nothing at all — expected alpha's own past-window images to be swept")
	}
}
