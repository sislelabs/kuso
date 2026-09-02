package builds

import (
	"context"
	"testing"
	"time"

	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"

	"kuso/server/internal/kube"
)

// Regression for the September 2026 tickero/scubatony incident: five staging
// envs pointed at image tags the sweep had untagged, and only survived because
// their nodes still had the layers cached (imagePullPolicy IfNotPresent). Any
// reschedule bricked them with ImagePullBackOff.
//
// The shape that makes staging special: its build sits in the same
// (project, service) bucket as main-branch builds, and main builds often
// enough to push the staging tag out of the newest-N window within weeks. So
// a staging image is kept ONLY by the env-protection set — there is no other
// line of defence. This pins that the protection matches the real CR shape:
// a custom-kind env, a registry-host-qualified repository, and a worker env
// that runs another service's image.
func TestSweepImagesPastWindow_ProtectsStagingEnvImages(t *testing.T) {
	t.Parallel()

	const reg = "kuso-registry.kuso.svc.cluster.local:5000"
	env := func(name, service, kind, envLabel, repo, tag string) seed {
		return typedSeed(kube.GVREnvironments, "KusoEnvironment", &kube.KusoEnvironment{
			ObjectMeta: metav1.ObjectMeta{
				Name: name, Namespace: "kuso",
				Labels: map[string]string{
					"kuso.sislelabs.com/project": "tickero",
					"kuso.sislelabs.com/service": service,
					"kuso.sislelabs.com/env":     envLabel,
				},
			},
			Spec: kube.KusoEnvironmentSpec{
				Project: "tickero", Service: "tickero-" + service, Kind: kind,
				Image: &kube.KusoImage{Repository: reg + "/" + repo, Tag: tag},
			},
		})
	}
	s := fakeService(t,
		env("tickero-api-production", "api", "production", "production", "tickero/api", "main-new6"),
		env("tickero-api-staging", "api", "custom", "staging", "tickero/api", "stage-old"),
		// The worker service runs the api image (--from-service api).
		env("tickero-worker-staging", "worker", "custom", "staging", "tickero/api", "stage-old"),
	)

	// Archived rows: six newer main builds push the older staging build out of
	// keep=5. Every build CR is long gone, so only the archive knows them.
	rows := []ArchivedImageRecord{
		{BuildName: "tickero-api-stage-old", Project: "tickero", Service: "api", ImageTag: "stage-old", Status: "succeeded", CreatedAt: time.Unix(100, 0)},
	}
	for i := 1; i <= 6; i++ {
		rows = append(rows, ArchivedImageRecord{
			BuildName: "tickero-api-main-new" + string(rune('0'+i)), Project: "tickero", Service: "api",
			ImageTag: "main-new" + string(rune('0'+i)), Status: "succeeded", CreatedAt: time.Unix(int64(1000+i), 0),
		})
	}
	lister := &fakeArchivedLister{rows: rows}
	del := &fakeDeleter{digests: map[string]string{}}
	for _, r := range rows {
		del.digests["tickero/api:"+r.ImageTag] = "sha256:" + r.ImageTag
	}

	if _, err := SweepImagesPastWindow(context.Background(), s.Kube, "kuso", lister, del, 5, nil); err != nil {
		t.Fatalf("sweep: %v", err)
	}

	deleted := map[string]bool{}
	for _, d := range del.deleted {
		deleted[d] = true
	}
	if deleted["tickero/api:stage-old"] {
		t.Errorf("sweep untagged the image a live staging env points at (deleted=%v) — "+
			"the env only keeps running until a node without the cached layers schedules it", del.deleted)
	}
	if deleted["tickero/api:main-new6"] {
		t.Errorf("sweep untagged the production env's image: %v", del.deleted)
	}
	if !deleted["tickero/api:main-new1"] {
		t.Errorf("sweep kept the oldest unreferenced main tag; deleted=%v — a no-op sweep would pass this test", del.deleted)
	}
}
